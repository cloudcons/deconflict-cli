package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cloudcons/deconflict/internal/claim"
	"github.com/cloudcons/deconflict/internal/settings"
)

// HTTPStore talks to `deconflict serve` — the shared-registry backend for a team.
type HTTPStore struct {
	Base   string
	Token  string
	Client *http.Client
}

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
			return nil, fmt.Errorf("%s: %s", h.Base, bytes.TrimSpace(b))
		}
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(b))
	}
	return b, nil
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
