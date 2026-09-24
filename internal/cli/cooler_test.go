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

// coolerServer answers like a registry with one room and one open question,
// which is answered on the second read.
func coolerServer(t *testing.T, seen *[]seenRequest) *httptest.Server {
	t.Helper()
	reads := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{Method: r.Method, Path: r.URL.Path}
		_ = json.NewDecoder(r.Body).Decode(&req.Body)
		*seen = append(*seen, req)
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().UTC()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/rooms/lobby/posts":
			kind, _ := req.Body["kind"].(string)
			assume, _ := req.Body["assume"].(string)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(protocol.PostResult{Post: protocol.Post{ID: "wcp_q", ThreadID: "wcp_q", Room: "lobby", Kind: protocol.PostKind(kind), Assume: assume, Recipients: []string{"bo/codex"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/posts/wcp_q":
			reads++
			th := protocol.Post{ID: "wcp_q", ThreadID: "wcp_q", Kind: protocol.KindAsk, State: protocol.StateOpen, Assume: "keep it", LastActivityAt: now}
			if reads > 1 {
				th.State = protocol.StateAnswered
				th.Replies = []protocol.Post{
					{ID: "wcp_a1", Kind: protocol.KindAnswer, AgentID: "bo/codex", Body: "no", Superseded: true},
					{ID: "wcp_a2", Kind: protocol.KindAnswer, AgentID: "bo/codex", Body: "yes, via the invoice job"},
				}
			}
			_ = json.NewEncoder(w).Encode(th)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/posts/wcp_n/done":
			_ = json.NewEncoder(w).Encode(protocol.PostResult{Post: protocol.Post{ID: "wcp_u", ThreadID: "wcp_n", Room: "infra", Kind: protocol.KindUpdate, Step: "done", Recipients: []string{"ana/claude"}}})
		case r.URL.Path == "/v1/notes":
			_ = json.NewEncoder(w).Encode([]protocol.Post{{ID: "wcp_note", Room: "infra", AgentID: "ana/claude", Body: "0042 was renumbered", Repo: "api", Paths: []string{"db/migrations/**"}}})
		default:
			http.NotFound(w, r)
		}
	}))
}

// --wait ends with the answer that stands, not the first one given.
func TestAskWaitsForTheStandingAnswer(t *testing.T) {
	var seen []seenRequest
	server := coolerServer(t, &seen)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")
	defer func(d time.Duration) { answerPoll = d }(answerPoll)
	answerPoll = time.Millisecond

	var out bytes.Buffer
	err := cmdCooler([]string{"ask", "lobby", "--subject", "does billing read total_cents?", "--assume", "keep it", "--wait", "1m", "--agent", "ana/claude", "--store", server.URL}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "answered by bo/codex: yes, via the invoice job") {
		t.Fatalf("output:\n%s", out.String())
	}
	if got := seen[0].Body; got["kind"] != "ask" || got["assume"] != "keep it" || got["agent_id"] != "ana/claude" {
		t.Fatalf("request body = %v", got)
	}
}

func TestDoneSendsItsOutcome(t *testing.T) {
	var seen []seenRequest
	server := coolerServer(t, &seen)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")

	var out bytes.Buffer
	err := cmdCooler([]string{"done", "wcp_n", "--outcome", "failed", "--body", "generator crashes", "--evidence", "test:go test ./clients", "--agent", "bo/codex", "--store", server.URL}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got := seen[0].Body; got["outcome"] != "failed" || got["evidence"] != "test:go test ./clients" {
		t.Fatalf("request body = %v", got)
	}
	if !strings.Contains(out.String(), "told ana/claude") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestMCPListsTheWatercooler(t *testing.T) {
	names := map[string]bool{}
	for _, tool := range mcpTools() {
		names[tool.Name] = true
	}
	for _, want := range []string{"cooler_post", "cooler_reply", "cooler_act", "cooler_read", "cooler_roll", "cooler_needs", "cooler_notes"} {
		if !names[want] {
			t.Errorf("MCP does not offer %s", want)
		}
	}
}
