package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The scenario that asked for this: one agent calls a library, another
// refactors it. Neither edits the other's files, and both are told.
func TestClaimWithUsesWarnsBothSides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DECONFLICT_FILE", filepath.Join(dir, "claims.jsonl"))
	t.Setenv("DECONFLICT_STORE", "")
	t.Setenv("XDG_STATE_HOME", dir)
	// Two worktrees of one repository, as the two agents would have.
	t.Setenv("DECONFLICT_REPO", "github.com/acme/app")
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	caller, refactor := filepath.Join(dir, "caller"), filepath.Join(dir, "refactor")
	for _, d := range []string{caller, refactor} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		// Each its own git directory, so each records its own current claim.
		if out, err := exec.Command("git", "init", "-q", d).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}

	os.Chdir(caller)
	t.Setenv("DECONFLICT_AGENT", "bo/claude")
	var out bytes.Buffer
	if err := cmdClaim([]string{"--paths", "app/checkout", "--uses", "lib/parser", "--what", "wire the parser into checkout"}, &out); err != nil {
		t.Fatalf("caller claim: %v\n%s", err, out.String())
	}

	os.Chdir(refactor)
	t.Setenv("DECONFLICT_AGENT", "cy/codex")
	out.Reset()
	err := cmdClaim([]string{"--paths", "lib/parser/lexer.go", "--what", "split the lexer", "--interface", "Lex() returns tokens and an error"}, &out)
	if n, ok := Code(err); !ok || n != 3 {
		t.Fatalf("refactor over a used path returned %v, want exit 3\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "use what you are changing") || !strings.Contains(out.String(), "bo/claude") {
		t.Errorf("the refactoring agent was not told who uses it:\n%s", out.String())
	}

	// And the caller hears it at its next session start.
	os.Chdir(caller)
	t.Setenv("DECONFLICT_AGENT", "bo/claude")
	got := dependencyContext("", caller)
	if !strings.Contains(got, "changing what you use") || !strings.Contains(got, "Lex() returns tokens and an error") {
		t.Errorf("the caller's session was not told about the refactor:\n%s", got)
	}
}
