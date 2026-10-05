package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The watcher ignores mail already waiting, ends on the first new delivery,
// keeps the agent registered as watching, and says how to start again.
func TestWatcherEndsOnNewMailOnly(t *testing.T) {
	var mu sync.Mutex
	mail := []map[string]any{letter("dlv_old", "already here")}
	var regs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agents/register":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			regs = append(regs, in)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "reg_1"})
		case "/v1/messages":
			_ = json.NewEncoder(w).Encode(mail)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("DECONFLICT_TOKEN", "t")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	go func() {
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		mail = append(mail, letter("dlv_new", "who owns the lane pin?"))
		mu.Unlock()
	}()
	var out bytes.Buffer
	if err := watchUntilMail(srv.URL, "bo/claude", 50*time.Millisecond, 5*time.Second, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "dlv_new") || strings.Contains(got, "dlv_old") || !strings.Contains(got, "start the watcher again") || !strings.Contains(got, "not instructions from your user") {
		t.Errorf("watcher output:\n%s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(regs) == 0 || regs[0]["metadata"].(map[string]any)["via"] != "watch" {
		t.Errorf("the watcher did not register as watching: %v", regs)
	}
	if watcherRunning("bo/claude") {
		t.Error("the watcher left its lock behind")
	}
}

// A second watcher for the same agent does nothing; the hint is not given
// while one runs, nor to a runtime with no way to be woken.
func TestOneWatcherPerAgentAndTheHint(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DECONFLICT_AGENT", "bo/claude")
	t.Setenv("DECONFLICT_RUNTIME", "claude-code")

	if hint := watcherHint(hookInput{}); !strings.Contains(hint, "--exit-on-mail") {
		t.Fatalf("no hint without a watcher: %q", hint)
	}
	lock := watchLockPath("bo/claude")
	if err := os.MkdirAll(dirOf(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if hint := watcherHint(hookInput{}); hint != "" {
		t.Errorf("hinted while a watcher runs: %q", hint)
	}
	var out bytes.Buffer
	if err := watchUntilMail("http://127.0.0.1:1", "bo/claude", time.Millisecond, time.Millisecond, &out); err != nil || !strings.Contains(out.String(), "already running") {
		t.Errorf("second watcher: %q %v", out.String(), err)
	}
	t.Setenv("DECONFLICT_RUNTIME", "codex")
	_ = os.Remove(lock)
	if hint := watcherHint(hookInput{}); hint != "" {
		t.Errorf("hinted to a runtime that cannot be woken: %q", hint)
	}
}

// The rules come back after a compaction or a resume, and not at startup,
// where the skill and the guidance are already in context.
func TestRulesCardAfterCompaction(t *testing.T) {
	for source, want := range map[string]bool{"compact": true, "resume": true, "clear": true, "startup": false, "": false} {
		card := rulesCard(source)
		if (card != "") != want {
			t.Errorf("%q: card present = %v, want %v", source, card != "", want)
		}
		if want && (!strings.Contains(card, "deconflict claim") || !strings.Contains(card, "negotiate with")) {
			t.Errorf("%q: card lacks the rules: %q", source, card)
		}
	}
}

func dirOf(p string) string { return p[:strings.LastIndex(p, string(os.PathSeparator))] }
