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
// writes them into configuration somebody else owns without damaging it. Most
// cases below are a real file shape that an earlier version damaged.

// planned runs one edit through a plan and commits it, the way cmdInstall does.
func planned(t *testing.T, uninstall bool, edit func(p *installPlan)) *installPlan {
	t.Helper()
	p := newInstallPlan(uninstall, false)
	edit(p)
	if len(p.refusals) > 0 {
		return p
	}
	if err := p.commit(); err != nil {
		t.Fatal(err)
	}
	return p
}

func install(t *testing.T, edit func(p *installPlan)) *installPlan {
	t.Helper()
	return planned(t, false, edit)
}

// ---------- hooks ----------

func TestInstallPreservesExistingConfig(t *testing.T) {
	settings := filepath.Join(t.TempDir(), ".claude", "settings.json")
	mustWrite(t, settings, `{
  "permissions": {"allow": ["Bash(git status:*)"]},
  "hooks": {"PreToolUse": [
    {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-own-guard.sh"}]}
  ]}
}`)
	install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })

	doc := readBack(t, settings)
	if doc["permissions"] == nil {
		t.Error("unrelated top-level key was dropped")
	}
	if got := commands(doc, "PreToolUse"); len(got) != 2 || got[0] != "my-own-guard.sh" {
		t.Errorf("someone else's hook did not survive: %v", got)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	for i := range 3 {
		p := install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })
		if i > 0 && p.changes[0].action != "already set" {
			t.Errorf("run %d reported %q, want no change", i+1, p.changes[0].action)
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
	settings := filepath.Join(t.TempDir(), "settings.json")
	install(t, func(p *installPlan) { p.hooks(settings, "/old/deconflict", claudeEditMatcher) })
	install(t, func(p *installPlan) { p.hooks(settings, "/new/deconflict", claudeEditMatcher) })

	got := commands(readBack(t, settings), "SessionStart")
	if len(got) != 1 || !strings.HasPrefix(got[0], "/new/") {
		t.Fatalf("want one hook at the new path, got %v", got)
	}
}

// `lefthook hook pre-tool` ends the way ours does. The old suffix match took it
// over: rewrote its command to ours and its matcher to Edit|Write.
func TestInstallLeavesLookalikeHooksAlone(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	mustWrite(t, settings, `{"hooks": {"PreToolUse": [
  {"matcher": "Bash", "hooks": [{"type": "command", "command": "lefthook hook pre-tool"}]}
]}}`)
	install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })

	doc := readBack(t, settings)
	if got := commands(doc, "PreToolUse"); len(got) != 2 || got[0] != "lefthook hook pre-tool" {
		t.Fatalf("lefthook's hook was taken over: %v", got)
	}
	if m := matchers(doc, "PreToolUse"); m[0] != "Bash" {
		t.Fatalf("lefthook's matcher changed to %q", m[0])
	}
}

// Our hook sharing a group with somebody else's: widening the group's matcher
// made their hook fire on Write and NotebookEdit too. It moves out instead.
func TestInstallMovesOurHookOutOfASharedGroup(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	mustWrite(t, settings, `{"hooks": {"PreToolUse": [
  {"matcher": "Edit", "hooks": [
    {"type": "command", "command": "fmt-check"},
    {"type": "command", "command": "deconflict hook pre-tool"}
  ]}
]}}`)
	install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })

	doc := readBack(t, settings)
	m := matchers(doc, "PreToolUse")
	if len(m) != 2 || m[0] != "Edit" || m[1] != claudeEditMatcher {
		t.Fatalf("matchers = %v, want the shared group left at Edit and ours on its own", m)
	}
	if got := commands(doc, "PreToolUse"); len(got) != 2 || got[0] != "fmt-check" {
		t.Fatalf("hooks = %v", got)
	}
}

// Two installs from different places left two session-start hooks, and only the
// first was updated — the stale one kept firing. Flags a user added survive.
func TestInstallCollapsesDuplicatesAndKeepsFlags(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	mustWrite(t, settings, `{"hooks": {"SessionStart": [
  {"hooks": [{"type": "command", "command": "/older/deconflict hook session-start --store http://reg:8080"}]},
  {"hooks": [{"type": "command", "command": "/old/deconflict hook session-start"}]}
]}}`)
	install(t, func(p *installPlan) { p.hooks(settings, "/new/deconflict", claudeEditMatcher) })

	got := commands(readBack(t, settings), "SessionStart")
	if len(got) != 1 || got[0] != "/new/deconflict hook session-start --store http://reg:8080" {
		t.Fatalf("SessionStart hooks = %v", got)
	}
}

func TestInstallQuotesABinaryPathWithSpaces(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	bin := "/Users/me/Application Support/deconflict"
	install(t, func(p *installPlan) { p.hooks(settings, bin, claudeEditMatcher) })
	install(t, func(p *installPlan) { p.hooks(settings, bin, claudeEditMatcher) })

	got := commands(readBack(t, settings), "PreToolUse")
	if len(got) != 1 || got[0] != `"`+bin+`" hook pre-tool` {
		t.Fatalf("PreToolUse = %v", got)
	}
}

// A shape we do not write is refused, not replaced: "PreToolUse" as an object
// used to be swapped for a fresh list, and the hook inside it was lost.
func TestInstallRefusesUnexpectedShapes(t *testing.T) {
	for name, body := range map[string]string{
		"hooks event is an object": `{"hooks": {"PreToolUse": {"keep": "me"}}}`,
		"hooks is a list":          `{"hooks": ["keep-me"]}`,
		"trailing comma":           `{"hooks": {},}`,
		"comment":                  "// mine\n{}",
		"top level is a list":      `[1, 2]`,
		"truncated":                `{"hooks": [ truncated`,
		"duplicate key":            `{"hooks": {}, "hooks": {"x": 1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			settings := filepath.Join(t.TempDir(), "settings.json")
			mustWrite(t, settings, body)
			p := install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })
			if len(p.refusals) != 1 || p.refusals[0].snippet == "" {
				t.Fatalf("want one refusal with a snippet, got %+v", p.refusals)
			}
			if read(t, settings) != body {
				t.Fatal("the file was changed")
			}
		})
	}
}

// ---------- JSON fidelity ----------

// Keys used to come back sorted, big numbers through float64, and & in other
// people's commands as \u0026. Only what the installer adds should differ.
func TestInstallKeepsJSONAsWritten(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	original := `{
    "zeta": 12345678901234567890,
    "alpha": {"cmd": "a && b < c > d"},
    "model": "opus"
}
`
	mustWrite(t, settings, original)
	install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })
	body := read(t, settings)

	for _, want := range []string{"12345678901234567890", `"a && b < c > d"`, `    "zeta"`} {
		if !strings.Contains(body, want) {
			t.Errorf("lost %s:\n%s", want, body)
		}
	}
	if strings.Index(body, `"zeta"`) > strings.Index(body, `"alpha"`) || strings.Index(body, `"model"`) > strings.Index(body, `"hooks"`) {
		t.Errorf("key order changed:\n%s", body)
	}
}

func TestAnUnchangedFileIsNotRewritten(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })
	// Reformat by hand; the next install has nothing to add and must not undo it.
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(read(t, settings))); err != nil {
		t.Fatal(err)
	}
	compact := buf.String()
	mustWrite(t, settings, compact)
	install(t, func(p *installPlan) { p.hooks(settings, "deconflict", claudeEditMatcher) })
	if read(t, settings) != compact {
		t.Fatal("a file with nothing to change was rewritten")
	}
}

// ---------- MCP ----------

func TestInstallPreservesOtherMCPServers(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".mcp.json")
	mustWrite(t, claude, `{"mcpServers":{"existing":{"command":"existing-server"}}}`)
	install(t, func(p *installPlan) { p.jsonMCP(claude, "/opt/deconflict") })
	servers := readBack(t, claude)["mcpServers"].(map[string]any)
	if len(servers) != 2 || servers["existing"].(map[string]any)["command"] != "existing-server" {
		t.Fatalf("Claude MCP servers = %#v", servers)
	}

	codex := filepath.Join(dir, "config.toml")
	mustWrite(t, codex, "[mcp_servers.existing]\ncommand = \"existing-server\"\n")
	install(t, func(p *installPlan) { p.codexMCP(codex, "/opt/deconflict") })
	body := read(t, codex)
	for _, want := range []string{"[mcp_servers.existing]", `command = "existing-server"`, "[mcp_servers.deconflict]", `command = "/opt/deconflict"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Codex config lost %q:\n%s", want, body)
		}
	}
}

func TestInstallRefusesMCPServersThatIsNotAnObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	mustWrite(t, path, `{"mcpServers": [{"name": "mine"}]}`)
	p := install(t, func(p *installPlan) { p.jsonMCP(path, "deconflict") })
	if len(p.refusals) != 1 || !strings.Contains(read(t, path), "mine") {
		t.Fatalf("refusals = %+v, file = %s", p.refusals, read(t, path))
	}
}

// ---------- Codex config.toml ----------

// Each of these is a config.toml an earlier version turned into one Codex
// refuses to load. The result must either be valid with exactly one definition
// of each of our keys, or refused with the file untouched.
func TestCodexConfigEdits(t *testing.T) {
	cases := []struct {
		name, body string
		refuse     bool
		want       []string // substrings of the result
		keep       []string // substrings of the original that must survive
	}{
		{name: "empty", body: "", want: []string{"[features]\ncodex_hooks = true\n", "[mcp_servers.deconflict]\n"}},
		{name: "no trailing newline", body: `model = "o3"`, keep: []string{`model = "o3"` + "\n"}},
		{name: "quoted header", body: "[mcp_servers.\"deconflict\"]\ncommand = \"/old\"\n",
			want: []string{`command = "/opt/deconflict"`}},
		{name: "inline server", body: "[mcp_servers]\ndeconflict = { command = \"/old\" }\n", refuse: true},
		{name: "inline mcp_servers", body: "mcp_servers = { other = { command = \"x\" } }\n", refuse: true},
		{name: "dotted server", body: "mcp_servers.deconflict.command = \"/old\"\n", refuse: true},
		{name: "multi-line args of ours are kept", body: "[mcp_servers.deconflict]\ncommand = \"/opt/deconflict\"\nargs = [\n  \"mcp\",\n  \"--store\", \"http://reg\",\n]\n",
			keep: []string{"\"--store\", \"http://reg\","}},
		{name: "multi-line foreign args are replaced whole", body: "[mcp_servers.deconflict]\ncommand = \"/old\"\nargs = [\n  \"serve\",\n]\n[mcp_servers.other]\ncommand = \"o\"\n",
			want: []string{"args = [\"mcp\"]\n[mcp_servers.other]"}},
		{name: "commented-out header", body: "# [mcp_servers.deconflict]\n[mcp_servers.other]\ncommand = \"o\"\n",
			keep: []string{"# [mcp_servers.deconflict]\n[mcp_servers.other]\ncommand = \"o\"\n"}},
		{name: "command without args, then a table, no newline", body: "[mcp_servers.deconflict]\ncommand = \"/old\"\n[mcp_servers.other]\ncommand = \"o\"",
			want: []string{"args = [\"mcp\"]\n[mcp_servers.other]\ncommand = \"o\""}},
		{name: "features header with a comment", body: "[features] # flags\nweb_search = true\n",
			keep: []string{"[features] # flags\n"}},
		{name: "dotted features at the top", body: "features.web_search = true\n[profiles.x]\nmodel = \"o3\"\n",
			want: []string{"features.codex_hooks = true\n"}},
		{name: "a profile's flag does not count", body: "[profiles.old]\nfeatures.codex_hooks = true\n",
			want: []string{"[features]\ncodex_hooks = true\n"}},
		{name: "multi-line string holding a header", body: "notes = \"\"\"\n[mcp_servers.deconflict]\n[features]\n\"\"\"\n",
			want: []string{"\n[features]\ncodex_hooks = true\n"}},
		{name: "unterminated string", body: "model = \"x\n", refuse: true},
		{name: "broken header", body: "[[[\n", refuse: true},
		{name: "array of mcp tables", body: "[[mcp_servers]]\nname = \"x\"\n", refuse: true},
		{name: "features twice", body: "[features]\na = 1\n[features]\nb = 2\n", refuse: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			mustWrite(t, path, c.body)
			p := install(t, func(p *installPlan) {
				p.codexFeature(path)
				p.codexMCP(path, "/opt/deconflict")
			})
			got := read(t, path)
			if c.refuse {
				if len(p.refusals) == 0 || got != c.body {
					t.Fatalf("want a refusal and the file untouched; refusals=%v file=\n%s", p.refusals, got)
				}
				return
			}
			if len(p.refusals) > 0 {
				t.Fatalf("refused: %+v", p.refusals)
			}
			checkCodexConfig(t, got)
			for _, w := range append(c.want, c.keep...) {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			// And again: a second run changes nothing.
			install(t, func(p *installPlan) {
				p.codexFeature(path)
				p.codexMCP(path, "/opt/deconflict")
			})
			if again := read(t, path); again != got {
				t.Errorf("second run changed the file:\n%s", again)
			}
		})
	}
}

// checkCodexConfig re-reads a result and fails on anything Codex would reject
// that this reader can see: a key or table defined twice, or ours missing.
func checkCodexConfig(t *testing.T, body string) {
	t.Helper()
	d, err := parseTOMLDoc(body)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, body)
	}
	seen := map[string]bool{}
	for _, st := range d.stmts {
		if st.kind == tomlArrayTable {
			continue
		}
		k := strings.Join(st.path, "\x00")
		if st.kind == tomlTable {
			k = "[" + k
		}
		if seen[k] {
			t.Fatalf("%v defined twice:\n%s", st.path, body)
		}
		seen[k] = true
	}
	for _, want := range [][]string{{"features", "codex_hooks"}, {"mcp_servers", "deconflict", "command"}, {"mcp_servers", "deconflict", "args"}} {
		if !seen[strings.Join(want, "\x00")] {
			t.Fatalf("%v missing:\n%s", want, body)
		}
	}
}

// A user who set codex_hooks = false chose that for every hook in hooks.json;
// "already set" hid the fact that ours would never run.
func TestCodexHooksLeftOffIsReportedNotFlipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	mustWrite(t, path, "[features]\ncodex_hooks = false\n")
	p := install(t, func(p *installPlan) { p.codexFeature(path) })
	if read(t, path) != "[features]\ncodex_hooks = false\n" {
		t.Fatal("the user's setting was changed")
	}
	if p.changes[0].action != "left as is" || len(p.notes) != 1 || !strings.Contains(p.notes[0], "will not run") {
		t.Fatalf("changes=%+v notes=%v", p.changes, p.notes)
	}
}

func TestCodexMCPInstallUpdatesMovedBinaryWithoutDuplicatingSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	install(t, func(p *installPlan) { p.codexMCP(path, "/old/deconflict") })
	install(t, func(p *installPlan) { p.codexMCP(path, "/new/deconflict") })
	body := read(t, path)
	if strings.Count(body, "[mcp_servers.deconflict]") != 1 || strings.Contains(body, "/old/") || !strings.Contains(body, "/new/deconflict") {
		t.Fatalf("MCP section was not updated in place:\n%s", body)
	}
}

// ---------- guidance ----------

func TestGuidanceBlockReplacesItselfOnly(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "CLAUDE.md")
	mustWrite(t, doc, "# Project\n\nHand-written notes.\n")
	install(t, func(p *installPlan) { p.guidance(doc) })
	mustWrite(t, doc, read(t, doc)+"\n## Notes added later\n")
	install(t, func(p *installPlan) { p.guidance(doc) })

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

// An editor that strips the final newline left the end marker as the last
// bytes of the file, and the next install panicked with a slice out of range.
func TestGuidanceEndMarkerAtEndOfFile(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "CLAUDE.md")
	mustWrite(t, doc, "# Rules\n"+guidanceStart+"\nold\n"+guidanceEnd)
	p := install(t, func(p *installPlan) { p.guidance(doc) })
	if len(p.refusals) > 0 {
		t.Fatal(p.refusals)
	}
	body := read(t, doc)
	if strings.Contains(body, "\nold\n") || !strings.HasPrefix(body, "# Rules\n"+guidanceStart) {
		t.Fatalf("block not replaced:\n%s", body)
	}
}

// Markers out of order used to copy everything between them again on each run.
func TestGuidanceRefusesMarkersItCannotPair(t *testing.T) {
	for name, body := range map[string]string{
		"out of order": "A\n" + guidanceEnd + "\nUSER TEXT\n" + guidanceStart + "\nB\n",
		"repeated":     guidanceStart + "\nx\n" + guidanceEnd + "\n" + guidanceStart + "\ny\n" + guidanceEnd + "\n",
		"start only":   "A\n" + guidanceStart + "\nB\n",
	} {
		t.Run(name, func(t *testing.T) {
			doc := filepath.Join(t.TempDir(), "AGENTS.md")
			mustWrite(t, doc, body)
			p := install(t, func(p *installPlan) { p.guidance(doc) })
			if len(p.refusals) != 1 || read(t, doc) != body {
				t.Fatalf("want a refusal and the file untouched, got %+v", p.refusals)
			}
		})
	}
}

// ---------- skill ----------

func TestSkillBackupOnlyWhenEditedByHand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "deconflict", "SKILL.md")
	install(t, func(p *installPlan) { p.skill(path) })

	// A pristine copy from an older text is replaced without a backup.
	old := strings.Replace(read(t, path), "revision="+skillRevision(), "revision=000000000000", 1)
	mustWrite(t, path, old)
	if !skillHandEdited(old) {
		t.Fatal("test setup: a copy whose stamp does not match its text counts as edited")
	}
	if skillHandEdited(renderSkill()) {
		t.Fatal("a freshly written skill counts as edited")
	}

	mustWrite(t, path, renderSkill()+"\nmy own notes\n")
	p := install(t, func(p *installPlan) { p.skill(path) })
	if len(p.backups) != 1 || !strings.Contains(read(t, p.backups[0]), "my own notes") {
		t.Fatalf("the hand-edited skill was not backed up: %v", p.backups)
	}
}

// ---------- the whole command ----------

func withHome(t *testing.T) (home, repo string) {
	t.Helper()
	home, repo = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home, repo
}

func runInstall(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	var err error
	if args[0] == "uninstall" {
		err = cmdUninstall(args[1:], &out)
	} else {
		err = cmdInstall(args, &out)
	}
	return out.String(), err
}

func TestInstallCommandReportsWhatItDid(t *testing.T) {
	_, repo := withHome(t)
	out, err := runInstall(t, "--dir", repo, "--agent", "claude", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"settings.json", "SKILL.md", "CLAUDE.md", "dry run"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary never mentions %q:\n%s", want, out)
		}
	}
	if entries, _ := os.ReadDir(repo); len(entries) != 0 {
		t.Errorf("dry run wrote %v", entries)
	}
}

func TestInstallRejectsAnUnknownScope(t *testing.T) {
	_, repo := withHome(t)
	if _, err := runInstall(t, "--dir", repo, "--agent", "claude", "--scope", "usr"); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(repo); len(entries) != 0 {
		t.Errorf("wrote %v", entries)
	}
}

// A refusal anywhere means nothing is written anywhere. The old installer had
// rewritten settings.json before it found .mcp.json unreadable, and left the
// repository half-installed.
func TestInstallIsAllOrNothing(t *testing.T) {
	_, repo := withHome(t)
	settings := filepath.Join(repo, ".claude", "settings.json")
	mustWrite(t, settings, `{"model": "opus"}`)
	mustWrite(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers": {,}}`)

	out, err := runInstall(t, "--dir", repo, "--agent", "claude")
	if err == nil {
		t.Fatal("want an error")
	}
	if read(t, settings) != `{"model": "opus"}` {
		t.Error("settings.json was written although the install was refused")
	}
	if _, err := os.Stat(filepath.Join(repo, "CLAUDE.md")); err == nil {
		t.Error("CLAUDE.md was written although the install was refused")
	}
	if !strings.Contains(out, `"deconflict": {"type": "stdio"`) {
		t.Errorf("the refusal did not say what to add:\n%s", out)
	}
}

func TestInstallBacksUpAndFollowsSymlinks(t *testing.T) {
	_, repo := withHome(t)
	real := filepath.Join(t.TempDir(), "dotfiles", "settings.json")
	mustWrite(t, real, `{"model": "opus"}`)
	link := filepath.Join(repo, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here")
	}

	out, err := runInstall(t, "--dir", repo, "--agent", "claude", "--doc=false", "--skill=false")
	if err != nil {
		t.Fatal(err, out)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a file")
	}
	if !strings.Contains(read(t, real), "hook pre-tool") {
		t.Fatal("the linked file was not updated")
	}
	if read(t, link+".deconflict.bak") != `{"model": "opus"}` || !strings.Contains(out, ".deconflict.bak") {
		t.Fatalf("no backup, or not mentioned:\n%s", out)
	}
}

// --scope user used to write .mcp.json and CLAUDE.md into whichever directory
// it was run from, and never gave Claude Code a user-level MCP server.
func TestUserScopeWritesNothingIntoTheRepository(t *testing.T) {
	home, repo := withHome(t)
	claudeJSON := filepath.Join(home, ".claude.json")
	mustWrite(t, claudeJSON, `{"numStartups": 7, "projects": {"/x": {"mcpServers": {}}}, "mcpServers": {"other": {"command": "o"}}}`)

	if out, err := runInstall(t, "--dir", repo, "--agent", "all", "--scope", "user"); err != nil {
		t.Fatal(err, out)
	}
	if entries, _ := os.ReadDir(repo); len(entries) != 0 {
		t.Fatalf("user scope wrote into the repository: %v", entries)
	}
	doc := readBack(t, claudeJSON)
	servers := doc["mcpServers"].(map[string]any)
	if servers["other"] == nil || servers["deconflict"] == nil || doc["numStartups"] != float64(7) {
		t.Fatalf("~/.claude.json = %v", doc)
	}
	for _, p := range []string{
		".claude/settings.json", ".claude/CLAUDE.md", ".claude/skills/deconflict/SKILL.md",
		".codex/hooks.json", ".codex/config.toml", ".codex/AGENTS.md", ".agents/skills/deconflict/SKILL.md",
	} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Errorf("missing ~/%s", p)
		}
	}
}

// Uninstall takes out exactly what install put in.
func TestUninstallRemovesOnlyOurs(t *testing.T) {
	home, repo := withHome(t)
	settings := filepath.Join(repo, ".claude", "settings.json")
	mustWrite(t, settings, `{"model": "opus", "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh"}]}]}}`)
	mustWrite(t, filepath.Join(repo, ".mcp.json"), `{"mcpServers": {"other": {"command": "o"}}}`)
	mustWrite(t, filepath.Join(repo, "CLAUDE.md"), "# Mine\n")
	cfg := filepath.Join(home, ".codex", "config.toml")
	mustWrite(t, cfg, "# my settings\nmodel = \"o3\"\n\n[mcp_servers.other]\ncommand = \"o\"\n")
	otherSkill := filepath.Join(repo, ".claude", "skills", "other", "SKILL.md")
	mustWrite(t, otherSkill, "mine")

	if out, err := runInstall(t, "--dir", repo, "--agent", "all"); err != nil {
		t.Fatal(err, out)
	}
	out, err := runInstall(t, "uninstall", "--dir", repo, "--agent", "all")
	if err != nil {
		t.Fatal(err, out)
	}

	doc := readBack(t, settings)
	if got := commands(doc, "PreToolUse"); len(got) != 1 || got[0] != "guard.sh" || commands(doc, "SessionStart") != nil {
		t.Errorf("hooks after uninstall: %v", doc["hooks"])
	}
	if doc["model"] != "opus" {
		t.Error("lost an unrelated key")
	}
	if servers := readBack(t, filepath.Join(repo, ".mcp.json"))["mcpServers"].(map[string]any); len(servers) != 1 {
		t.Errorf("mcpServers after uninstall: %v", servers)
	}
	if got := read(t, filepath.Join(repo, "CLAUDE.md")); got != "# Mine\n" {
		t.Errorf("CLAUDE.md after uninstall: %q", got)
	}
	if got := read(t, cfg); strings.Contains(got, "deconflict") || !strings.Contains(got, "[mcp_servers.other]") || !strings.Contains(got, "codex_hooks = true") {
		t.Errorf("config.toml after uninstall (codex_hooks stays on for other hooks):\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "skills", "deconflict")); !os.IsNotExist(err) {
		t.Error("our skill directory is still there")
	}
	if read(t, otherSkill) != "mine" {
		t.Error("another skill was touched")
	}
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("an AGENTS.md holding only our block was left behind")
	}
}

// ---------- Codex trust ----------

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
	mustWrite(t, hooks, `{"hooks":{"PreToolUse":[
	  {"hooks":[{"type":"command","command":"/opt/other/tool watch"}]},
	  {"hooks":[{"type":"command","command":"/usr/local/bin/deconflict hook pre-tool"}]}
	],"SessionStart":[
	  {"hooks":[{"type":"command","command":"/usr/local/bin/deconflict hook session-start"}]}
	]}}`)
	// Only index 0 of PreToolUse is trusted, and SessionStart's index 0 is ours.
	mustWrite(t, config, `[hooks.state."`+hooks+`:pre_tool_use:0:0"]
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
	mustWrite(t, hooks, `{"hooks":{"PreToolUse":[
	  {"hooks":[{"type":"command","command":"/usr/local/bin/deconflict hook pre-tool"}]}
	]}}`)
	mustWrite(t, config, `[hooks.state."`+hooks+`:pre_tool_use:0:0"]
trusted_hash = "sha256:aaa"
`)
	if missing := untrustedCodexHooks(hooks, config); len(missing) != 0 {
		t.Errorf("a trusted hook was reported as untrusted: %v", missing)
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

func groupsOf(doc map[string]any, event string) []map[string]any {
	hooks, _ := doc["hooks"].(map[string]any)
	raw, _ := hooks[event].([]any)
	var out []map[string]any
	for _, g := range raw {
		if group, ok := g.(map[string]any); ok {
			out = append(out, group)
		}
	}
	return out
}

func commands(doc map[string]any, event string) []string {
	var out []string
	for _, group := range groupsOf(doc, event) {
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

func matchers(doc map[string]any, event string) []string {
	var out []string
	for _, group := range groupsOf(doc, event) {
		m, _ := group["matcher"].(string)
		out = append(out, m)
	}
	return out
}
