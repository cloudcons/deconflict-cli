package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The first changed file used to come back with its leading byte removed.
//
// `git status --porcelain=v1` emits "XY <path>", and run trims the whole
// output — which strips the leading space of a first line that is unstaged
// only (" M path"). A fixed three-byte slice then cut one byte into the
// filename, so `deconflict status` reported "nternal/auth/login.go" as a file
// changed outside a claim that plainly covered internal/auth/**. Only the
// first such file was affected, which is why it read as an off-by-one rather
// than as a broken command.
func TestChangedPathsKeepsTheFirstUnstagedFileIntact(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q")
	git("config", "user.email", "t@t.test")
	git("config", "user.name", "t")
	write("alpha.go", "package a\n")
	write("beta.go", "package b\n")
	git("add", "-A")
	git("commit", "-qm", "initial")

	// Modified and not staged, so each porcelain line begins with a space and
	// the first one loses it to the trim.
	write("alpha.go", "package a // changed\n")
	write("beta.go", "package b // changed\n")

	got := ChangedPaths(dir, "")
	want := map[string]bool{"alpha.go": true, "beta.go": true}
	for _, p := range got {
		if !want[p] {
			t.Errorf("ChangedPaths returned %q, which is not a file that changed", p)
			continue
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("ChangedPaths never reported %q; got %q", p, got)
	}
}
