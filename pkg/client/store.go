// Package store persists the append-only claim log.
//
// Two backends, same interface: a local file (one machine, many sessions) and
// an HTTP client (many machines, one team). Because claims are advisory the
// store needs no transactions and no locking beyond an atomic append — a lost
// race produces two overlapping claims, which is a report, not a corruption.
package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudcons/deconflict/internal/claim"
	"github.com/cloudcons/deconflict/internal/db"
	"github.com/cloudcons/deconflict/internal/settings"
)

type Store interface {
	Append(claim.Event) error
	Events() ([]claim.Event, error)
	// Settings lets a client honour operator-set defaults — the lease length,
	// the ignore list, whether --why is mandatory. Without this the control
	// panel would only configure the server, and the knobs that matter are the
	// ones that change what the next `deconflict claim` does.
	Settings() settings.Settings
	Describe() string
}

// CtxStore is optionally implemented by backends that can honour cancellation.
//
// It is an extension rather than a change to Store because the CLI has no
// context to pass and never wants one — a claim is a two-second command with a
// process for a lifetime. The server does, and a dashboard poll abandoned by a
// closed browser tab should not keep a database connection busy.
type CtxStore interface {
	Store
	AppendCtx(context.Context, claim.Event) error
	EventsCtx(context.Context) ([]claim.Event, error)
}

// EventsWith reads the log, honouring ctx when the backend can.
func EventsWith(ctx context.Context, st Store) ([]claim.Event, error) {
	if cs, ok := st.(CtxStore); ok {
		return cs.EventsCtx(ctx)
	}
	return st.Events()
}

// AppendWith writes one event, honouring ctx when the backend can.
func AppendWith(ctx context.Context, st Store, e claim.Event) error {
	if cs, ok := st.(CtxStore); ok {
		return cs.AppendCtx(ctx, e)
	}
	return st.Append(e)
}

// Open resolves a store from a DSN, falling back to the local default.
//
//	file:/path/to/claims.jsonl   (or a bare path)
//	http://host:port             (+ DECONFLICT_TOKEN for a bearer token)
//	postgres://user@host/db      (direct, for the server and for operators)
func Open(dsn string) (Store, error) { return OpenCtx(context.Background(), dsn) }

// OpenCtx is Open with a context, which the Postgres backend needs in order to
// connect and migrate.
func OpenCtx(ctx context.Context, dsn string) (Store, error) {
	if dsn == "" {
		dsn = os.Getenv("DECONFLICT_STORE")
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
		base := strings.TrimRight(u.String(), "/")
		return &HTTPStore{Base: base, Token: TokenFor(base)}, nil
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		// A direct database connection is the server's path, and an escape
		// hatch for an operator debugging one. It is not how agents should
		// reach the registry: a client with the database credentials bypasses
		// every permission check in the server, so `deconflict claim` against this
		// DSN records an unattributed claim on purpose.
		pool, err := db.Open(ctx, dsn)
		if err != nil {
			return nil, err
		}
		cfg, err := settings.NewPGStore(ctx, pool)
		if err != nil {
			pool.Close()
			return nil, err
		}
		return NewPGStore(pool, cfg), nil
	case strings.HasPrefix(dsn, "file:"):
		return &FileStore{Path: strings.TrimPrefix(dsn, "file:")}, nil
	default:
		return &FileStore{Path: dsn}, nil
	}
}

// DefaultPath is the per-machine log, which is all a single box needs.
func DefaultPath() string {
	if p := os.Getenv("DECONFLICT_FILE"); p != "" {
		return p
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "deconflict", "claims.jsonl")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "deconflict", "claims.jsonl")
}
