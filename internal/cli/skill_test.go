package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stamp sits after the front matter, so the front matter still parses and
// still opens the file.
func TestRenderedSkillIsStampedAfterItsFrontMatter(t *testing.T) {
	body := renderSkill()
	if !strings.HasPrefix(body, "---\nname: deconflict\n") {
		t.Fatalf("front matter no longer opens the skill:\n%.200s", body)
	}
	fm := strings.Index(body[4:], "\n---\n") + 4 + len("\n---\n")
	if !strings.HasPrefix(body[fm:], "<!-- deconflict-skill version=") {
		t.Fatalf("stamp is not directly after the front matter:\n%.400s", body)
	}
	if why, stale := staleSkill(body); stale {
		t.Fatalf("a freshly rendered skill reads as stale: %s", why)
	}
}

func TestStaleSkill(t *testing.T) {
	defer func(v string) { Version = v }(Version)
	Version = "v0.1.7"
	for _, c := range []struct {
		name, body string
		stale      bool
	}{
		{"never stamped", skillMarkdown, true},
		{"older text", "<!-- deconflict-skill version=v0.1.6 revision=000000000000 -->", true},
		{"unstamped build wrote it", "<!-- deconflict-skill version=dev revision=000000000000 -->", true},
		// A newer client wrote it: refreshing from this one would downgrade it.
		{"newer client wrote it", "<!-- deconflict-skill version=v0.2.0 revision=000000000000 -->", false},
		{"same text, other version", "<!-- deconflict-skill version=v0.1.2 revision=" + skillRevision() + " -->", false},
	} {
		if _, stale := staleSkill(c.body); stale != c.stale {
			t.Errorf("%s: stale = %v, want %v", c.name, stale, c.stale)
		}
	}
}

// A machine with an old copy is told, once per copy, with the command that
// refreshes that copy; a machine whose copies are current hears nothing.
func TestSessionStartFlagsAnOutOfDateSkill(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	old := filepath.Join(home, ".agents", "skills", "deconflict", "SKILL.md")
	current := filepath.Join(repo, ".claude", "skills", "deconflict", "SKILL.md")
	mustWrite(t, old, skillMarkdown)
	mustWrite(t, current, renderSkill())

	got := skillContext(repo)
	if !strings.Contains(got, old) || !strings.Contains(got, "deconflict install --agent codex --scope user") {
		t.Fatalf("the out-of-date copy was not flagged with its command:\n%s", got)
	}
	if strings.Contains(got, current) {
		t.Fatalf("a current copy was flagged:\n%s", got)
	}
	if err := os.WriteFile(old, []byte(renderSkill()), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := skillContext(repo); got != "" {
		t.Fatalf("every copy is current, and still:\n%s", got)
	}
}

func TestCodexInstallWritesTheSkillWhereCodexReadsIt(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	var out bytes.Buffer
	if err := cmdInstall([]string{"--dir", repo, "--agent", "codex"}, &out); err != nil {
		t.Fatal(err)
	}
	body := read(t, filepath.Join(repo, ".agents", "skills", "deconflict", "SKILL.md"))
	if body != renderSkill() {
		t.Fatal("the Codex skill is not the rendered skill")
	}
	out.Reset()
	if err := cmdInstall([]string{"--dir", repo, "--agent", "codex", "--scope", "user"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "deconflict", "SKILL.md")); err != nil {
		t.Fatalf("--scope user did not install into the home directory: %v", err)
	}
}
