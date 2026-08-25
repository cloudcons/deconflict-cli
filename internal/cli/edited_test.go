package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEditedPaths(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: src/auth/token.go
@@ func Refresh() {
-	old
+	new
*** Add File: src/auth/rotate.go
+package auth
*** Delete File: src/auth/legacy.go
*** End Patch`

	cases := []struct {
		name string
		tool string
		in   map[string]any
		want []string
	}{
		{
			name: "claude code edit",
			tool: "Edit",
			in:   map[string]any{"file_path": "/repo/src/auth/token.go"},
			want: []string{"/repo/src/auth/token.go"},
		},
		{
			name: "codex apply_patch, command as a string",
			tool: "apply_patch",
			in:   map[string]any{"command": patch},
			want: []string{"src/auth/token.go", "src/auth/rotate.go", "src/auth/legacy.go"},
		},
		{
			name: "codex apply_patch, command as an argv array",
			tool: "apply_patch",
			in:   map[string]any{"command": []any{"apply_patch", patch}},
			want: []string{"src/auth/token.go", "src/auth/rotate.go", "src/auth/legacy.go"},
		},
		{
			// A shell command really can edit a claimed file, but recovering the
			// target means guessing at command text. Silence beats a warning
			// people learn to dismiss.
			name: "bash is deliberately out of scope",
			tool: "Bash",
			in:   map[string]any{"command": []any{"bash", "-lc", "sed -i s/a/b/ src/auth/token.go"}},
			want: nil,
		},
		{
			name: "read-only tools never warn",
			tool: "Read",
			in:   map[string]any{"file_path": "/repo/src/auth/token.go"},
			want: nil,
		},
		{
			name: "patch with no file markers yields nothing",
			tool: "apply_patch",
			in:   map[string]any{"command": "*** Begin Patch\n*** End Patch"},
			want: nil,
		},
	}
	for _, c := range cases {
		got := editedPaths(c.tool, c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: editedPaths(%q) = %#v, want %#v", c.name, c.tool, got, c.want)
		}
	}
}

func TestEditedPathsDeduplicates(t *testing.T) {
	got := editedPaths("apply_patch", map[string]any{
		"command": "*** Update File: a.go\n*** Update File: a.go\n*** Add File: b.go",
	})
	if !reflect.DeepEqual(got, []string{"a.go", "b.go"}) {
		t.Errorf("got %#v", got)
	}
}

func TestRelToRepo(t *testing.T) {
	if got := relToRepo("/repo", "/repo/src/auth/token.go"); got != "src/auth/token.go" {
		t.Errorf("absolute inside repo: %q", got)
	}
	if got := relToRepo("/repo", "src/auth/token.go"); got != "src/auth/token.go" {
		t.Errorf("already relative: %q", got)
	}
	if got := relToRepo("/repo", "./src/auth/token.go"); got != "src/auth/token.go" {
		t.Errorf("dot-slash prefix survived: %q", got)
	}
	// Outside the repo: left alone rather than turned into ../.. noise.
	if got := relToRepo("/repo", "/etc/passwd"); got != "/etc/passwd" {
		t.Errorf("outside repo: %q", got)
	}
}

// git reports the worktree root as its real path and an editor hands over the
// path the user typed, so one of them having gone through a symlink used to
// mean no claim covered any file. Rel returned a "../.." path, the absolute
// path fell through, and matching it against a glob like "src/**" failed every
// time — so the pre-write warning stopped firing entirely for anybody whose
// checkout sits under a linked path. On macOS that is anything under /tmp.
func TestRelToRepoLooksThroughSymlinks(t *testing.T) {
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The root as git reports it, the path as an editor supplies it.
	// a.go deliberately does not exist: this runs before the write, and the
	// write is frequently the thing that creates the file.
	if got := relToRepo(real, filepath.Join(link, "src", "a.go")); got != "src/a.go" {
		t.Errorf("relToRepo(real, linked) = %q, want src/a.go", got)
	}
	// And the other way round, since either side may be the resolved one.
	if got := relToRepo(link, filepath.Join(real, "src", "a.go")); got != "src/a.go" {
		t.Errorf("relToRepo(linked, real) = %q, want src/a.go", got)
	}
	// A path genuinely outside the repository is still reported as it came.
	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	if got := relToRepo(real, outside); got != outside {
		t.Errorf("relToRepo of an unrelated path = %q, want it unchanged", got)
	}
}
