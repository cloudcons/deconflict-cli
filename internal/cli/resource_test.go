package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/protocol"
)

type seenRequest struct {
	Method, Path string
	Body         map[string]any
}

// resourceServer answers like a registry holding one exclusive lock on staging,
// taken by ana/claude, and records what the client sent.
func resourceServer(t *testing.T, seen *[]seenRequest) *httptest.Server {
	t.Helper()
	held := protocol.ResourceLock{ID: "lck_ana", ResourceID: "res_1", Resource: "staging", AgentID: "ana/claude", Mode: protocol.Exclusive,
		DelegatedLogin: "ana", Reason: "migration 42", ExpiresAt: time.Now().Add(time.Hour)}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{Method: r.Method, Path: r.URL.Path}
		_ = json.NewDecoder(r.Body).Decode(&req.Body)
		*seen = append(*seen, req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/resources":
			_ = json.NewEncoder(w).Encode([]protocol.Resource{{ID: "res_1", Name: "staging", Kind: "env", Locks: []protocol.ResourceLock{held}}})
		case r.URL.Path == "/v1/resources/staging/locks":
			mode, _ := req.Body["mode"].(string)
			res := protocol.LockResult{Lock: protocol.ResourceLock{ID: "lck_me", Resource: "staging", AgentID: "bo/claude", Mode: protocol.LockMode(mode), ExpiresAt: time.Now().Add(time.Hour)}, Conflicts: []protocol.ResourceLock{}}
			if protocol.Contends(res.Lock.Mode, held.Mode) {
				res.Conflicts = append(res.Conflicts, held)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(res)
		case r.URL.Path == "/v1/resources/staging/unlock":
			_ = json.NewEncoder(w).Encode([]protocol.ResourceLock{})
		default:
			http.Error(w, `no resource named "stagin" — known: staging`, http.StatusNotFound)
		}
	}))
}

// A held resource is reported, never refused: the lock is printed, the holder
// is named with their reason, and the exit code says "contended" the way an
// overlapping claim does.
func TestLockReportsContentionWithExitThree(t *testing.T) {
	var seen []seenRequest
	server := resourceServer(t, &seen)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")

	var out bytes.Buffer
	err := cmdLock([]string{"staging", "--agent", "bo/claude", "--reason", "deploy #88", "--store", server.URL}, &out)
	if n, ok := Code(err); !ok || n != 3 {
		t.Fatalf("contended lock returned %v, want exit 3", err)
	}
	for _, want := range []string{"locked staging lck_me", "ana/claude for ana", "migration 42", "advisory"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if got := seen[0].Body; got["agent_id"] != "bo/claude" || got["reason"] != "deploy #88" {
		t.Errorf("request body = %v", got)
	}

	// Positional name after the flags works too, and --shared under an
	// exclusive holder still contends.
	out.Reset()
	err = cmdLock([]string{"--shared", "--agent", "bo/claude", "--store", server.URL, "staging"}, &out)
	if n, _ := Code(err); n != 3 {
		t.Fatalf("shared under exclusive: %v, want exit 3", err)
	}
	if seen[1].Body["mode"] != "shared" {
		t.Errorf("--shared sent mode %v", seen[1].Body["mode"])
	}
}

func TestUnlockAllReleasesEveryAgentOfTheAccount(t *testing.T) {
	var seen []seenRequest
	server := resourceServer(t, &seen)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")

	var out bytes.Buffer
	if err := cmdUnlock([]string{"staging", "--all", "--store", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if _, set := seen[0].Body["agent_id"]; set {
		t.Errorf("--all still narrowed to an agent: %v", seen[0].Body)
	}
	if !strings.Contains(out.String(), "nothing to release") {
		t.Errorf("an unlock that released nothing did not say so: %q", out.String())
	}
}

// The session hook names other agents' locks and leaves out this agent's own.
func TestSessionContextNamesOthersLocks(t *testing.T) {
	var seen []seenRequest
	server := resourceServer(t, &seen)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")

	t.Setenv("DECONFLICT_AGENT", "bo/claude")
	if got := resourceContext(server.URL); !strings.Contains(got, "staging: ana/claude for ana — exclusive") {
		t.Errorf("session context = %q", got)
	}
	t.Setenv("DECONFLICT_AGENT", "ana/claude")
	if got := resourceContext(server.URL); got != "" {
		t.Errorf("an agent was told about its own lock: %q", got)
	}
}
