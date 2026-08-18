package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudcons/deconflict/internal/claim"
	"github.com/cloudcons/deconflict/internal/gitinfo"
)

// Claude Code hook plumbing.
//
// The whole point of the hook layer: never rely on an agent remembering to ask.
// "Check the claims registry first" is a probabilistic instruction to an LLM.
// Injection is not. Claims arrive in context at session start, and again at the
// moment a file under someone else's claim is first opened for writing — which
// is the last instant the warning can still change what happens.

type hookInput struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
}

func cmdHook(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	kind := fs.Arg(0)
	if kind == "" {
		return fmt.Errorf("hook needs one of: session-start, pre-tool, user-prompt")
	}
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	var in hookInput
	_ = json.Unmarshal(raw, &in) // a malformed payload degrades to "no context"

	cwd := in.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd != "" {
		// Run git queries and repo detection from the session's directory.
		_ = os.Chdir(cwd)
	}

	var text string
	switch kind {
	case "session-start", "user-prompt":
		text = sessionContext(*dsn, cwd)
	case "pre-tool":
		text = preToolContext(*dsn, cwd, in)
	default:
		return fmt.Errorf("unknown hook kind %q", kind)
	}

	if text == "" {
		return nil // silence is the common case; emit nothing at all
	}
	event := map[string]string{
		"session-start": "SessionStart",
		"user-prompt":   "UserPromptSubmit",
		"pre-tool":      "PreToolUse",
	}[kind]
	resp := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": text,
		},
	}
	enc := json.NewEncoder(out)
	return enc.Encode(resp)
}

// sessionContext is what an agent sees before it starts: who else is inside
// this repo right now. Capped, because this lands in every session's context.
func sessionContext(dsn, cwd string) string {
	st, err := openStore(dsn)
	if err != nil {
		return ""
	}
	evs, err := st.Events()
	if err != nil {
		// Advisory means never blocking: a registry that is down produces no
		// warnings, not a broken session.
		return ""
	}
	now := time.Now().UTC()
	repo := gitinfo.Repo(cwd)
	mine := map[string]bool{}
	if id := readCurrent(cwd); id != "" {
		mine[id] = true
	}
	var active []claim.Claim
	for _, c := range claim.Fold(evs) {
		if c.Repo == repo && c.Active(now) && !mine[c.ID] {
			active = append(active, c)
		}
	}
	if len(active) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Other agents are currently working in this repository (advisory claims):\n")
	for i, c := range active {
		if i == 8 {
			fmt.Fprintf(&b, "\n  ...and %d more (`claims list`).\n", len(active)-i)
			break
		}
		fmt.Fprintf(&b, "\n  [%s] %s (%s ago): %s\n", c.ID, c.Agent, c.Age(now), c.What)
		fmt.Fprintf(&b, "    paths: %s\n", strings.Join(c.Paths, ", "))
		if len(c.NotPaths) > 0 {
			fmt.Fprintf(&b, "    NOT touching: %s\n", strings.Join(c.NotPaths, ", "))
		}
		if c.Interface != "" {
			fmt.Fprintf(&b, "    interface changes: %s\n", c.Interface)
		}
	}
	b.WriteString("\nBefore editing files in those areas, run `claims check --file <path>`. " +
		"When you start a task of your own, announce it: " +
		"`claims claim --paths <globs> --what <text> --why <text> --not <globs>`.\n")
	return b.String()
}

var writeTools = map[string]bool{"Edit": true, "Write": true, "NotebookEdit": true, "MultiEdit": true}

func preToolContext(dsn, cwd string, in hookInput) string {
	if !writeTools[in.ToolName] {
		return ""
	}
	fp, _ := in.ToolInput["file_path"].(string)
	if fp == "" {
		return ""
	}
	root := gitinfo.Root(cwd)
	rel := fp
	if root != "" {
		if r, err := filepath.Rel(root, fp); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}
	st, err := openStore(dsn)
	if err != nil {
		return ""
	}
	evs, err := st.Events()
	if err != nil {
		return ""
	}
	now := time.Now().UTC()
	mine := map[string]bool{}
	if id := readCurrent(cwd); id != "" {
		mine[id] = true
	}
	conflicts := claim.FindOverlaps(claim.Fold(evs), gitinfo.Repo(cwd), []string{rel}, now, mine, st.Settings().IgnorePaths)
	if len(conflicts) == 0 {
		return ""
	}
	// Say each thing once per session. Repeating an identical warning on every
	// edit burns context and trains the model to skim past it.
	var fresh []claim.Conflict
	for _, c := range conflicts {
		if markSeen(in.SessionID, c.Other.ID+"|"+rel) {
			fresh = append(fresh, c)
		}
	}
	if len(fresh) == 0 {
		return ""
	}
	return fmt.Sprintf("You are about to edit %s, which another agent has claimed.\n\n%s", rel, claim.Render(fresh, now))
}

// markSeen records a (session, key) pair and reports whether it was new.
func markSeen(session, key string) bool {
	if session == "" {
		return true
	}
	sum := sha256.Sum256([]byte(session))
	dir := filepath.Join(os.TempDir(), "agentclaims-seen")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true
	}
	path := filepath.Join(dir, hex.EncodeToString(sum[:8]))
	b, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(b), "\n") {
		if l == key {
			return false
		}
	}
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return true
	}
	defer fh.Close()
	fmt.Fprintln(fh, key)
	return true
}
