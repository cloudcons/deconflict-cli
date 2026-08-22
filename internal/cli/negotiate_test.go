package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudcons/deconflict/internal/negotiation"
)

func TestNegotiateRequestUsesAuthenticatedProtocolTransport(t *testing.T) {
	var got negotiation.AccessRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/negotiations" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-agent-token" {
			t.Errorf("authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(negotiation.Session{ID: "neg_test", Status: negotiation.Negotiating})
	}))
	defer server.Close()

	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")
	t.Setenv("DECONFLICT_AGENT", "auth-refactor/codex-1")
	var out bytes.Buffer
	err := cmdNegotiate([]string{"request", "--store", server.URL, "--paths", "internal/auth/**,cmd/**", "--objective", "refresh authentication", "--success", "tests pass,no legacy callback", "--access", "modify", "--lease", "30m", "--runtime", "codex", "--model", "gpt-5"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.ID != "auth-refactor/codex-1" || got.Agent.Runtime != "codex" || got.Agent.Model != "gpt-5" {
		t.Fatalf("agent = %+v", got.Agent)
	}
	if got.Objective.Summary != "refresh authentication" || len(got.Objective.SuccessCriteria) != 2 {
		t.Fatalf("objective = %+v", got.Objective)
	}
	if len(got.Resources) != 1 || len(got.Resources[0].Paths) != 2 || got.Lease.Duration != "30m" {
		t.Fatalf("request = %+v", got)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"id": "neg_test"`)) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestNegotiateLeaseSendsDelegatedAgentAndDuration(t *testing.T) {
	var got negotiation.LeaseInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/negotiations/neg_test/lease" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(negotiation.Session{ID: "neg_test", Status: negotiation.Executing})
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := cmdNegotiate([]string{"lease", "neg_test", "--store", server.URL, "--agent", "auth/codex", "--duration", "20m"}, &out); err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "auth/codex" || got.Duration != "20m" {
		t.Fatalf("lease = %+v", got)
	}
}
