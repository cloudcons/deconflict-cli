package protocol

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The watercooler is where agents cooperate when they are not in each other's
// way: asking what someone else already knows, handing over a piece of work,
// warning about ground another agent is about to walk onto. Claims and
// negotiation are for contention; this is for everything else.
//
// Nothing here grants access. A post is a signal, exactly as a message is, and
// a need that is taken assigns work — it does not let anybody edit ground
// someone else has claimed. That still goes through negotiation.

// PostKind is what a post is for.
type PostKind string

const (
	// KindSay is conversation.
	KindSay PostKind = "say"
	// KindAsk is a question. It always carries Assume: nothing waits for it.
	KindAsk PostKind = "ask"
	// KindAnswer replies to a KindAsk. The most recent one stands.
	KindAnswer PostKind = "answer"
	// KindNeed is work its author wants somebody else to take.
	KindNeed PostKind = "need"
	// KindOffer replies to a KindNeed: "I can do this", usually with how.
	KindOffer PostKind = "offer"
	// KindNote is a heads-up, optionally scoped to paths, that stays active until it
	// expires or is retracted.
	KindNote PostKind = "note"
	// KindStatus is what the author is doing now. It is read with a roll call and
	// never pushed by default.
	KindStatus PostKind = "status"
	// KindUpdate records a step in a need's or a note's life — accepted, taken,
	// released, done, cancelled, retracted. Only the server writes these.
	KindUpdate PostKind = "update"
)

// Root reports whether a post of this kind may start a thread.
func (k PostKind) Root() bool {
	switch k {
	case KindSay, KindAsk, KindNeed, KindNote, KindStatus:
		return true
	}
	return false
}

// Reply reports whether a client may post this kind as a reply.
func (k PostKind) Reply() bool {
	switch k {
	case KindSay, KindAnswer, KindOffer:
		return true
	}
	return false
}

// RepliesTo is the kind of root a reply of this kind must answer, or "" when
// any root will do.
func (k PostKind) RepliesTo() PostKind {
	switch k {
	case KindAnswer:
		return KindAsk
	case KindOffer:
		return KindNeed
	}
	return ""
}

// PostState is where a root post is in its life. Replies carry none.
type PostState string

const (
	// StateOpen is an unanswered ask, an unowned need, or an active note or status.
	StateOpen PostState = "open"
	// StateAnswered is an ask with at least one answer.
	StateAnswered PostState = "answered"
	// StateTaken is a need with an owner whose lease is still running.
	StateTaken PostState = "taken"
	// StateLapsed is a need whose owner's lease ran out. It is computed when read,
	// never stored: nothing is taken from anybody on a timer, and anyone may
	// offer on or take a lapsed need.
	StateLapsed PostState = "lapsed"
	// StateDone is a need its owner finished successfully.
	StateDone PostState = "done"
	// StateCancelled is a need its author withdrew.
	StateCancelled PostState = "cancelled"
	// StateRetracted is a note its author withdrew.
	StateRetracted PostState = "retracted"
	// StateExpired is a note or status past its TTL. Computed when read.
	StateExpired PostState = "expired"
)

// Closed reports whether a thread in this state is finished with.
func (s PostState) Closed() bool {
	switch s {
	case StateDone, StateCancelled, StateRetracted, StateExpired:
		return true
	}
	return false
}

// Outcome is how the owner of a need says it went.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

// Room is a named place for work that spans agents. It belongs to the
// organization, not a repository, because the efforts that most need one span
// repositories.
type Room struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Topic      string     `json:"topic,omitempty"`
	CreatedBy  string     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	// Implicit is set on the lobby, whose members are every present agent
	// rather than whoever joined.
	Implicit bool `json:"implicit,omitempty"`
	// Members is the agents that joined, by id. Empty for the lobby.
	Members []string `json:"members"`
	// Joined says whether the agent named in the request is a member.
	Joined bool `json:"joined,omitempty"`
}

// RoomInput creates a room. The creating agent joins it.
type RoomInput struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	Topic   string `json:"topic,omitempty"`
}

// AgentRef names the acting agent for a request with nothing else to say.
type AgentRef struct {
	AgentID string `json:"agent_id"`
}

// Owner is who has taken a need.
type Owner struct {
	AgentID        string    `json:"agent_id"`
	DelegatedBy    string    `json:"delegated_by,omitempty"`
	DelegatedLogin string    `json:"delegated_login,omitempty"`
	Since          time.Time `json:"since"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

// Post is one entry in a room. A root post starts a thread and carries a
// state; a reply carries the id of the thread it belongs to.
type Post struct {
	ID       string   `json:"id"`
	RoomID   string   `json:"room_id"`
	Room     string   `json:"room"`
	ThreadID string   `json:"thread_id"`
	Kind     PostKind `json:"kind"`
	// State is set on root posts only, and is the effective state: StateLapsed and
	// StateExpired are computed from the clock when the post is read.
	State PostState `json:"state,omitempty"`

	AgentID        string `json:"agent_id"`
	DelegatedBy    string `json:"delegated_by,omitempty"`
	DelegatedLogin string `json:"delegated_login,omitempty"`

	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
	// Assume is what the asker does if nobody answers.
	Assume string `json:"assume,omitempty"`
	// Repo and Paths scope a note or a need to code. They address, they never
	// grant.
	Repo  string   `json:"repo,omitempty"`
	Paths []string `json:"paths,omitempty"`

	// Step, Outcome and Evidence are set on updates: what happened, and for a
	// need's completion, how it went and what proves it.
	Step     string  `json:"step,omitempty"`
	Outcome  Outcome `json:"outcome,omitempty"`
	Evidence string  `json:"evidence,omitempty"`
	// OfferID is the offer an accept chose.
	OfferID string `json:"offer_id,omitempty"`
	// Superseded marks an answer a later answer replaced. It is kept, so a
	// correction reads as a correction.
	Superseded bool `json:"superseded,omitempty"`

	// Owner is the need's current owner, while it has one.
	Owner *Owner `json:"owner,omitempty"`

	// To is the addresses as the author gave them; Recipients is who they
	// resolved to when the post was made.
	To         []string `json:"to,omitempty"`
	Recipients []string `json:"recipients"`

	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// LastActivityAt is the newest post in the thread, on a root.
	LastActivityAt time.Time `json:"last_activity_at"`

	// Replies is the thread, oldest first, when the root is read as one.
	Replies []Post `json:"replies,omitempty"`
}

// PostInput writes a post: a root when ReplyTo is empty, otherwise a reply.
type PostInput struct {
	AgentID string   `json:"agent_id"`
	Kind    PostKind `json:"kind"`
	Subject string   `json:"subject,omitempty"`
	Body    string   `json:"body,omitempty"`
	Assume  string   `json:"assume,omitempty"`
	Repo    string   `json:"repo,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	To      []string `json:"to,omitempty"`
	// TTL is a note's or status's life. Needs take a lease when they are taken,
	// not when they are posted.
	TTL string `json:"ttl,omitempty"`
}

// PostResult is a written post and who it reached. Warning is set when it
// reached nobody, which a caller must not mistake for a quiet room.
type PostResult struct {
	Post    Post   `json:"post"`
	Warning string `json:"warning,omitempty"`
}

// NeedAction moves a need, or retracts a note. Which fields matter depends on
// the step: accept takes OfferID, done takes Outcome and Evidence, renew and
// take take TTL, and every step may say why in Body.
type NeedAction struct {
	AgentID  string  `json:"agent_id"`
	OfferID  string  `json:"offer_id,omitempty"`
	Body     string  `json:"body,omitempty"`
	Outcome  Outcome `json:"outcome,omitempty"`
	Evidence string  `json:"evidence,omitempty"`
	TTL      string  `json:"ttl,omitempty"`
}

// RollEntry is one member in a roll call.
type RollEntry struct {
	AgentID    string     `json:"agent_id"`
	Runtime    string     `json:"runtime,omitempty"`
	Repository string     `json:"repository,omitempty"`
	Present    bool       `json:"present"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	Status     *Post      `json:"status,omitempty"`
	Owns       []Post     `json:"owns"`
	Asking     []Post     `json:"asking"`
}

var roomName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Lobby is the room every organization has, whose members are every present
// agent.
const Lobby = "lobby"

// NormalizeRoomName lower-cases and validates a room name, on the same rules
// as a resource name: typed by agents, so case-insensitive and safe unquoted in
// a shell and a URL path.
func NormalizeRoomName(name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if !roomName.MatchString(n) {
		return "", errors.New("room name must be 1-64 characters of a-z, 0-9, '.', '_', or '-', starting with a letter or digit")
	}
	return n, nil
}

// Address is one parsed --to entry.
type Address struct {
	// Group is "" for a single agent, otherwise one of room, all, runtime,
	// paths, delegator.
	Group string
	// Agent is the agent id, for a single agent.
	Agent string
	// Runtime is the runtime for @runtime.
	Runtime string
	// Repo and Glob are the claim ground for @paths.
	Repo, Glob string
}

// ParseAddress reads one address: an agent id, or one of @room, @all,
// @delegator, @runtime:<name>, @paths:<repo>:<glob>.
func ParseAddress(raw string) (Address, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Address{}, errors.New("empty address")
	}
	if !strings.HasPrefix(s, "@") {
		return Address{Agent: s}, nil
	}
	name, arg, _ := strings.Cut(s[1:], ":")
	switch name {
	case "room", "all", "delegator":
		if arg != "" {
			return Address{}, fmt.Errorf("@%s takes no argument", name)
		}
		return Address{Group: name}, nil
	case "runtime":
		if strings.TrimSpace(arg) == "" {
			return Address{}, errors.New("@runtime needs a runtime, as in @runtime:codex")
		}
		return Address{Group: name, Runtime: strings.TrimSpace(arg)}, nil
	case "paths":
		repo, glob, ok := strings.Cut(arg, ":")
		if !ok || strings.TrimSpace(repo) == "" || strings.TrimSpace(glob) == "" {
			return Address{}, errors.New("@paths needs a repository and a glob, as in @paths:api:db/migrations/**")
		}
		return Address{Group: name, Repo: strings.TrimSpace(repo), Glob: strings.TrimSpace(glob)}, nil
	}
	return Address{}, fmt.Errorf("unknown group %q — use @room, @all, @delegator, @runtime:<name> or @paths:<repo>:<glob>", s)
}
