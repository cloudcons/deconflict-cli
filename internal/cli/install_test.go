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

func TestInstallPreservesOtherMCPServers(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".mcp.json")
	mustWrite(t, claude, `{"mcpServers":{"existing":{"command":"existing-server"}}}`)
	if _, err := installClaudeMCP(claude, "/opt/deconflict", false); err != nil {
		t.Fatal(err)
	}
	servers := readBack(t, claude)["mcpServers"].(map[string]any)
	if len(servers) != 2 || servers["existing"].(map[string]any)["command"] != "existing-server" {
		t.Fatalf("Claude MCP servers = %#v", servers)
	}

	codex := filepath.Join(dir, "config.toml")
	mustWrite(t, codex, "[mcp_servers.existing]\ncommand = \"existing-server\"\n")
	if _, err := installCodexMCP(codex, "/opt/deconflict", false); err != nil {
		t.Fatal(err)
	}
	body := read(t, codex)
	for _, want := range []string{"[mcp_servers.existing]", `command = "existing-server"`, "[mcp_servers.deconflict]", `command = "/opt/deconflict"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Codex config lost %q:\n%s", want, body)
		}
	}
}

func TestCodexMCPInstallUpdatesMovedBinaryWithoutDuplicatingSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := installCodexMCP(path, "/old/deconflict", false); err != nil {
		t.Fatal(err)
	}
	if _, err := installCodexMCP(path, "/new/deconflict", false); err != nil {
		t.Fatal(err)
	}
	body := read(t, path)
	if strings.Count(body, "[mcp_servers.deconflict]") != 1 || strings.Contains(body, "/old/") || !strings.Contains(body, "/new/deconflict") {
		t.Fatalf("MCP section was not updated in place:\n%s", body)
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

// Codex trusts a hook command by hash, keyed on where it sits in hooks.json.
// We append ours as a new group, so on a machine that already has a hook for
// the same event ours lands at an index no trust entry covers — Codex runs the
// trusted one and ignores ours. The install said "wrote hooks" over a hook that
// never fired, and an agent edited a file another agent had claimed while the
// warning was being generated correctly and discarded unread.
func TestInstallReportsACodexHookCodexWillNotRun(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, "hooks.json")
	config := filepath.Join(dir, "config.toml")

	// Somebody else's hook at index 0, ours appended at index 1.
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(hooks, `{"hooks":{"PreToolUse":[
	  {"hooks":[{"type":"command","command":"/opt/other/tool watch"}]},
	  {"hooks":[{"type":"command","command":"/usr/local/bin/deconflict hook pre-tool"}]}
	],"SessionStart":[
	  {"hooks":[{"type":"command","command":"/usr/local/bin/deconflict hook session-start"}]}
	]}}`)
	// Only index 0 of PreToolUse is trusted, and SessionStart's index 0 is ours.
	write(config, `[hooks.state."`+hooks+`:pre_tool_use:0:0"]
trusted_hash = "sha256:aaa"

[hooks.state."`+hooks+`:session_start:0:0"]
trusted_hash = "sha256:bbb"
`)

	missing := untrustedCodexHooks(hooks, config)
	if len(missing) != 1 || missing[0] != "PreToolUse" {
		t.Fatalf("untrusted hooks = %v, want just PreToolUse — ours sits at index 1 and nothing trusts it", missing)
	}
}

// A hook that is trusted must not be reported, or the warning becomes noise
// people learn to scroll past.
func TestATrustedCodexHookIsNotReported(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(dir, "hooks.json")
	config := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(hooks, []byte(`{"hooks":{"PreToolUse":[
	  {"hooks":[{"type":"command","command":"/usr/local/bin/deconflict hook pre-tool"}]}
	]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`[hooks.state."`+hooks+`:pre_tool_use:0:0"]
trusted_hash = "sha256:aaa"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if missing := untrustedCodexHooks(hooks, config); len(missing) != 0 {
		t.Errorf("a trusted hook was reported as untrusted: %v", missing)
	}
}
