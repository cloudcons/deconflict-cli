package protocol

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Shared resources are the things a team contends for that are not a path in a
// repository: a staging environment, a database, a deploy slot, a test tenant.
// Two agents cannot collide in `src/auth` if they are in different
// repositories, but they can both deploy to staging from anywhere, so a
// resource belongs to the organization rather than to a repository.
//
// A lock on one is soft, in exactly the sense a claim is. Taking it always
// succeeds; what comes back is who else already holds it. The registry never
// refuses the second agent, because the registry being wrong, down, or stale
// must never be the reason an agent cannot deploy — and because two agents
// wanting staging at once is the fact worth surfacing, not a race to settle.

// LockMode says how a holder is using the resource.
type LockMode string

const (
	// Shared is using the resource alongside others: running tests against
	// staging, reading from a database.
	Shared LockMode = "shared"
	// Exclusive is needing it alone: deploying to staging, running a
	// migration, resetting fixtures.
	Exclusive LockMode = "exclusive"
)

// Valid reports whether m is a mode this protocol defines.
func (m LockMode) Valid() bool { return m == Shared || m == Exclusive }

// Contends reports whether two holders in these modes get in each other's way.
// Two shared holders do not; anything involving an exclusive one does.
func Contends(a, b LockMode) bool { return a == Exclusive || b == Exclusive }

// Resource is one entry in an organization's catalog of shared things.
//
// The catalog is explicit rather than created on first lock. A lock is keyed on
// a name an agent typed, and a lock on "stagin" protects nothing while looking
// exactly like one that does; refusing an unknown name and listing the known
// ones is what makes a typo visible.
type Resource struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind,omitempty"` // env | database | service | … free-form
	Description string    `json:"description,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// Locks is the active locks only. Released and expired ones are history,
	// and a list that mixed them would make "who holds staging" a filtering
	// exercise.
	Locks []ResourceLock `json:"locks"`
}

// ResourceInput adds a resource to the catalog.
type ResourceInput struct {
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
}

// ResourceLock is one agent's declared use of a resource.
//
// AgentID is the self-reported label, as on a claim; DelegatedBy is the human
// account the credential belongs to, filled in by the server. Releasing a lock
// is gated on the second, never the first.
type ResourceLock struct {
	ID             string     `json:"id"`
	ResourceID     string     `json:"resource_id"`
	Resource       string     `json:"resource"`
	AgentID        string     `json:"agent_id"`
	Mode           LockMode   `json:"mode"`
	Reason         string     `json:"reason,omitempty"`
	DelegatedBy    string     `json:"delegated_by,omitempty"`
	DelegatedLogin string     `json:"delegated_login,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	ReleaseReason  string     `json:"release_reason,omitempty"`
}

// Active reports whether the lock is still announcing anything.
func (l ResourceLock) Active(now time.Time) bool {
	return l.ReleasedAt == nil && now.Before(l.ExpiresAt)
}

// LockInput takes a lock. TTL is capped by the organization's maximum lease and
// defaults to its default lease, the same knobs a claim obeys.
type LockInput struct {
	AgentID string   `json:"agent_id"`
	Mode    LockMode `json:"mode,omitempty"` // defaults to exclusive
	Reason  string   `json:"reason,omitempty"`
	TTL     string   `json:"ttl,omitempty"`
}

// LockResult is what taking a lock answers: the lock, and every active lock it
// contends with. An empty Conflicts is the all-clear; a non-empty one is not a
// refusal.
type LockResult struct {
	Lock      ResourceLock   `json:"lock"`
	Conflicts []ResourceLock `json:"conflicts"`
}

// UnlockInput releases the caller's own active locks on a resource. AgentID
// narrows it to one agent's; empty releases every lock the caller's account
// holds there, which is what a human cleaning up after a dead session wants.
type UnlockInput struct {
	AgentID string `json:"agent_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// RenewInput extends a lock.
type RenewInput struct {
	TTL string `json:"ttl,omitempty"`
}

var resourceName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// NormalizeResourceName lower-cases and validates a resource name. Names are
// what agents type, so they are case-insensitive and limited to characters that
// survive a shell and a URL path unquoted.
func NormalizeResourceName(name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if !resourceName.MatchString(n) {
		return "", errors.New("resource name must be 1-64 characters of a-z, 0-9, '.', '_', or '-', starting with a letter or digit")
	}
	return n, nil
}
