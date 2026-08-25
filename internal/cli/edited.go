package cli

import (
	"path/filepath"
	"strings"
)

// Which files is a tool call about to write?
//
// Claude Code and Codex both fire a pre-tool hook, but they describe an edit
// differently: Claude Code names the tool `Edit`/`Write` and puts the target in
// `tool_input.file_path`; Codex names it `apply_patch` and puts a whole patch
// body in `tool_input.command`, which may cover several files at once.
//
// Deliberately NOT handled: shell commands. Codex fires PreToolUse for `Bash`
// too, and a `sed -i` in there really can edit a claimed file — but recovering
// the target means pattern-matching command text, which is guesswork that
// produces false warnings on every `grep -r src/auth/`. A warning people learn
// to dismiss is worse than no warning. Claude Code has the same blind spot for
// bash edits, so this is parity rather than a regression, and `claims status`
// catches it afterwards by diffing against the claim.

// writeTools are the tool names that mean "about to modify a file".
// `apply_patch` is Codex; the rest are Claude Code.
var writeTools = map[string]bool{
	"Edit": true, "Write": true, "NotebookEdit": true, "MultiEdit": true,
	"apply_patch": true,
}

// editedPaths returns the files a tool call is about to write, as given.
func editedPaths(toolName string, toolInput map[string]any) []string {
	if !writeTools[toolName] {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	// Claude Code, and any tool that names its target directly.
	for _, k := range []string{"file_path", "notebook_path", "path", "file"} {
		if v, ok := toolInput[k].(string); ok {
			add(v)
		}
	}

	// Codex apply_patch: the target files are named inside the patch body,
	// which arrives as `command` (a string, or an argv array).
	add2 := func(s string) {
		for _, p := range applyPatchTargets(s) {
			add(p)
		}
	}
	switch v := toolInput["command"].(type) {
	case string:
		add2(v)
	case []any:
		var parts []string
		for _, e := range v {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
		add2(strings.Join(parts, "\n"))
	}
	return out
}

// applyPatchTargets pulls file paths out of an apply_patch body. The format
// names every file it touches on its own line:
//
//	*** Begin Patch
//	*** Update File: src/auth/token.go
//	*** Add File: src/auth/rotate.go
//	*** Delete File: src/auth/legacy.go
//	*** End Patch
func applyPatchTargets(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "***") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "***"))
		for _, verb := range []string{"Update File:", "Add File:", "Delete File:", "Move to:"} {
			if strings.HasPrefix(rest, verb) {
				if p := strings.TrimSpace(strings.TrimPrefix(rest, verb)); p != "" {
					out = append(out, p)
				}
				break
			}
		}
	}
	return out
}

// relToRepo makes a path repo-relative when it is absolute and inside the
// repository; claims are always written repo-relative.
func relToRepo(root, p string) string {
	if root == "" || !filepath.IsAbs(p) {
		return strings.TrimPrefix(p, "./")
	}
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	// Both sides resolved before giving up, because one of them having gone
	// through a symlink is not a different file.
	//
	// git reports the worktree root as its real path, and an editor hands over
	// the path the user typed. On macOS /tmp is a link to /private/tmp, so a
	// repository under it produced a root and a target that share no prefix,
	// Rel returned a "../.." path, and the absolute path fell through to be
	// matched against globs like "src/**" — which it never matches. The effect
	// was not a wrong warning but no warning at all: every claim silently
	// stopped covering every file, for anybody whose checkout sits under a
	// linked path.
	// The directory is resolved rather than the file, because the file is
	// routinely one that does not exist yet — this runs before a write, and a
	// write is often a creation. EvalSymlinks fails on a path that is not
	// there, which would have left new files unmatched by any claim.
	realRoot, rootErr := filepath.EvalSymlinks(root)
	realDir, dirErr := filepath.EvalSymlinks(filepath.Dir(p))
	if rootErr == nil && dirErr == nil {
		if r, err := filepath.Rel(realRoot, filepath.Join(realDir, filepath.Base(p))); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return p
}
