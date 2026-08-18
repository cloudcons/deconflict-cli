// Package settings holds the operator-tunable knobs.
//
// Every field here is load-bearing on both sides: the server enforces what only
// it can (reaping, webhooks), and clients fetch the rest so that changing a
// default in the control panel actually changes what the next `deconflict claim`
// does. A settings page whose values only decorate the page it lives on is
// worse than no settings page.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Duration serialises as a human string ("8h", "45m") rather than nanoseconds,
// because these values are edited by hand in a text field.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(compact(time.Duration(d)))
}

// compact renders 8h rather than 8h0m0s. These strings are shown in a text
// field an operator edits by hand; Go's default is round-trippable but reads
// like machine output. Built from whole components rather than by trimming the
// string — trimming "0s" off "30s" leaves "3".
func compact(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	var b strings.Builder
	if h := d / time.Hour; h > 0 {
		fmt.Fprintf(&b, "%dh", h)
		d -= h * time.Hour
	}
	if m := d / time.Minute; m > 0 {
		fmt.Fprintf(&b, "%dm", m)
		d -= m * time.Minute
	}
	if s := d / time.Second; s > 0 {
		fmt.Fprintf(&b, "%ds", s)
	}
	return b.String()
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("bad duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*d = Duration(time.Duration(n))
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// Settings is the whole configuration surface.
type Settings struct {
	// DefaultLease is what a client uses when --ttl is not given.
	DefaultLease Duration `json:"default_lease"`

	// AutoReleaseStale closes claims that ran past their lease by StaleGrace.
	// An agent that died mid-task leaves a claim nobody will ever release, and
	// stale claims are how an advisory system stops being believed.
	AutoReleaseStale bool     `json:"auto_release_stale"`
	StaleGrace       Duration `json:"stale_grace"`

	// IgnorePaths never count as an overlap. Lockfiles, generated code and
	// snapshots collide constantly and mean nothing; without this the registry
	// cries wolf until people stop reading it.
	IgnorePaths []string `json:"ignore_paths"`

	// RequireWhy and RequireNot make the description fields mandatory at claim
	// time. `--not` is the field that lets another agent work alongside rather
	// than back off, and it is the one most often skipped.
	RequireWhy bool `json:"require_why"`
	RequireNot bool `json:"require_not"`

	// WebhookURL receives a JSON {"text": …} body when a new claim overlaps an
	// existing one — Slack- and Mattermost-shaped.
	WebhookURL       string `json:"webhook_url"`
	WebhookOnOverlap bool   `json:"webhook_on_overlap"`

	// MaxLease caps what a client may ask for. A 30-day claim is not a claim.
	MaxLease Duration `json:"max_lease"`
}

// Defaults are deliberately unopinionated except where a wrong default would
// quietly break the model: leases decay, and generated files never count.
func Defaults() Settings {
	return Settings{
		DefaultLease:     Duration(8 * time.Hour),
		AutoReleaseStale: true,
		StaleGrace:       Duration(2 * time.Hour),
		IgnorePaths: []string{
			"**/go.sum", "**/go.mod", "**/package-lock.json", "**/pnpm-lock.yaml",
			"**/yarn.lock", "**/Cargo.lock", "**/poetry.lock", "**/uv.lock",
			"**/*.snap", "**/__snapshots__/**", "**/*.generated.*", "**/vendor/**",
		},
		RequireWhy:       false,
		RequireNot:       false,
		WebhookOnOverlap: true,
		MaxLease:         Duration(72 * time.Hour),
	}
}

// Validate normalises and rejects values that would break the model.
func (s *Settings) Validate() error {
	if s.DefaultLease <= 0 {
		return fmt.Errorf("default lease must be positive")
	}
	if s.MaxLease <= 0 {
		s.MaxLease = Duration(72 * time.Hour)
	}
	if s.DefaultLease > s.MaxLease {
		return fmt.Errorf("default lease (%s) exceeds max lease (%s)", s.DefaultLease.Std(), s.MaxLease.Std())
	}
	if s.StaleGrace < 0 {
		return fmt.Errorf("stale grace cannot be negative")
	}
	if s.WebhookURL != "" && !strings.HasPrefix(s.WebhookURL, "http://") && !strings.HasPrefix(s.WebhookURL, "https://") {
		return fmt.Errorf("webhook url must be http(s)")
	}
	var clean []string
	for _, p := range s.IgnorePaths {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	s.IgnorePaths = clean
	return nil
}

// Provider is where settings live. Two implementations: a JSON file beside the
// event log (the single-machine and no-database case) and a row in Postgres.
//
// Load never returns an error. That is a deliberate constraint on every
// implementation: settings are read on the path of every client call, and a
// registry that cannot answer "what is the default lease" must fall back to
// defaults rather than fail the claim. Save is where problems are reported,
// because that is where a human is waiting for an answer.
type Provider interface {
	Load() Settings
	Save(Settings) error
	// Describe names the backing store for the startup banner.
	Describe() string
}

// Store persists settings beside the event log.
type Store struct {
	Path string
	mu   sync.RWMutex
}

func (st *Store) Describe() string { return st.Path }

func NewStore(logPath string) *Store {
	return &Store{Path: filepath.Join(filepath.Dir(logPath), "settings.json")}
}

func (st *Store) Load() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	b, err := os.ReadFile(st.Path)
	if err != nil {
		return Defaults()
	}
	s := Defaults()
	if err := json.Unmarshal(b, &s); err != nil {
		return Defaults()
	}
	return s
}

func (st *Store) Save(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(st.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Write-rename so a crash mid-write cannot leave unparseable settings, which
	// would silently revert the whole estate to defaults.
	tmp := st.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, st.Path)
}
