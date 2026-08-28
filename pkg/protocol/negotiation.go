// Package protocol is the wire contract between a deconflict client and a
// deconflict registry: the JSON types that cross the boundary, and nothing
// else. It holds no state, opens no connections and imports nothing outside
// the standard library.
//
// These declarations are a published interface. Fields may be added; a field
// or a constant value that has shipped is never removed or repurposed, because
// an installed CLI older than the registry it is talking to must keep working.
// The registry is advisory — it may never be the reason an agent stops.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Status string

const (
	Negotiating        Status = "negotiating"
	Agreed             Status = "agreed"
	Executing          Status = "executing"
	CheckpointRequired Status = "checkpoint_required"
	Recovering         Status = "recovering"
	Abandoned          Status = "abandoned"
	Completed          Status = "completed"
	Rejected           Status = "rejected"
)

// AgentIdentity is the autonomous actor taking part in an agreement. The
// server attests DelegatedBy from the presented account credential; an agent
// cannot claim somebody else's delegation in its request body.
type AgentIdentity struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	Runtime      string   `json:"runtime,omitempty"`
	Model        string   `json:"model,omitempty"`
	Instance     string   `json:"instance,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	DelegatedBy  string   `json:"delegated_by"`
	Delegation   string   `json:"delegation"`
}

type Objective struct {
	ID              string   `json:"id,omitempty"`
	Summary         string   `json:"summary"`
	SuccessCriteria []string `json:"success_criteria,omitempty"`
	Priority        string   `json:"priority,omitempty"`
}

type ResourceRequest struct {
	Paths  []string `json:"paths"`
	Access string   `json:"access"` // inspect | modify | exclusive_modify
	Scope  string   `json:"scope,omitempty"`
}

type Lease struct {
	Duration  string    `json:"duration"`
	ExpiresAt time.Time `json:"expires_at"`
	Renewable bool      `json:"renewable"`
}

type AccessRequest struct {
	Repository string            `json:"repository"`
	Agent      AgentIdentity     `json:"agent"`
	Objective  Objective         `json:"objective"`
	Resources  []ResourceRequest `json:"resources"`
	Lease      Lease             `json:"lease"`
}

type Participant struct {
	Agent AgentIdentity `json:"agent"`
	Role  string        `json:"role"` // requester | affected
}

type Dependency struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // artifact | checkpoint | commitment | release
	AgentID     string `json:"agent_id,omitempty"`
	Reference   string `json:"reference"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
}

type Checkpoint struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Evidence string    `json:"evidence,omitempty"`
	DueAt    time.Time `json:"due_at,omitempty"`
	Status   string    `json:"status"` // pending | reached | missed
}

type Commitment struct {
	ID          string       `json:"id"`
	Order       int          `json:"order"`
	AgentID     string       `json:"agent_id"`
	Action      string       `json:"action"`
	Resources   []string     `json:"resources"`
	DependsOn   []string     `json:"depends_on,omitempty"`
	Lease       Lease        `json:"lease"`
	Checkpoints []Checkpoint `json:"checkpoints,omitempty"`
	Status      string       `json:"status"` // pending | ready | executing | satisfied | failed
}

type RecoveryTerms struct {
	OnLeaseExpiry string `json:"on_lease_expiry"`
	OnAgentLost   string `json:"on_agent_lost"`
	Rollback      string `json:"rollback,omitempty"`
	Handoff       string `json:"handoff,omitempty"`
}

type Proposal struct {
	Version      int           `json:"version"`
	Kind         string        `json:"kind"` // proposal | counterproposal
	Parent       int           `json:"parent_version,omitempty"`
	AuthorAgent  string        `json:"author_agent"`
	Rationale    string        `json:"rationale"`
	Dependencies []Dependency  `json:"dependencies,omitempty"`
	Commitments  []Commitment  `json:"commitments"`
	Recovery     RecoveryTerms `json:"recovery"`
	CreatedAt    time.Time     `json:"created_at"`
}

type Acceptance struct {
	AgentID         string    `json:"agent_id"`
	ProposalVersion int       `json:"proposal_version"`
	AcceptedAt      time.Time `json:"accepted_at"`
}

type HumanDecision struct {
	Actor     string    `json:"actor"`
	Decision  string    `json:"decision"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

type Event struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Actor     string         `json:"actor"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Session struct {
	ID             string          `json:"id"`
	OrgID          string          `json:"-"`
	Status         Status          `json:"status"`
	Request        AccessRequest   `json:"request"`
	Participants   []Participant   `json:"participants"`
	Current        Proposal        `json:"current_proposal"`
	Acceptances    []Acceptance    `json:"acceptances"`
	HumanDecisions []HumanDecision `json:"human_decisions,omitempty"`
	Questions      []Question      `json:"questions,omitempty"`
	Events         []Event         `json:"events"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ExpiresAt      time.Time       `json:"expires_at"`
}

type ProposalInput struct {
	AgentID      string        `json:"agent_id"`
	Rationale    string        `json:"rationale"`
	Dependencies []Dependency  `json:"dependencies,omitempty"`
	Commitments  []Commitment  `json:"commitments"`
	Recovery     RecoveryTerms `json:"recovery"`
}

type CheckpointInput struct {
	AgentID      string `json:"agent_id"`
	CommitmentID string `json:"commitment_id"`
	CheckpointID string `json:"checkpoint_id"`
	Evidence     string `json:"evidence"`
}

type RecoveryInput struct {
	AgentID string `json:"agent_id"`
	Reason  string `json:"reason"`
}

// DeviationInput reports that the agreement and the work have come apart.
// CommitmentID is optional and names the commitment the deviation was found
// in, which is usually the reporter's own.
type DeviationInput struct {
	AgentID      string `json:"agent_id"`
	CommitmentID string `json:"commitment_id,omitempty"`
	Reason       string `json:"reason"`
}

// A Question is an agent asking a human something that was never the agents'
// to settle.
//
// Coordination questions do not belong here. Which of two agents goes first is
// theirs, and a deadlock over it is evidence about how the work was split
// rather than a matter for a referee — the humans have less context than either
// agent by then. What belongs here is the other kind: may I break this API, may
// I commit, is this the direction you wanted. Withholding those from a human
// does not preserve autonomy, it produces a confident guess.
//
// Asking does not pause anything. An unanswered question that halts six agents
// is worse than a wrong assumption, so every question carries what its asker
// will do if nobody replies, and silence means that.
type Question struct {
	ID      string `json:"id"`
	AskedBy string `json:"asked_by"`
	// Scope decides who may answer, and is the whole of the routing rule.
	Scope    string   `json:"scope"` // mandate | agreement
	Audience []string `json:"audience"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body,omitempty"`
	// Assume is what the asker proceeds with if nobody answers. It is required:
	// a question with no default is a request to block.
	Assume       string `json:"assume"`
	CommitmentID string `json:"commitment_id,omitempty"`
	// Status walks the ladder: with_peers while the other agents in the
	// agreement may still settle it, deferred once one of them has said it is
	// not theirs to settle, awaiting_asker while the person handed it back for
	// something they need before they can rule, answered once anybody has. A
	// human sees only the deferred ones, which is the whole point — the queue
	// is what the agents could not resolve between them, not everything they
	// wondered about.
	Status string `json:"status"` // with_peers | deferred | awaiting_asker | answered
	// Answer, AnsweredBy and AnsweredAt describe the answer that stands. They
	// are the last entry in Replies, kept flat because almost every reader
	// wants only the current one.
	Answer     string     `json:"answer,omitempty"`
	AnsweredBy string     `json:"answered_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
	// Peers are the other agents working this repository when the question was
	// asked — everyone holding a live claim, not just the participants in this
	// agreement.
	//
	// Participation is derived from claim overlap, so an agreement usually has
	// exactly one participant: only agents whose ground collides end up in one.
	// The agents talk far more widely than that. In a lab run three of them
	// exchanged fifty-one messages while every negotiation had a single
	// participant, so a peer set drawn from participants would have been empty
	// for every question asked, and "ask the others first" would have meant
	// nothing.
	Peers []string `json:"peers,omitempty"`
	// Consulted records which peers engaged, and how. A question reaches a
	// human only after this is non-empty, or after Peers is, so an escalation
	// always arrives with what the agents already tried attached.
	Consulted []Consultation `json:"consulted,omitempty"`
	// Needs says what kind of input is wanted from the person: a decision they
	// alone can make, or a clarification of something the mandate left unclear.
	// Both are input; the difference is what the person has to supply.
	Needs string `json:"needs,omitempty"` // decision | clarification
	// DeferredBy, DeferredReason and DeferredAt record the agent that judged
	// this was not the agents' to settle, and why. Empty while the question is
	// still with the peers.
	DeferredBy     string     `json:"deferred_by,omitempty"`
	DeferredReason string     `json:"deferred_reason,omitempty"`
	DeferredAt     *time.Time `json:"deferred_at,omitempty"`
	// Clarifications are the rounds where the person did not rule but asked the
	// asker something first.
	//
	// There was nowhere to put that. Answering was a single shot, so a human
	// who wanted to understand before deciding had only the answer field, and
	// what they typed there was recorded as the ruling — the agent would have
	// read a question as its instruction. On a protocol whose whole claim is
	// that a human is the last resort, the last resort could not hold a
	// conversation.
	Clarifications []Clarification `json:"clarifications,omitempty"`
	// Replies is every answer given, oldest first.
	//
	// An answer used to be final: the second was refused. That held the line
	// against two people overwriting each other in turn, but it also made a
	// mistake permanent. The first time it met a real error — two answers typed
	// into the wrong boxes — the agents worked around it over the message
	// mailbox and left the record saying something nobody had decided. Working
	// around a record is always available and never fixes it.
	//
	// So an answer can now be superseded, and nothing is thrown away. What
	// stands is the last one; what was said before stays visible, which is what
	// makes a correction legible as a correction rather than a rewrite.
	Replies []Reply `json:"replies,omitempty"`
}

// Consultation is one peer's engagement with a question: either it settled it,
// or it looked and said it could not.
type Consultation struct {
	AgentID   string    `json:"agent_id"`
	Outcome   string    `json:"outcome"` // resolved | cannot_settle
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Clarification is a person asking the asking agent something before ruling,
// and the agent's reply. It settles nothing: when the reply lands the question
// goes back to the human queue still unanswered.
type Clarification struct {
	ID        string     `json:"id"`
	Request   string     `json:"request"`
	AskedBy   string     `json:"asked_by"`
	CreatedAt time.Time  `json:"created_at"`
	Reply     string     `json:"reply,omitempty"`
	RepliedBy string     `json:"replied_by,omitempty"`
	RepliedAt *time.Time `json:"replied_at,omitempty"`
}

// Reply is one answer to a question, and stays in the record after it is
// superseded.
type Reply struct {
	Answer     string    `json:"answer"`
	AnsweredBy string    `json:"answered_by"`
	CreatedAt  time.Time `json:"created_at"`
	// ByAgent names the agent that settled this, when an agent settled it.
	//
	// An agent acts on a credential its human delegated, so from the server's
	// side an agent and the person accountable for it authenticate identically.
	// Recording only the account meant a question resolved by one agent for
	// another was written into the record as a decision by the human — which
	// is the one thing an escalation record must never get wrong, because its
	// entire value is saying who decided.
	ByAgent string `json:"by_agent,omitempty"`
	// Via is how the reply was authenticated: session for a person in a
	// browser, token for a credential. Kept because "a human at a keyboard"
	// and "something holding that human's key" are not the same claim.
	Via string `json:"via,omitempty"`
	// Supersedes counts how many answers came before this one. Zero for the
	// first, so a reader can tell a plain answer from a correction without
	// comparing timestamps.
	Supersedes int `json:"supersedes,omitempty"`
}

type QuestionInput struct {
	AgentID string `json:"agent_id"`
	// Peers is filled in by the server from live claims, never by the caller —
	// an agent must not be able to declare itself alone in the repository and
	// escalate straight past everybody.
	Peers        []string `json:"-"`
	Scope        string   `json:"scope"`
	Subject      string   `json:"subject"`
	Body         string   `json:"body,omitempty"`
	Assume       string   `json:"assume"`
	CommitmentID string   `json:"commitment_id,omitempty"`
}

type AnswerInput struct {
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
	// AgentID is set when a peer agent is resolving the question rather than a
	// human ruling on it. Both authenticate on a credential belonging to the
	// same person, so without this the record cannot tell them apart — and did
	// not, until an agent's resolution turned up in the log signed by a human
	// who had never seen it.
	AgentID string `json:"agent_id,omitempty"`
	// Via is filled in by the server from the credential, never by the caller.
	Via string `json:"-"`
}

// ClarifyInput is a person asking the asking agent for something before they
// will rule on its question.
type ClarifyInput struct {
	QuestionID string `json:"question_id"`
	Request    string `json:"request"`
}

// ExplainInput is the asking agent answering that request. It does not settle
// the question — it puts it back in front of the person who asked.
type ExplainInput struct {
	QuestionID string `json:"question_id"`
	AgentID    string `json:"agent_id"`
	Reply      string `json:"reply"`
}

// DeferInput hands a question up to the humans. It is the only way one reaches
// them, so the reason travels with it: a person pulled into an agreement they
// were not watching needs to know why the agents could not settle it.
type DeferInput struct {
	QuestionID string `json:"question_id"`
	AgentID    string `json:"agent_id"`
	Reason     string `json:"reason"`
	// Needs is what the person has to supply: a decision, or a clarification of
	// something the mandate left unclear. Defaults to decision.
	Needs string `json:"needs,omitempty"`
	// Note is what a peer says when it is deferring because it looked and could
	// not settle the question. Ignored when the asker defers.
	Note string `json:"note,omitempty"`
}

type LeaseInput struct {
	AgentID  string `json:"agent_id"`
	Duration string `json:"duration"`
}

type OverrideInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// NewID mints a protocol identifier with the caller's prefix. Clients generate
// objective ids locally so a request carries its own name before the registry
// has seen it.
func NewID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("protocol: crypto/rand unavailable: " + err.Error())
	}
	return prefix + hex.EncodeToString(b)
}

// ExecutionPlan is a derived projection of an agreement, not a queue stored as
// independent truth. Commitments and dependencies remain the source of order.
type ExecutionPlan struct {
	NegotiationID string        `json:"negotiation_id"`
	Status        Status        `json:"status"`
	Objective     Objective     `json:"objective"`
	Dependencies  []Dependency  `json:"dependencies"`
	Commitments   []Commitment  `json:"commitments"`
	Recovery      RecoveryTerms `json:"recovery"`
}
