package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/claim"
	"github.com/cloudcons/deconflict-cli/internal/gitinfo"
	"github.com/cloudcons/deconflict-cli/pkg/protocol"
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
		// Held resources are told once, at the start. Repeating them on every
		// prompt would spend context on the same lines until they were skimmed.
		if kind == "session-start" {
			if deps := dependencyContext(*dsn, cwd); deps != "" {
				if text != "" {
					text += "\n"
				}
				text += deps
			}
			if held := resourceContext(*dsn); held != "" {
				if text != "" {
					text += "\n"
				}
				text += held
			}
		}
	case "pre-tool":
		text = preToolContext(*dsn, cwd, in)
	default:
		return fmt.Errorf("unknown hook kind %q", kind)
	}
	if mailbox := mailboxContext(*dsn, cwd, in); mailbox != "" {
		if text != "" {
			text += "\n\n"
		}
		text += mailbox
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

// mailboxContext bridges independently launched agent sessions. Hooks cannot
// wake a closed process, but every live session checks the durable inbox at its
// normal boundaries.
//
// It reads and never acknowledges. Rendering a message into this string is not
// evidence that any agent read it, so the mailbox stays at-least-once and the
// agent acknowledges what it has actually processed.
func mailboxContext(dsn, cwd string, in hookInput) string {
	h, err := negotiationClient(dsn)
	if err != nil {
		return ""
	}
	instance := strings.TrimSpace(in.SessionID)
	if instance == "" {
		instance = defaultInstance()
	}
	registration := protocol.RegisterInput{
		AgentID:      agentID(),
		Name:         agentID(),
		Runtime:      runtimeName(),
		InstanceID:   instance,
		Repository:   gitinfo.Repo(cwd),
		Capabilities: []string{"messaging", "negotiation", "checkpoints"},
		TTL:          "5m",
	}
	var registered protocol.Registration
	if err := h.JSON("POST", "/v1/agents/register", registration, &registered); err != nil {
		return ""
	}
	var messages []protocol.Message
	path := "/v1/messages?agent_id=" + url.QueryEscape(registration.AgentID)
	if err := h.JSON("GET", path, nil, &messages); err != nil || len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Deconflict delivered messages from other autonomous agents:\n")
	for i, message := range messages {
		if i == 12 {
			fmt.Fprintf(&b, "\n  ...and %d more (`deconflict message inbox`).", len(messages)-i)
			break
		}
		fmt.Fprintf(&b, "\n  [%s] %s from %s", message.Kind, message.DeliveryID, message.SenderAgentID)
		if message.NegotiationID != "" {
			fmt.Fprintf(&b, " (negotiation %s)", message.NegotiationID)
		}
		if body, ok := message.Payload["body"].(string); ok && body != "" {
			fmt.Fprintf(&b, ": %s", body)
		} else if objective, ok := message.Payload["objective"].(string); ok && objective != "" {
			fmt.Fprintf(&b, ": %s", objective)
		}
	}
	// Deliberately not acknowledged here.
	//
	// This hook used to ack each message while rendering it, which made the
	// mailbox at-most-once: everything between the ack and the model actually
	// reading this string — a truncated hook output, a killed process, a
	// compacted context — consumed the message permanently. An agent lost two
	// deliveries that way and recovered them only from persisted hook output,
	// which is luck rather than a mechanism, on the one channel the whole
	// protocol depends on.
	//
	// Inbox already stamps delivered_at, so "the registry handed this over" is
	// recorded without anyone claiming to have read it. Acknowledgement is a
	// statement about having processed a message, and only the agent that
	// processed it can honestly make that statement. Unacknowledged mail is
	// redelivered at the next session start, which is the failure everyone
	// would rather have.
	b.WriteString("\n\nTreat delivery as a signal, not consent. Inspect the negotiation before proposing, accepting, or changing shared resources.")
	b.WriteString("\nThese stay in your mailbox until you acknowledge them, and will be delivered again next session: `deconflict message ack <delivery-id>` once you have acted on one, or `deconflict message ack <id> <id> ...` for a batch you have worked through.")
	return b.String()
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
			fmt.Fprintf(&b, "\n  ...and %d more (`deconflict list`).\n", len(active)-i)
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
	b.WriteString("\nBefore editing files in those areas, run `deconflict check --file <path>`. " +
		"When you start a task of your own, announce it: " +
		"`deconflict claim --paths <globs> --what <text> --why <text> --not <globs>`.\n")
	return b.String()
}

// ownDrift is the other half of the sentence this hook has always spoken.
//
// FindOverlaps is handed the caller's own claim as an exclusion, so the warning
// it produces is always about somebody else's ground and never about leaving
// your own. That left one signal for having drifted from what you declared —
// `deconflict status`, run by hand, if it occurs to you — and an agent that
// widens its area silently is exactly the case the announcement exists to
// prevent. Two agents corrected their own claims during a live run by noticing;
// nothing told them.
//
// Said at the moment of the write, because that is when it is cheap to fix and
// when the agent still knows why it is touching the file.
func ownDrift(claims []claim.Claim, id string, rels []string, now time.Time) string {
	if id == "" {
		return ""
	}
	var mine *claim.Claim
	for i := range claims {
		if claims[i].ID == id && claims[i].Active(now) {
			mine = &claims[i]
			break
		}
	}
	if mine == nil {
		return ""
	}
	var outside []string
	for _, rel := range rels {
		covered := false
		for _, p := range mine.Paths {
			if claim.Match(p, rel) {
				covered = true
				break
			}
		}
		if !covered {
			outside = append(outside, rel)
		}
	}
	if len(outside) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"This edit is outside what you announced in claim %s (%s).\n"+
			"  outside: %s\n"+
			"Nothing is blocked. If the work really does reach here, amend the claim so it keeps\n"+
			"describing what you are doing: `deconflict amend --paths '%s'`",
		mine.ID, strings.Join(mine.Paths, ", "), strings.Join(outside, ", "),
		strings.Join(append(append([]string{}, mine.Paths...), outside...), ","))
}

func preToolContext(dsn, cwd string, in hookInput) string {
	targets := editedPaths(in.ToolName, in.ToolInput)
	if len(targets) == 0 {
		return ""
	}
	root := gitinfo.Root(cwd)
	var rels []string
	for _, t := range targets {
		rels = append(rels, relToRepo(root, t))
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
	folded := claim.Fold(evs)
	conflicts := claim.FindOverlaps(folded, gitinfo.Repo(cwd), rels, now, mine, st.Settings().IgnorePaths)
	drift := ownDrift(folded, readCurrent(cwd), rels, now)
	if len(conflicts) == 0 {
		return drift
	}
	// Say each thing once per session, and say it at the length it is worth.
	//
	// A claim on src/auth/** covers every file under it, and Claude Code asks
	// once per file — so ten edits inside one claimed area used to inject ten
	// near-identical blocks. That is the failure this dedup exists to prevent,
	// arriving through the door the key was too specific to close: it included
	// the file set, and the file set is different every time.
	//
	// So there are two keys. The claim alone decides whether the agent has ever
	// been told about this claim; the claim plus the file set decides whether it
	// has been told about these exact files. First meeting gets the full block,
	// a new file under a known claim gets one line, and the same file twice gets
	// nothing.
	//
	// One patch can touch several files at once, which is why the second key is
	// the whole set rather than a single path.
	key := strings.Join(rels, ",")
	var first, repeat []claim.Conflict
	for _, c := range conflicts {
		switch {
		case markSeen(in.SessionID, c.Other.ID):
			// Record the file set too. Without this the first file is the one
			// file the session never fully remembers: coming back to it later
			// finds no file key and repeats itself.
			markSeen(in.SessionID, c.Other.ID+"|"+key)
			first = append(first, c)
		case markSeen(in.SessionID, c.Other.ID+"|"+key):
			repeat = append(repeat, c)
		}
	}
	if len(first) == 0 && len(repeat) == 0 {
		return ""
	}

	var b strings.Builder
	if len(first) > 0 {
		what := rels[0]
		if len(rels) > 1 {
			what = strings.Join(rels, ", ")
		}
		fmt.Fprintf(&b, "You are about to edit %s, which another agent has claimed.\n\n%s", what, claim.Render(first, now))
	}
	if len(repeat) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(claim.RenderBrief(repeat))
	}
	// Somebody else's ground and your own are different facts about the same
	// edit, and an agent that is told only the first will widen quietly.
	if drift != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(drift)
	}
	return b.String()
}

// markSeen records a (session, key) pair and reports whether it was new.
func markSeen(session, key string) bool {
	if session == "" {
		return true
	}
	sum := sha256.Sum256([]byte(session))
	dir := filepath.Join(os.TempDir(), "deconflict-seen")
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

// dependencyContext is the other half of what a session needs before it plans:
// not who is editing beside it, which sessionContext says, but who is changing
// the ground its own claim stands on, and who stands on the ground it is
// changing. Only for the claim this worktree recorded, and only at session
// start — a dependency does not change between prompts.
func dependencyContext(dsn, cwd string) string {
	id := readCurrent(cwd)
	if id == "" {
		return ""
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
	all := claim.Fold(evs)
	for _, c := range all {
		if c.ID == id && c.Active(now) {
			return claim.RenderDependencies(claim.DependenciesOf(all, c, now), claim.DependentsOf(all, c, now), now)
		}
	}
	return ""
}
