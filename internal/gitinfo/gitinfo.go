// Package gitinfo derives facts from the working tree instead of asking the
// agent for them. Everything an agent self-reports can be wrong or stale;
// everything git knows is true by construction.
package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func run(dir string, args ...string) (string, error) {
	out, err := runRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// runRaw is run without the trim, for the one caller whose output is
// column-aligned. Trimming is right for every other git command here — they
// return a single value with a trailing newline — and wrong for porcelain
// status, where a leading space is data.
func runRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// Root returns the repository root for dir, or "" if it is not a work tree.
func Root(dir string) string {
	// --show-toplevel resolves the worktree root, which is what we want: two
	// sessions in two worktrees of one repo must share a claim namespace.
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return out
}

// Branch returns the current branch name, or "" when detached.
func Branch(dir string) string {
	out, err := run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || out == "HEAD" {
		return ""
	}
	return out
}

var scpLike = regexp.MustCompile(`^[\w.+-]+@([\w.-]+):(.+)$`)

// Repo returns a stable identity for the repository, shared across every clone
// and worktree: the normalized origin URL, e.g. "github.com/acme/api". Falls
// back to the directory name so the tool still works outside a remote-backed
// repo.
func Repo(dir string) string {
	if v := os.Getenv("DECONFLICT_REPO"); v != "" {
		return v
	}
	root := Root(dir)
	if root == "" {
		root = dir
	}
	for _, remote := range []string{"upstream", "origin"} {
		if u, err := run(root, "remote", "get-url", remote); err == nil && u != "" {
			return normalizeRemote(u)
		}
	}
	return filepath.Base(root)
}

func normalizeRemote(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(u, ".git")
	if m := scpLike.FindStringSubmatch(u); m != nil {
		return m[1] + "/" + strings.Trim(m[2], "/")
	}
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(u, p) {
			u = strings.TrimPrefix(u, p)
			if i := strings.Index(u, "@"); i >= 0 && i < strings.Index(u+"/", "/") {
				u = u[i+1:]
			}
			return strings.Trim(u, "/")
		}
	}
	return strings.Trim(u, "/")
}

// ChangedPaths lists files this branch touches relative to base, plus anything
// uncommitted. This is how progress is derived rather than self-reported.
func ChangedPaths(dir, base string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		for _, l := range strings.Split(s, "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	if base != "" {
		if v, err := run(dir, "diff", "--name-only", base+"...HEAD"); err == nil {
			add(v)
		}
	}
	// Porcelain v1 is "XY <path>": two status columns, a space, then the path.
	// Read raw, because trimming the whole output eats the leading space of a
	// first line that is unstaged-only (" M path") and shifts this slice one
	// byte into the filename — which reported a real path as a file changed
	// outside the claim that covered it, for the first changed file and no
	// other.
	if v, err := runRaw(dir, "status", "--porcelain=v1", "--no-renames"); err == nil {
		for _, l := range strings.Split(v, "\n") {
			if len(l) > 3 {
				add(l[3:])
			}
		}
	}
	return out
}

// Resolves reports whether a revision still exists in this worktree. A claim
// records the commit it was made at, and that commit can be gone by the time
// anybody asks — rebased away, or made in a checkout this one has never seen.
func Resolves(dir, rev string) bool {
	out, err := run(dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil && out != ""
}

// BaseRef resolves the published base branch — the thing a claim is measured
// against. Prefers an explicit override, then upstream/, then origin/.
func BaseRef(dir string) string {
	if v := os.Getenv("DECONFLICT_BASE"); v != "" {
		return v
	}
	for _, c := range []string{"upstream/main", "origin/main", "upstream/master", "origin/master"} {
		if _, err := run(dir, "rev-parse", "--verify", "--quiet", c); err == nil {
			return c
		}
	}
	return ""
}

// HeadSHA returns the current commit, recorded on a claim so that "the branch
// has not moved" is distinguishable later.
func HeadSHA(dir string) string {
	out, err := run(dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// Landed reports whether a claim's work has reached the published base. This is
// the close signal, and it is derived rather than reported: "done" self-reported
// is the most common lie in an agent system, and landed-in-base is a fact git
// already holds.
//
// headAtClaim is the branch tip when the claim was made, and it is what makes
// the answer correct. Commit count cannot distinguish the two states that both
// read as zero-ahead: a branch nobody has committed to yet, and a branch whose
// commits have all landed. Only "has the tip moved since the claim" separates
// them, and releasing an unstarted claim would silently delete a live warning
// for an agent that is mid-task.
func Landed(dir, branch, base, headAtClaim string) bool {
	if branch == "" || base == "" {
		return false
	}
	tip, err := run(dir, "rev-parse", "--verify", "--quiet", branch)
	if err != nil || tip == "" {
		// The branch is gone — deleted after a squash merge, typically.
		// Absent is as closed as landed for our purposes.
		return true
	}
	if headAtClaim != "" && tip == headAtClaim {
		return false // nothing committed since the claim: not started, not landed
	}
	out, err := run(dir, "branch", "--merged", base, "--format=%(refname:short)")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == branch {
			return true
		}
	}
	return false
}

// AheadBy counts commits on branch not in base — the cheapest progress signal
// there is.
func AheadBy(dir, branch, base string) int {
	if branch == "" || base == "" {
		return 0
	}
	out, err := run(dir, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range out {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
