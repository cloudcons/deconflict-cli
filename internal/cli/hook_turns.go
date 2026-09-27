package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"crypto/sha256"
	"encoding/hex"

	"github.com/cloudcons/deconflict-cli/internal/gitinfo"
	"github.com/cloudcons/deconflict-cli/pkg/claim"
	"github.com/cloudcons/deconflict-cli/pkg/protocol"
)

// The boundaries between the two the hooks started with.
//
// Session start and the first write into someone else's claim were the only
// moments anything reached an agent. Between them an agent reading, testing or
// waiting on CI heard nothing: mail sat in its mailbox, a question addressed to
// it went to its asker's default, and it could finish holding a claim whose
// branch had long since landed. Being told to "watch the watercooler" was the
// only way it ever looked. These hooks put the registry at the boundaries an
// agent already crosses — each prompt, its tool calls, the commit, and the
// moment it tries to stop — and stay silent when there is nothing new.

// mailInterval bounds how often the per-tool boundaries ask the registry for
// mail. A tool call is frequent and the registry is remote; a prompt and a
// session start always ask.
const mailInterval = 2 * time.Minute

// due reports whether interval has passed since the last time key was due in
// this session, and records now if it has. Local and cheap: it is what stands
// between a tool call and a network round trip. With no session, always due.
func due(session, key string, interval time.Duration) bool {
	if session == "" {
		return true
	}
	path := stampPath(session, key)
	if b, err := os.ReadFile(path); err == nil {
		if last, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			if time.Since(time.Unix(last, 0)) < interval {
				return false
			}
		}
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
	return true
}

func stampPath(session, key string) string {
	sum := sha256.Sum256([]byte(session + "|" + key))
	return filepath.Join(os.TempDir(), "deconflict-seen", "at-"+hex.EncodeToString(sum[:8]))
}

// ownActiveClaim is this worktree's claim, if it is still in force.
func ownActiveClaim(claims []claim.Claim, cwd string, now time.Time) *claim.Claim {
	id := readCurrent(cwd)
	if id == "" {
		return nil
	}
	for i := range claims {
		if claims[i].ID == id && claims[i].Active(now) {
			return &claims[i]
		}
	}
	return nil
}

// unclaimedNudge is the first write in a repository this agent holds no claim
// in. Nothing else ever said so: the pre-write hook only spoke about other
// agents' ground, so unannounced work was silent all the way to the merge.
// Once per repository per session.
func unclaimedNudge(claims []claim.Claim, cwd string, rels []string, session string, now time.Time) string {
	repo := gitinfo.Repo(cwd)
	if repo == "" || len(rels) == 0 || ownActiveClaim(claims, cwd, now) != nil {
		return ""
	}
	if !markSeen(session, "unclaimed|"+repo) {
		return ""
	}
	return fmt.Sprintf("You are editing %s in %s without a claim, so no other agent can see this work.\n"+
		"Announce it before going further: `deconflict claim --paths '<globs>' --what '<what>' --why '<why>' --not '<globs you will not touch>'`",
		strings.Join(rels, ", "), repo)
}

// gitCommit matches a shell command that commits or pushes, including through
// `git -C <dir>` and inside a compound command.
var gitCommit = regexp.MustCompile(`(^|[;&|(\s])git(\s+-C\s+\S+)?\s+(commit|push)\b`)

// shellCommand is the command a shell tool is about to run, from Claude
// Code's Bash (a string) or Codex's shell (a string or an argv array).
func shellCommand(in hookInput) string {
	switch v := in.ToolInput["command"].(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, p := range v {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// commitContext is the moment work leaves the worktree: a commit or a push.
// It is when an unannounced change, or one that outgrew its claim, becomes
// somebody else's merge conflict, so it is the last cheap moment to say so.
// Returns before any network call unless the command commits or pushes.
func commitContext(dsn, cwd string, in hookInput) string {
	if !gitCommit.MatchString(shellCommand(in)) {
		return ""
	}
	repo := gitinfo.Repo(cwd)
	if repo == "" {
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
	mine := ownActiveClaim(claim.Fold(evs), cwd, now)
	if mine == nil {
		if !markSeen(in.SessionID, "commit-unclaimed|"+repo) {
			return ""
		}
		return fmt.Sprintf("You are committing in %s with no claim, so no other agent knew this work was happening.\n"+
			"Announce what it covers: `deconflict claim --paths '<globs>' --what '<what>' --why '<why>'`", repo)
	}
	_, outside := changedOutside(cwd, *mine, gitinfo.BaseRef(cwd))
	if len(outside) == 0 {
		return ""
	}
	sort.Strings(outside)
	if !markSeen(in.SessionID, "commit-drift|"+mine.ID+"|"+strings.Join(outside, ",")) {
		return ""
	}
	return fmt.Sprintf("This commit carries %d file(s) outside claim %s (%s):\n  %s\n"+
		"Amend the claim so it keeps describing the work: `deconflict amend --paths '%s'`",
		len(outside), mine.ID, strings.Join(mine.Paths, ", "), strings.Join(outside, "\n  "),
		strings.Join(append(append([]string{}, mine.Paths...), outside...), ","))
}

// pendingQuestion mirrors GET /v1/agents/{agent}/questions.
type pendingQuestion struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Asker   string `json:"asker"`
	Assume  string `json:"assume"`
	Where   string `json:"where"`
	Answer  string `json:"answer"`
}

// stopHook runs when the agent tries to finish. If something is waiting on it
// — a question put to it, mail it has not acknowledged, a claim to release or
// amend — it is asked to deal with that first, once per thing, and then let
// go. Silence lets it stop.
//
// This is the boundary that replaces "remember to check deconflict": the
// work is raised exactly when the agent believes it is done, which is the last
// moment it still has the context to answer.
func stopHook(dsn, cwd string, in hookInput, out io.Writer) error {
	var items []string
	me := agentID()

	if h, err := negotiationClient(dsn); err == nil {
		var questions []pendingQuestion
		// An older registry has no such route; that is a quiet no.
		if err := h.JSON("GET", "/v1/agents/"+url.PathEscape(me)+"/questions", nil, &questions); err == nil {
			for _, q := range questions {
				if !markSeen(in.SessionID, "stop-question|"+q.ID) {
					continue
				}
				line := fmt.Sprintf("- %s asked you (%s): %q", q.Asker, q.Where, q.Subject)
				if q.Assume != "" {
					line += fmt.Sprintf("\n  If nobody answers, they go ahead as: %s", q.Assume)
				}
				line += "\n  Answer: " + q.Answer
				items = append(items, line)
			}
		}
		var mail []protocol.Message
		if err := h.JSON("GET", "/v1/messages?agent_id="+url.QueryEscape(me), nil, &mail); err == nil && len(mail) > 0 {
			ids := make([]string, 0, len(mail))
			for _, m := range mail {
				ids = append(ids, m.DeliveryID)
			}
			sort.Strings(ids)
			if markSeen(in.SessionID, "stop-mail|"+strings.Join(ids, ",")) {
				items = append(items, fmt.Sprintf("- %d message(s) in your mailbox are unacknowledged. Act on them, then `deconflict message ack %s`.",
					len(mail), strings.Join(ids, " ")))
			}
		}
	}

	if st, err := openStore(dsn); err == nil {
		if evs, err := st.Events(); err == nil {
			now := time.Now().UTC()
			if mine := ownActiveClaim(claim.Fold(evs), cwd, now); mine != nil {
				base := gitinfo.BaseRef(cwd)
				if mine.Branch != "" && gitinfo.Landed(cwd, mine.Branch, base, mine.HeadSHA) {
					if markSeen(in.SessionID, "stop-landed|"+mine.ID) {
						items = append(items, fmt.Sprintf("- Your claim %s is still held, but its branch %s has landed. Release it: `deconflict release --reason merged`.", mine.ID, mine.Branch))
					}
				} else if _, outside := changedOutside(cwd, *mine, base); len(outside) > 0 {
					sort.Strings(outside)
					if markSeen(in.SessionID, "stop-drift|"+mine.ID+"|"+strings.Join(outside, ",")) {
						items = append(items, fmt.Sprintf("- You changed %d file(s) outside claim %s: %s. Amend it: `deconflict amend --paths '%s'`.",
							len(outside), mine.ID, strings.Join(outside, ", "),
							strings.Join(append(append([]string{}, mine.Paths...), outside...), ",")))
					}
				}
			}
		}
	}

	if len(items) == 0 {
		return nil
	}
	reason := "Before you finish, deconflict has things waiting on you:\n\n" + strings.Join(items, "\n\n") +
		"\n\nDeal with each, or say why it does not apply, then finish. You will not be stopped for these again."
	return json.NewEncoder(out).Encode(map[string]any{"decision": "block", "reason": reason})
}
