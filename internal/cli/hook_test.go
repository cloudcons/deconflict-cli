package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A repository pair: two checkouts of one remote, which is the shape the hook
// actually runs in. Claiming and checking from the same checkout is a different
// question — there the claim is correctly yours — and testing that instead is
// how this behaviour looks fine when it is not.
type hookRepo struct {
	t     *testing.T
	dsn   string
	mine  string
	other string
}

func newHookRepo(t *testing.T) *hookRepo {
	t.Helper()
	root := t.TempDir()
	h := &hookRepo{
		t:     t,
		dsn:   "file:" + filepath.Join(root, "store.json"),
		mine:  filepath.Join(root, "mine"),
		other: filepath.Join(root, "other"),
	}
	for _, dir := range []string{h.mine, h.other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		run(t, dir, "git", "init", "-q")
		run(t, dir, "git", "remote", "add", "origin", "https://example.test/acme/api.git")
	}
	return h
}

// claimAs stakes a claim from the other checkout, so the hook under test sees it
// as somebody else's.
func (h *hookRepo) claimAs(agent, paths string) {
	h.t.Helper()
	h.t.Setenv("DECONFLICT_AGENT", agent)
	restore := chdir(h.t, h.other)
	defer restore()

	var out bytes.Buffer
	if err := cmdClaim([]string{
		"--store", h.dsn, "--paths", paths,
		"--what", "rework token refresh", "--why", "the expiry is wrong",
		"--not", "src/auth/ldap/**",
	}, &out); err != nil {
		h.t.Fatalf("claim: %v (%s)", err, out.String())
	}
	h.t.Setenv("DECONFLICT_AGENT", "me")
}

func (h *hookRepo) preTool(session, file string) string {
	h.t.Helper()
	return preToolContext(h.dsn, h.mine, hookInput{
		SessionID:     h.t.Name() + "/" + session,
		CWD:           h.mine,
		HookEventName: "PreToolUse",
		ToolName:      "Edit",
		ToolInput:     map[string]any{"file_path": file},
	})
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}

// Ten files under one claim used to mean ten near-identical blocks, because the
// dedup key included the file set and Claude Code asks once per file. The cost
// is paid in the context window of the agent we are trying to keep informed.
func TestOneClaimWarnsOnceThenBriefly(t *testing.T) {
	h := newHookRepo(t)
	h.claimAs("other-agent", "src/auth/**")

	full := h.preTool("s", "src/auth/token.go")
	if !strings.Contains(full, "advisory") {
		t.Fatalf("first edit should get the whole explanation, got %q", full)
	}

	for _, f := range []string{"store.go", "session.go", "cookie.go"} {
		b := h.preTool("s", "src/auth/"+f)
		switch {
		case b == "":
			t.Errorf("%s: a newly-touched claimed file said nothing at all", f)
		case len(b) > len(full)/3:
			t.Errorf("%s: repeat warning is not brief (%d vs %d chars): %q", f, len(b), len(full), b)
		case strings.Contains(b, "advisory"):
			t.Errorf("%s: repeat warning re-explains the claim: %q", f, b)
		}
	}

	// The same file twice is nothing new at all — including the first one,
	// which is the case a naive fix leaves repeating forever.
	for _, f := range []string{"token.go", "store.go"} {
		if got := h.preTool("s", "src/auth/"+f); got != "" {
			t.Errorf("%s warned again on revisit: %q", f, got)
		}
	}

	// A different session has been told nothing yet.
	if got := h.preTool("other-session", "src/auth/token.go"); !strings.Contains(got, "advisory") {
		t.Errorf("a new session should get the whole explanation, got %q", got)
	}
}

// An unclaimed file must cost nothing at all: the quiet case is the common case,
// and a hook that always says something is a hook people turn off.
func TestUnclaimedFileIsSilent(t *testing.T) {
	h := newHookRepo(t)
	h.claimAs("other-agent", "src/auth/**")

	if got := h.preTool("s", "src/billing/invoice.go"); got != "" {
		t.Errorf("unclaimed file produced output: %q", got)
	}
}
