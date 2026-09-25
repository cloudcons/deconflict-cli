package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/client"
	"github.com/cloudcons/deconflict-cli/pkg/protocol"
)

// boardServer answers /v1/needs with the whole board whatever it is asked,
// the way a registry from before the ground filter does, and records the
// query it was sent.
func boardServer(t *testing.T, asked *url.Values) *httptest.Server {
	t.Helper()
	now := time.Now().UTC()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/needs" {
			http.NotFound(w, r)
			return
		}
		*asked = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]protocol.Post{
			{ID: "wcp_mig", Room: "infra", Kind: protocol.KindNeed, State: protocol.StateOpen, AgentID: "ana/claude", Subject: "renumber 0042", Repo: "api", Paths: []string{"db/migrations/**"}, LastActivityAt: now},
			{ID: "wcp_mine", Room: "infra", Kind: protocol.KindNeed, State: protocol.StateOpen, AgentID: "bo/claude", Subject: "my own ask", Repo: "api", LastActivityAt: now},
			{ID: "wcp_web", Room: "infra", Kind: protocol.KindNeed, State: protocol.StateLapsed, AgentID: "cy/codex", Subject: "restyle checkout", Repo: "web", Paths: []string{"src/**"}, LastActivityAt: now},
			{ID: "wcp_room", Room: "lobby", Kind: protocol.KindNeed, State: protocol.StateOpen, AgentID: "cy/codex", Subject: "anyone free?", LastActivityAt: now},
		})
	}))
}

func TestSessionStartShowsNeedsInThisRepository(t *testing.T) {
	var asked url.Values
	server := boardServer(t, &asked)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")
	t.Setenv("DECONFLICT_AGENT", "bo/claude")
	t.Setenv("DECONFLICT_REPO", "api")

	got := needContext(server.URL, t.TempDir())
	if asked.Get("repo") != "api" {
		t.Errorf("asked the registry for %v", asked)
	}
	if !strings.Contains(got, "wcp_mig") || !strings.Contains(got, "in api:db/migrations/**") {
		t.Errorf("the need in this repository is missing:\n%s", got)
	}
	for _, id := range []string{"wcp_mine", "wcp_web", "wcp_room"} {
		if strings.Contains(got, id) {
			t.Errorf("%s should not be shown here:\n%s", id, got)
		}
	}
}

func TestSessionStartIsSilentWithoutNeeds(t *testing.T) {
	var asked url.Values
	server := boardServer(t, &asked)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")
	t.Setenv("DECONFLICT_AGENT", "bo/claude")
	t.Setenv("DECONFLICT_REPO", "billing")
	if got := needContext(server.URL, t.TempDir()); got != "" {
		t.Errorf("session context = %q", got)
	}
}

func TestClaimShowsNeedsOnItsGround(t *testing.T) {
	var asked url.Values
	server := boardServer(t, &asked)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")
	t.Setenv("DECONFLICT_AGENT", "bo/claude")

	st, err := openStore(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.(*client.HTTPStore); !ok {
		t.Fatalf("store is %T", st)
	}
	var ids []string
	for _, n := range claimNeeds(st, "api", []string{"db"}) {
		ids = append(ids, n.ID)
	}
	if strings.Join(ids, ",") != "wcp_mig" {
		t.Errorf("needs on db in api = %v", ids)
	}
	if asked.Get("repo") != "api" || strings.Join(asked["path"], ",") != "db" {
		t.Errorf("asked the registry for %v", asked)
	}
	if got := claimNeeds(st, "api", []string{"src/auth"}); len(got) != 0 {
		t.Errorf("needs on src/auth = %v", got)
	}
}
