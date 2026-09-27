package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// registryStub serves the mailbox and the questions route from mutable state,
// so a test can add mail between two hook calls.
type registryStub struct {
	mail      []map[string]any
	questions []map[string]any
	calls     int
}

func (s *registryStub) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/agents/register":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "reg_1"})
		case r.URL.Path == "/v1/messages":
			_ = json.NewEncoder(w).Encode(s.mail)
		case strings.HasSuffix(r.URL.Path, "/questions"):
			_ = json.NewEncoder(w).Encode(s.questions)
		case r.URL.Path == "/v1/events":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.URL.Path == "/v1/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DECONFLICT_TOKEN", "t")
	t.Setenv("DECONFLICT_AGENT", "bo/claude")
	t.Setenv("TMPDIR", t.TempDir())
	return srv.URL
}

func letter(id, body string) map[string]any {
	return map[string]any{"delivery_id": id, "sender_agent_id": "ana/claude", "kind": "message", "payload": map[string]any{"body": body}}
}

// Mail is read at every boundary now, so each delivery is shown once: the new
// one must not be buried under the ones already read.
func TestMailboxShowsEachDeliveryOnce(t *testing.T) {
	reg := &registryStub{mail: []map[string]any{letter("dlv_1", "first")}}
	dsn := reg.serve(t)
	in := hookInput{SessionID: "s"}

	if got := mailboxContext(dsn, t.TempDir(), in, false); !strings.Contains(got, "first") {
		t.Fatalf("first read: %q", got)
	}
	if got := mailboxContext(dsn, t.TempDir(), in, false); got != "" {
		t.Fatalf("nothing new, yet it spoke: %q", got)
	}
	reg.mail = append(reg.mail, letter("dlv_2", "second"))
	got := mailboxContext(dsn, t.TempDir(), in, false)
	if !strings.Contains(got, "second") || strings.Contains(got, "first") || !strings.Contains(got, "1 earlier message") {
		t.Fatalf("new mail should arrive alone, with a count of the rest: %q", got)
	}
	// A new session start shows everything: a resumed session lost what it saw.
	if got := mailboxContext(dsn, t.TempDir(), in, true); !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("session start should show the whole mailbox: %q", got)
	}
}

// A tool call is frequent and the registry is remote: the per-tool boundary
// asks at most once per interval.
func TestDueThrottlesPerSession(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	if !due("s", "mail", time.Hour) {
		t.Fatal("first call should be due")
	}
	if due("s", "mail", time.Hour) {
		t.Fatal("second call inside the interval should not be due")
	}
	if !due("other", "mail", time.Hour) {
		t.Fatal("another session has its own clock")
	}
	if !due("s", "mail", 0) {
		t.Fatal("a zero interval is always due")
	}
}

// Only a commit or a push is looked at; every other shell command costs nothing.
func TestCommitCheckOnlyLooksAtCommits(t *testing.T) {
	for cmd, want := range map[string]bool{
		"git commit -m x":                        true,
		"cd api && git commit -am 'fix'":         true,
		"git -C /repo push origin HEAD":          true,
		"git status":                             false,
		"echo git committed":                     false,
		"gitcommit":                              false,
		"make test; git push --force-with-lease": true,
	} {
		if got := gitCommit.MatchString(cmd); got != want {
			t.Errorf("%q: matched=%v, want %v", cmd, got, want)
		}
	}
	h := newHookRepo(t)
	t.Setenv("DECONFLICT_AGENT", "me")
	in := hookInput{SessionID: "s", CWD: h.mine, ToolName: "Bash", ToolInput: map[string]any{"command": "git commit -m wip"}}
	if got := commitContext(h.dsn, h.mine, in); !strings.Contains(got, "with no claim") {
		t.Errorf("committing unclaimed work said %q", got)
	}
	if got := commitContext(h.dsn, h.mine, in); got != "" {
		t.Errorf("said it twice: %q", got)
	}
	in.ToolInput["command"] = "go test ./..."
	if got := commitContext(h.dsn, h.mine, in); got != "" {
		t.Errorf("a non-commit command said %q", got)
	}
}

// The stop hook raises what waits on the agent, once, and then lets it go.
func TestStopHookRaisesWhatWaitsOnceThenLetsGo(t *testing.T) {
	reg := &registryStub{
		mail: []map[string]any{letter("dlv_9", "please look")},
		questions: []map[string]any{{
			"id": "wcp_q", "kind": "watercooler", "subject": "who owns the lane pin?", "asker": "ana/claude",
			"assume": "I take it", "where": "#lobby", "answer": "deconflict cooler answer wcp_q --body '…'",
		}},
	}
	dsn := reg.serve(t)
	cwd := t.TempDir()
	in := hookInput{SessionID: "s", CWD: cwd}

	var out bytes.Buffer
	if err := stopHook(dsn, cwd, in, &out); err != nil {
		t.Fatal(err)
	}
	var got struct{ Decision, Reason string }
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stop output is not JSON: %q", out.String())
	}
	for _, want := range []string{"who owns the lane pin?", "cooler answer wcp_q", "I take it", "message ack dlv_9"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason lacks %q:\n%s", want, got.Reason)
		}
	}
	if got.Decision != "block" {
		t.Errorf("decision = %q, want block", got.Decision)
	}

	out.Reset()
	in.StopHookActive = true
	if err := stopHook(dsn, cwd, in, &out); err != nil || out.Len() != 0 {
		t.Fatalf("second stop should let the agent go, got %q (%v)", out.String(), err)
	}
	// New mail is new, and is raised.
	reg.mail = append(reg.mail, letter("dlv_10", "one more"))
	out.Reset()
	if err := stopHook(dsn, cwd, in, &out); err != nil || !strings.Contains(out.String(), "dlv_10") {
		t.Fatalf("new mail at stop: %q (%v)", out.String(), err)
	}
}

// A registry without the questions route is a quiet no, not a stuck agent.
func TestStopHookWithNothingWaitingIsSilent(t *testing.T) {
	reg := &registryStub{}
	dsn := reg.serve(t)
	var out bytes.Buffer
	if err := stopHook(dsn, t.TempDir(), hookInput{SessionID: "s"}, &out); err != nil || out.Len() != 0 {
		t.Fatalf("nothing waiting, got %q (%v)", out.String(), err)
	}
}

// Every boundary is installed, and a second install changes nothing.
func TestInstallPutsTheRegistryAtEveryBoundary(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })
	doc := readBack(t, settings)
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		if got := commands(doc, event); len(got) != 1 || !strings.HasPrefix(got[0], "deconflict hook ") {
			t.Errorf("%s: %v", event, got)
		}
	}
	if m := matchers(doc, "PreToolUse"); len(m) != 1 || !strings.Contains(m[0], "Bash") {
		t.Errorf("pre-tool matcher %v does not see commits", m)
	}
	p := install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })
	if p.changes[0].action != "already set" {
		t.Errorf("second install changed something: %s", p.changes[0].action)
	}
}
