package cli

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// Every session on the superlinked VM ran as the same Unix user, so every one
// of them was the agent claude@superlinked: none could settle another's
// question, and none heard another's watercooler posts. Sessions in different
// worktrees, or of different runtimes, must be different agents.
func TestAgentIDSeparatesWorktreesAndRuntimes(t *testing.T) {
	for _, k := range []string{"DECONFLICT_AGENT", "DECONFLICT_RUNTIME", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SSE_PORT", "CODEX_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("USER", "claude")
	base := t.TempDir()
	repo := filepath.Join(base, "app")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(base, "init", "-q", "app")
	git(repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	other := filepath.Join(base, "fix-login")
	git(repo, "worktree", "add", "-q", other)

	idIn := func(dir, runtime string) string {
		t.Chdir(dir)
		t.Setenv("DECONFLICT_RUNTIME", runtime)
		return agentID()
	}
	main := idIn(repo, "claude-code")
	if want := "/app/claude"; main[len(main)-len(want):] != want {
		t.Fatalf("id in the main checkout = %q, want it to end in %q", main, want)
	}
	if wt := idIn(other, "claude-code"); wt == main {
		t.Fatalf("two worktrees share the agent id %q", wt)
	}
	if codex := idIn(repo, "codex"); codex == main {
		t.Fatalf("Claude Code and Codex in one worktree share the agent id %q", codex)
	}
	if again := idIn(repo, "claude-code"); again != main {
		t.Fatalf("the same worktree and runtime gave %q, then %q", main, again)
	}

	t.Setenv("DECONFLICT_AGENT", "bo/claude")
	if got := agentID(); got != "bo/claude" {
		t.Fatalf("DECONFLICT_AGENT was ignored: got %q", got)
	}
}
