// Package store persists the append-only claim log.
//
// Two backends, same interface: a local file (one machine, many sessions) and
// an HTTP client (many machines, one team). Because claims are advisory the
// store needs no transactions and no locking beyond an atomic append — a lost
// race produces two overlapping claims, which is a report, not a corruption.
package store

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudops/agentclaims/internal/claim"
	"github.com/cloudops/agentclaims/internal/settings"
)

type Store interface {
	Append(claim.Event) error
	Events() ([]claim.Event, error)
	// Settings lets a client honour operator-set defaults — the lease length,
	// the ignore list, whether --why is mandatory. Without this the control
	// panel would only configure the server, and the knobs that matter are the
	// ones that change what the next `claims claim` does.
	Settings() settings.Settings
	Describe() string
}

// Open resolves a store from a DSN, falling back to the local default.
//
//	file:/path/to/claims.jsonl   (or a bare path)
//	http://host:port             (+ AGENTCLAIMS_TOKEN for a bearer token)
func Open(dsn string) (Store, error) {
	if dsn == "" {
		dsn = os.Getenv("AGENTCLAIMS_STORE")
	}
	if dsn == "" {
		dsn = "file:" + DefaultPath()
	}
	switch {
	case strings.HasPrefix(dsn, "http://"), strings.HasPrefix(dsn, "https://"):
		u, err := url.Parse(dsn)
		if err != nil {
			return nil, fmt.Errorf("bad store url %q: %w", dsn, err)
		}
		return &HTTPStore{Base: strings.TrimRight(u.String(), "/"), Token: os.Getenv("AGENTCLAIMS_TOKEN")}, nil
	case strings.HasPrefix(dsn, "file:"):
		return &FileStore{Path: strings.TrimPrefix(dsn, "file:")}, nil
	default:
		return &FileStore{Path: dsn}, nil
	}
}

// DefaultPath is the per-machine log, which is all a single box needs.
func DefaultPath() string {
	if p := os.Getenv("AGENTCLAIMS_FILE"); p != "" {
		return p
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "agentclaims", "claims.jsonl")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "agentclaims", "claims.jsonl")
}
