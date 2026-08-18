package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What matters about the installer is not that it writes files — it is that it
// writes them into configuration somebody else owns without damaging it.

func TestInstallPreservesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.json")
	mustWrite(t, settings, `{
	  "permissions": {"allow": ["Bash(git status:*)"]},
	  "hooks": {"PreToolUse": [
	    {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-own-guard.sh"}]}
	  ]}
	}`)

	if _, err := installClaudeHooks(settings, "deconflict", false); err != nil {
		t.Fatal(err)
	}

	doc := readBack(t, settings)
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		t.Error("unrelated top-level key was dropped")
	}
	if got := commands(doc, "PreToolUse"); len(got) != 2 || got[0] != "my-own-guard.sh" {
		t.Errorf("someone else's hook did not survive: %v", got)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	for i := range 3 {
		c, err := installClaudeHooks(settings, "deconflict", false)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && c.action != "already set" {
			t.Errorf("run %d reported %q, want no change", i+1, c.action)
		}
	}
	if got := commands(readBack(t, settings), "PreToolUse"); len(got) != 1 {
		t.Errorf("hook was appended again on re-install: %v", got)
	}
}

// A binary that moved must be rewritten in place. The failure this guards is
// silent: a second hook pointing at a path that no longer exists still fires,
// and the agent sees an error where it used to see claims.
func TestInstallRewritesMovedBinary(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	if _, err := installClaudeHooks(settings, "/old/deconflict", false); err != nil {
		t.Fatal(err)
	}
	if _, err := installClaudeHooks(settings, "/new/deconflict", false); err != nil {
		t.Fatal(err)
	}

	got := commands(readBack(t, settings), "SessionStart")
	if len(got) != 1 {
		t.Fatalf("want one hook after the move, got %v", got)
	}
	if !strings.HasPrefix(got[0], "/new/") {
		t.Errorf("stale path kept: %q", got[0])
	}
}

// Refusing is correct: a file we cannot parse is a file we do not understand
// well enough to add to, and overwriting it would lose someone's work.
func TestInstallRefusesUnparseableSettings(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	mustWrite(t, settings, `{"hooks": [ truncated`)

	if _, err := installClaudeHooks(settings, "deconflict", false); err == nil {
		t.Fatal("want an error, got none")
	}
	if b, _ := os.ReadFile(settings); !strings.Contains(string(b), "truncated") {
		t.Error("the unreadable file was overwritten anyway")
	}
}

func TestEnableCodexHooksUsesExistingSection(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	mustWrite(t, cfg, "model = \"gpt-5\"\n\n[features]\nweb_search = true\n")

	if _, err := enableCodexHooks(cfg, false); err != nil {
		t.Fatal(err)
	}
	body := read(t, cfg)
	if strings.Count(body, "[features]") != 1 {
		t.Errorf("duplicated the section, which is a TOML error:\n%s", body)
	}
	for _, want := range []string{"codex_hooks = true", "web_search = true", `model = "gpt-5"`} {
		if !strings.Contains(body, want) {
			t.Errorf("lost %q:\n%s", want, body)
		}
	}
	// And a second run must not add it twice.
	if _, err := enableCodexHooks(cfg, false); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(read(t, cfg), "codex_hooks"); n != 1 {
		t.Errorf("codex_hooks appears %d times", n)
	}
}

func TestGuidanceBlockReplacesItselfOnly(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "CLAUDE.md")
	mustWrite(t, doc, "# Project\n\nHand-written notes.\n")

	if _, err := installGuidance(doc, false); err != nil {
		t.Fatal(err)
	}
	// Simulate an edit after ours, then re-install.
	appended := read(t, doc) + "\n## Notes added later\n"
	mustWrite(t, doc, appended)
	if _, err := installGuidance(doc, false); err != nil {
		t.Fatal(err)
	}

	body := read(t, doc)
	if n := strings.Count(body, guidanceStart); n != 1 {
		t.Errorf("block appears %d times", n)
	}
	for _, want := range []string{"Hand-written notes.", "## Notes added later"} {
		if !strings.Contains(body, want) {
			t.Errorf("clobbered %q:\n%s", want, body)
		}
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	if _, err := installClaudeHooks(settings, "deconflict", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Error("dry run created the file")
	}
}

func TestInstallCommandReportsWhatItDid(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := cmdInstall([]string{"--dir", dir, "--agent", "claude", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"settings.json", "SKILL.md", "CLAUDE.md", "dry run"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary never mentions %q:\n%s", want, out.String())
		}
	}
}

// ---------- helpers ----------

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readBack(t *testing.T, path string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func commands(doc map[string]any, event string) []string {
	hooks, _ := doc["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	var out []string
	for _, g := range groups {
		group, _ := g.(map[string]any)
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			if c, ok := entry["command"].(string); ok {
				out = append(out, c)
			}
		}
	}
	return out
}
