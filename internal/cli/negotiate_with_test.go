package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/claim"
	"github.com/cloudcons/deconflict-cli/pkg/protocol"
)

// negotiate with builds the request from the two claims: where they meet, and
// what this agent's claim is for. Nothing typed but the other claim's id.
func TestNegotiateWithBuildsTheRequestFromBothClaims(t *testing.T) {
	now := time.Now().UTC()
	events := []claim.Event{
		{Op: "claim", TS: now, Claim: claim.Claim{ID: "c-me", Repo: "example.test/acme/api", Agent: "me", Paths: []string{"telemetry/**"}, What: "add error spans", Created: now, Expires: now.Add(time.Hour)}},
		{Op: "claim", TS: now, Claim: claim.Claim{ID: "c-ana", Repo: "example.test/acme/api", Agent: "ana/claude", Paths: []string{"telemetry/contract.yaml", "docs/**"}, What: "rework the contract", Created: now, Expires: now.Add(time.Hour)}},
	}
	var got protocol.AccessRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/events":
			_ = json.NewEncoder(w).Encode(events)
		case "/v1/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case "/v1/negotiations":
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(protocol.Session{ID: "neg_1", Participants: []protocol.Participant{
				{Agent: protocol.AgentIdentity{ID: "me"}}, {Agent: protocol.AgentIdentity{ID: "ana/claude"}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h := newHookRepo(t)
	t.Setenv("DECONFLICT_AGENT", "me")
	t.Setenv("DECONFLICT_TOKEN", "t")
	writeCurrent(h.mine, "c-me")
	restore := chdir(t, h.mine)
	defer restore()

	var out bytes.Buffer
	if err := cmdNegotiate([]string{"with", "c-ana", "--store", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if len(got.Resources) != 1 || strings.Join(got.Resources[0].Paths, ",") != "telemetry/**" || got.Resources[0].Access != "modify" {
		t.Errorf("requested %+v, want my side of the overlap, to modify", got.Resources)
	}
	if got.Objective.Summary != "add error spans" || got.Repository != "example.test/acme/api" || got.Agent.ID != "me" {
		t.Errorf("request = %+v", got)
	}
	if !strings.Contains(out.String(), "neg_1") || !strings.Contains(out.String(), "ana/claude told") {
		t.Errorf("output: %q", out.String())
	}

	// Claims that do not meet, and a claim that does not exist, are refused.
	if err := cmdNegotiate([]string{"with", "c-nope", "--store", srv.URL}, &out); err == nil {
		t.Error("an unknown claim was negotiated over")
	}
	_ = os.Remove(currentPath(h.mine))
	if err := cmdNegotiate([]string{"with", "c-ana", "--store", srv.URL}, &out); err == nil || !strings.Contains(err.Error(), "claim your work first") {
		t.Errorf("without a claim of my own: %v", err)
	}
}
