package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The session-start hook mentions a newer release, because nobody runs
// `deconflict update` on a schedule and an agent working from last month's
// skill is the failure this whole client exists to prevent. It costs one
// request a day at most, and never more than updateCheckTimeout of a session's
// start: a hook that makes an agent wait is a hook somebody uninstalls.

var updateCheckTimeout = 800 * time.Millisecond

const updateCheckEvery = 24 * time.Hour

type updateCache struct {
	CheckedAt  time.Time `json:"checked_at"`
	Latest     string    `json:"latest,omitempty"`
	NotifiedAt time.Time `json:"notified_at,omitempty"`
}

func updateCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "deconflict", "latest.json")
}

// updateContext is the session-start line saying a newer release exists, or
// "" — which is also the answer to every failure, since a missed nudge costs
// nothing and a hook error costs the session its context.
func updateContext(now time.Time) string {
	if os.Getenv("DECONFLICT_NO_UPDATE_CHECK") == "1" {
		return ""
	}
	current := clientVersion()
	if _, ok := semver(current); !ok {
		return "" // a development build has nothing to be behind
	}
	path := updateCachePath()
	if path == "" {
		return ""
	}
	var c updateCache
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	dirty := false
	if now.Sub(c.CheckedAt) >= updateCheckEvery {
		// A failed check is recorded as a check, so an offline machine asks
		// once a day rather than at every session start.
		if tag, err := latestTag(updateCheckTimeout); err == nil {
			c.Latest = tag
		}
		c.CheckedAt, dirty = now, true
	}
	msg := ""
	if c.Latest != "" && newer(c.Latest, current) && now.Sub(c.NotifiedAt) >= updateCheckEvery {
		msg = fmt.Sprintf("deconflict %s is available (this is %s) — run `deconflict update`.",
			strings.TrimPrefix(c.Latest, "v"), strings.TrimPrefix(current, "v"))
		c.NotifiedAt, dirty = now, true
	}
	if dirty {
		if b, err := json.Marshal(c); err == nil {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, b, 0o644)
		}
	}
	return msg
}
