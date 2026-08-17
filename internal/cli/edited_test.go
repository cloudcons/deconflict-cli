package cli

import (
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
