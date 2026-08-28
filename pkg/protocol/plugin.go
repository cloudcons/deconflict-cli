package protocol

import (
	"encoding/json"
	"strings"
	"time"
)

// WorkItem is a ticket, story, issue or card — whatever the tracker calls it —
// reduced to the fields a claim actually displays.
type WorkItem struct {
	Ref      string          `json:"ref"`
	Title    string          `json:"title"`
	URL      string          `json:"url"`
	State    string          `json:"state"`
	Assignee string          `json:"assignee,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// Config is one configured integration as a provider sees it. Secret is
// decrypted at the point of use and is never serialised back to a client.
type Config struct {
	ID       string            `json:"id"`
	Provider string            `json:"provider"`
	Name     string            `json:"name"`
	Enabled  bool              `json:"enabled"`
	Repos    []string          `json:"repos"`
	Options  map[string]string `json:"options"`
	Secret   string            `json:"-"`
}

// Option reads a configured value, trimmed. Providers use this rather than
// indexing the map so that a value pasted with a trailing space — which is most
// of them — does not produce a 404 nobody can explain.
func (c Config) Option(key string) string { return strings.TrimSpace(c.Options[key]) }

// AppliesTo reports whether this integration covers a repository. An empty repo
// list means every repository, which is the right default for the common case
// of one team, one tracker.
func (c Config) AppliesTo(repo string) bool {
	if len(c.Repos) == 0 {
		return true
	}
	for _, r := range c.Repos {
		if strings.EqualFold(strings.TrimSpace(r), repo) {
			return true
		}
	}
	return false
}

// Field describes one configuration input, so the control panel can render a
// provider's form without knowing anything about that provider.
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Help        string `json:"help,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Required    bool   `json:"required,omitempty"`
	// Secret fields are write-only: stored encrypted, never sent back, and
	// rendered as a password input that shows "unchanged" once set.
	Secret bool `json:"secret,omitempty"`
}

// Capability is what a provider can do. The UI hides controls for the ones a
// provider lacks rather than offering a button that returns an error.
type Capability string

const (
	CapResolve Capability = "resolve"
	CapSearch  Capability = "search"
	CapNotify  Capability = "notify"
)

// Descriptor is a provider's self-description.
type Descriptor struct {
	Provider     string       `json:"provider"`
	Title        string       `json:"title"`
	Blurb        string       `json:"blurb"`
	Fields       []Field      `json:"fields"`
	Capabilities []Capability `json:"capabilities"`
	// RefHint tells a human what to put in `--task`, which is the single most
	// common thing to get wrong.
	RefHint string `json:"ref_hint,omitempty"`
}

func (d Descriptor) Has(c Capability) bool {
	for _, x := range d.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// Integration is a configured provider as the API and the control panel see it.
// The secret is never a field here — only the fact that one is set — so there
// is no path by which a handler can accidentally serialise a credential.
type Integration struct {
	Config
	HasSecret bool      `json:"has_secret"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// Delivery is one queued outbound notification.
type Delivery struct {
	ID            int64           `json:"id"`
	OrgID         string          `json:"-"`
	IntegrationID string          `json:"integration_id"`
	Event         string          `json:"event"`
	ClaimID       string          `json:"claim_id"`
	Ref           string          `json:"ref"`
	Payload       json.RawMessage `json:"-"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	Status        string          `json:"status"`
	LastError     string          `json:"last_error,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// SearchHit is one search result. The integration name rides along so a person
// choosing from a list can tell two trackers' identically-titled tickets apart.
type SearchHit struct {
	WorkItem
	Integration string `json:"integration"`
	Provider    string `json:"provider"`
}
