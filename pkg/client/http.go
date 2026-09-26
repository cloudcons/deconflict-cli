package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/claim"
	"github.com/cloudcons/deconflict-cli/pkg/settings"
)

// HTTPStore talks to `deconflict serve` — the shared-registry backend for a team.
type HTTPStore struct {
	Base   string
	Token  string
	Client *http.Client

	// OnUnregistered, when set, runs once when the registry refuses a request
	// because the acting agent id was never registered under this credential,
	// and the request is then retried once. It gets a copy of the store with
	// the hook cleared, so registering through it cannot recurse.
	OnUnregistered func(*HTTPStore) error
}

// UnregisteredError is the registry refusing an agent id this credential
// never registered. Presence lapsing does not cause it; acting under a new id
// does — and the CLI derives ids from the worktree it runs in.
type UnregisteredError struct{ msg string }

func (e *UnregisteredError) Error() string { return e.msg }

// unregisteredPrefix is the clause the registry leads that refusal with.
const unregisteredPrefix = "register this autonomous agent"

func (h *HTTPStore) Describe() string { return h.Base }

func (h *HTTPStore) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	// Short timeout on purpose: the registry is advisory, so an agent must
	// never be blocked by it being down. Callers degrade to a warning.
	return &http.Client{Timeout: 5 * time.Second}
}

func (h *HTTPStore) do(method, path string, body any) ([]byte, error) {
	b, err := h.send(method, path, body)
	var unregistered *UnregisteredError
	if h.OnUnregistered == nil || !errors.As(err, &unregistered) {
		return b, err
	}
	plain := *h
	plain.OnUnregistered = nil
	if h.OnUnregistered(&plain) != nil {
		// The refusal says what to do; a failed registration would only
		// bury it.
		return nil, err
	}
	return plain.send(method, path, body)
}

func (h *HTTPStore) send(method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.Base+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		// The two statuses worth translating. An agent that hits either of
		// these has a human problem, not a network problem, and the fix is one
		// command away — saying so here saves reading the server log.
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("%s rejected this credential — run `deconflict login --server %s`", h.Base, h.Base)
		case http.StatusForbidden:
			msg := bytes.TrimSpace(b)
			if bytes.HasPrefix(msg, []byte(unregisteredPrefix)) {
				return nil, &UnregisteredError{fmt.Sprintf("%s: %s", h.Base, msg)}
			}
			return nil, fmt.Errorf("%s: %s", h.Base, msg)
		}
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(b))
	}
	return b, nil
}

// JSON exposes the authenticated registry transport to higher-level protocol
// clients such as negotiation. Claims remain Store's small common interface;
// coordination exists only on a shared HTTP registry with identity and tenancy.
func (h *HTTPStore) JSON(method, path string, body, out any) error {
	b, err := h.do(method, path, body)
	if err != nil {
		return err
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// Settings fetches operator settings, degrading to defaults if the registry is
// unreachable — the same rule as everywhere else: never block on it.
func (h *HTTPStore) Settings() settings.Settings {
	b, err := h.do(http.MethodGet, "/v1/settings", nil)
	if err != nil {
		return settings.Defaults()
	}
	s := settings.Defaults()
	if err := json.Unmarshal(b, &s); err != nil {
		return settings.Defaults()
	}
	return s
}

func (h *HTTPStore) Append(e claim.Event) error {
	_, err := h.do(http.MethodPost, "/v1/events", e)
	return err
}

func (h *HTTPStore) Events() ([]claim.Event, error) {
	b, err := h.do(http.MethodGet, "/v1/events", nil)
	if err != nil {
		return nil, err
	}
	var out []claim.Event
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
