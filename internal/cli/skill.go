package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/cloudcons/deconflict-cli/internal/gitinfo"
)

// The skill ships inside the binary, and a copy of it is written onto every
// machine by `deconflict install`. Nothing updates that copy afterwards. So a
// machine can run a new client with the old instructions — told nothing about a
// feature its own tools offer — and there is no symptom anyone would notice. An
// agent that was never told about the watercooler does not report missing it.
//
// So the written copy carries a stamp: the client version that wrote it, and a
// revision derived from the text itself. The session-start hook compares the
// stamp with the text this binary would write and says so, once, when they
// differ.
//
// The revision is the check, not the version. A version is "dev" on every
// unstamped build, and two builds of the same version can carry different text
// while one is being edited; the text cannot disagree with itself.

var skillStamp = regexp.MustCompile(`<!-- deconflict-skill version=(\S+) revision=([0-9a-f]+) -->`)

// skillRevision identifies the skill text this binary writes.
func skillRevision() string {
	sum := sha256.Sum256([]byte(skillMarkdown))
	return hex.EncodeToString(sum[:])[:12]
}

// clientVersion is the version this binary reports: the one stamped at build
// time, or failing that the module version `go install …@vX` records, which is
// how most people will have got it.
func clientVersion() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}

// renderSkill is the skill as written to disk: the text, with the stamp placed
// after the front matter, where the agent that loads the skill never has to
// read past it and a parser of the front matter never sees it.
func renderSkill() string {
	stamp := fmt.Sprintf("<!-- deconflict-skill version=%s revision=%s -->\n", clientVersion(), skillRevision())
	const fence = "---\n"
	if strings.HasPrefix(skillMarkdown, fence) {
		if end := strings.Index(skillMarkdown[len(fence):], "\n"+fence); end >= 0 {
			cut := len(fence) + end + 1 + len(fence)
			return skillMarkdown[:cut] + stamp + skillMarkdown[cut:]
		}
	}
	return stamp + skillMarkdown
}

// skillLocation is one place `deconflict install` writes the skill, and the
// install command that refreshes that copy.
type skillLocation struct {
	path, command string
}

func skillLocations(root, home string) []skillLocation {
	return []skillLocation{
		{filepath.Join(root, ".claude", "skills", "deconflict", "SKILL.md"), "deconflict install --agent claude"},
		{filepath.Join(home, ".claude", "skills", "deconflict", "SKILL.md"), "deconflict install --agent claude --scope user"},
		{filepath.Join(root, ".agents", "skills", "deconflict", "SKILL.md"), "deconflict install --agent codex"},
		{filepath.Join(home, ".agents", "skills", "deconflict", "SKILL.md"), "deconflict install --agent codex --scope user"},
	}
}

// staleSkill reports whether an installed skill should be refreshed, and why.
// A copy whose text matches is current, whatever version wrote it. A copy
// written by a newer client than this one is left alone: refreshing it would
// be a downgrade, and the fix is to update this binary rather than the file.
func staleSkill(body string) (string, bool) {
	m := skillStamp.FindStringSubmatch(body)
	if m == nil {
		return "was installed before skills were versioned", true
	}
	if m[2] == skillRevision() {
		return "", false
	}
	if newer(m[1], clientVersion()) {
		return "", false
	}
	return "was written by deconflict " + m[1] + " and this client is " + clientVersion(), true
}

// skillContext is the session-start line for installed skills that are out of
// date, one per copy, with the command that refreshes it. Silent when every
// copy is current or none is installed.
func skillContext(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	root := gitinfo.Root(cwd)
	if root == "" {
		root = cwd
	}
	var lines []string
	seen := map[string]bool{}
	for _, loc := range skillLocations(root, home) {
		if seen[loc.path] {
			continue // the repository is the home directory
		}
		seen[loc.path] = true
		body, err := os.ReadFile(loc.path)
		if err != nil {
			continue
		}
		if why, stale := staleSkill(string(body)); stale {
			lines = append(lines, fmt.Sprintf("  %s %s — run `%s` to update it.", loc.path, why, loc.command))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "The deconflict skill installed here is out of date, so it does not describe everything these tools can do:\n" +
		strings.Join(lines, "\n")
}

// newer reports whether version a is later than b. Only v-prefixed dotted
// versions are compared; anything else, "dev" included, is never newer, which
// errs toward suggesting a refresh.
func newer(a, b string) bool {
	pa, oka := semver(a)
	pb, okb := semver(b)
	if !oka || !okb {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func semver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
