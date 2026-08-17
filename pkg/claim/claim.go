// Package claim holds the data model for advisory (soft) intention claims.
//
// A claim is an announcement, not a lock. It never prevents an edit. Its whole
// job is to put "someone else is already in here, and here is what they are
// doing" in front of an agent early enough to matter. Two agents claiming the
// same area at the same instant is not a race to be resolved — it is precisely
// the signal the system exists to surface, so the log is append-only and needs
// no compare-and-swap anywhere.
package claim

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Claim is one agent's declared intention over an area of code.
type Claim struct {
	ID     string `json:"id"`
	Repo   string `json:"repo"`
	Agent  string `json:"agent"`
	Host   string `json:"host,omitempty"`
	Branch string `json:"branch,omitempty"`

	// HeadSHA is the branch tip when the claim was made. It is what lets
	// reconcile tell "not started" apart from "already landed" — both of
	// which look identical by commit count.
	HeadSHA string `json:"head_sha,omitempty"`

	// Paths are globs, repo-relative. NotPaths is the explicit "I am not
	// touching this" — the field that lets another agent proceed alongside
	// rather than back off entirely, and the one a human would never think
	// to write.
	Paths    []string `json:"paths"`
	NotPaths []string `json:"not_paths,omitempty"`

	What      string `json:"what"`
	Why       string `json:"why,omitempty"`
	Interface string `json:"interface,omitempty"`
	Priority  string `json:"priority,omitempty"`
	Task      string `json:"task,omitempty"`
	PR        string `json:"pr,omitempty"`

	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`

	Released      *time.Time `json:"released,omitempty"`
	ReleaseReason string     `json:"release_reason,omitempty"`
}

// Event is one line of the append-only log. State is the fold over events.
type Event struct {
	Op    string    `json:"op"` // claim | renew | release | annotate
	TS    time.Time `json:"ts"`
	Claim Claim     `json:"claim"`
}

// Active reports whether the claim is currently announcing anything.
func (c Claim) Active(now time.Time) bool {
	return c.Released == nil && now.Before(c.Expires)
}

// Stale reports a claim that ran out its lease without ever being released —
// almost always an agent that died mid-task. Advisory claims make this cheap:
// a stale claim is noise, not a deadlock. But noise compounds until the signal
// gets ignored, which is how a system like this dies, so it decays.
func (c Claim) Stale(now time.Time) bool {
	return c.Released == nil && !now.Before(c.Expires)
}

// Age renders how long ago the claim was made, the way a human reads it.
func (c Claim) Age(now time.Time) string { return humanize(now.Sub(c.Created)) }

// TTL renders remaining lease.
func (c Claim) TTL(now time.Time) string { return humanize(c.Expires.Sub(now)) }

func humanize(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// NewID returns a short, sortable-enough identifier.
func NewID() string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("c%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Fold replays the event log into current claim state, newest wins per ID.
func Fold(events []Event) []Claim {
	byID := map[string]Claim{}
	order := []string{}
	for _, e := range events {
		if e.Claim.ID == "" {
			continue
		}
		if _, seen := byID[e.Claim.ID]; !seen {
			order = append(order, e.Claim.ID)
		}
		cur, seen := byID[e.Claim.ID]
		switch e.Op {
		case "claim":
			byID[e.Claim.ID] = e.Claim
		case "renew":
			if seen {
				cur.Expires = e.Claim.Expires
				byID[e.Claim.ID] = cur
			}
		case "release":
			if seen {
				ts := e.TS
				cur.Released = &ts
				cur.ReleaseReason = e.Claim.ReleaseReason
				if e.Claim.PR != "" {
					cur.PR = e.Claim.PR
				}
				byID[e.Claim.ID] = cur
			}
		case "annotate":
			if seen {
				if e.Claim.PR != "" {
					cur.PR = e.Claim.PR
				}
				if e.Claim.What != "" {
					cur.What = e.Claim.What
				}
				byID[e.Claim.ID] = cur
			}
		}
	}
	out := make([]Claim, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Conflict is one claim overlapping another, with the specific patterns that
// collided so the report can say where rather than merely that.
type Conflict struct {
	Other Claim       `json:"other"`
	Pairs [][2]string `json:"pairs"`
}

// FindOverlaps returns active claims in the same repo whose paths intersect the
// given patterns, excluding the agent's own claim IDs.
func FindOverlaps(claims []Claim, repo string, paths []string, now time.Time, exclude map[string]bool) []Conflict {
	var out []Conflict
	for _, c := range claims {
		if exclude[c.ID] || !c.Active(now) {
			continue
		}
		if repo != "" && c.Repo != "" && c.Repo != repo {
			continue
		}
		pairs := PatternsOverlap(paths, c.Paths)
		if len(pairs) == 0 {
			continue
		}
		out = append(out, Conflict{Other: c, Pairs: pairs})
	}
	return out
}

// Render formats conflicts for an agent to read. Terse on purpose: this lands
// in a context window, at the start of every task, for every agent.
func Render(cs []Conflict, now time.Time) string {
	if len(cs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d active claim(s) overlap this area:\n", len(cs))
	for _, c := range cs {
		o := c.Other
		fmt.Fprintf(&b, "\n  [%s] %s — %s ago, lease %s left\n", o.ID, o.Agent, o.Age(now), o.TTL(now))
		fmt.Fprintf(&b, "    what: %s\n", o.What)
		if o.Why != "" {
			fmt.Fprintf(&b, "    why:  %s\n", o.Why)
		}
		if o.Interface != "" {
			fmt.Fprintf(&b, "    interface changes: %s\n", o.Interface)
		}
		if len(o.NotPaths) > 0 {
			fmt.Fprintf(&b, "    NOT touching: %s\n", strings.Join(o.NotPaths, ", "))
		}
		if o.Branch != "" {
			fmt.Fprintf(&b, "    branch: %s", o.Branch)
			if o.PR != "" {
				fmt.Fprintf(&b, "  pr: %s", o.PR)
			}
			b.WriteString("\n")
		}
		var ps []string
		for _, p := range c.Pairs {
			ps = append(ps, fmt.Sprintf("%s ~ %s", p[0], p[1]))
		}
		fmt.Fprintf(&b, "    overlap: %s\n", strings.Join(ps, ", "))
	}
	b.WriteString("\nThis is advisory — it does not block you. Read the 'NOT touching' line " +
		"before deciding to back off; the area may be safe to work alongside. If you " +
		"proceed into a genuine overlap, say so to your operator and note it on the PR.\n")
	return b.String()
}
