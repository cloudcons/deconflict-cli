package claim

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Dependency is one claim changing ground another claim uses.
//
// It is the relation an overlap cannot express. Two agents editing the same
// file find each other; an agent refactoring a library and an agent calling it
// never do, because only one of them edits anything there. Here the caller has
// said what it stands on, and the refactor is told before it lands.
type Dependency struct {
	// User declared the ground in its Uses.
	User Claim `json:"user"`
	// Changer claims that ground in its Paths.
	Changer Claim `json:"changer"`
	// Pairs are (the user's glob, the changer's glob) that met.
	Pairs [][2]string `json:"pairs"`
}

// SplitUse separates an entry of Uses into the repository it names and its
// glob. An entry with no "<repo>:" prefix is in home, the claim's own repo.
func SplitUse(use, home string) (repo, glob string) {
	if i := strings.Index(use, ":"); i > 0 {
		return strings.TrimSpace(use[:i]), strings.TrimSpace(use[i+1:])
	}
	return home, strings.TrimSpace(use)
}

// NormalizeUses normalizes the glob half of each entry the way paths are,
// keeping any repository prefix.
func NormalizeUses(in []string) []string {
	var out []string
	for _, u := range in {
		repo, glob := SplitUse(u, "")
		for _, g := range Normalize(glob) {
			if repo != "" {
				g = repo + ":" + g
			}
			out = append(out, g)
		}
	}
	return out
}

// sameRepo matches the way FindOverlaps does: an unknown repository on either
// side matches, because a local file store has none and failing toward a
// warning is the side this system errs on.
func sameRepo(a, b string) bool {
	return a == "" || b == "" || strings.EqualFold(a, b)
}

// usePairs is where user's Uses meet changer's Paths, less anything the changer
// explicitly said it is not touching.
func usePairs(user, changer Claim) [][2]string {
	var pairs [][2]string
	for _, u := range user.Uses {
		repo, glob := SplitUse(u, user.Repo)
		if !sameRepo(repo, changer.Repo) {
			continue
		}
		for _, p := range PatternsOverlap([]string{glob}, changer.Paths) {
			// A concrete used file the changer disclaimed is safe; a glob is
			// kept, since containment cannot be proved for it.
			if coveredByAny(changer.NotPaths, p[0]) {
				continue
			}
			pairs = append(pairs, [2]string{u, p[1]})
		}
	}
	return pairs
}

func related(a, b Claim) bool {
	// The same agent under the same account depending on its own change is
	// not news to it.
	// A claim being written has no UserID yet (the registry fills it in), so
	// an unknown account on either side does not make the same agent a stranger.
	return a.ID == b.ID || (a.Agent == b.Agent && (a.UserID == b.UserID || a.UserID == "" || b.UserID == ""))
}

// DependenciesOf is every active claim changing what c uses — what c should
// hear about before it builds on ground that is moving.
func DependenciesOf(claims []Claim, c Claim, now time.Time) []Dependency {
	var out []Dependency
	for _, o := range claims {
		if related(c, o) || !o.Active(now) {
			continue
		}
		if pairs := usePairs(c, o); len(pairs) > 0 {
			out = append(out, Dependency{User: c, Changer: o, Pairs: pairs})
		}
	}
	return out
}

// DependentsOf is every active claim that uses what c changes — who c would
// break, and who should hear about its interface before it lands.
func DependentsOf(claims []Claim, c Claim, now time.Time) []Dependency {
	var out []Dependency
	for _, o := range claims {
		if related(c, o) || !o.Active(now) {
			continue
		}
		if pairs := usePairs(o, c); len(pairs) > 0 {
			out = append(out, Dependency{User: o, Changer: c, Pairs: pairs})
		}
	}
	return out
}

// LiveDependencies is every current dependency between active claims, for the
// dashboard. Unlike overlaps it crosses repositories.
func LiveDependencies(claims []Claim, now time.Time) []Dependency {
	var active []Claim
	for _, c := range claims {
		if c.Active(now) {
			active = append(active, c)
		}
	}
	var out []Dependency
	for _, user := range active {
		if len(user.Uses) == 0 {
			continue
		}
		for _, changer := range active {
			if related(user, changer) {
				continue
			}
			if pairs := usePairs(user, changer); len(pairs) > 0 {
				out = append(out, Dependency{User: user, Changer: changer, Pairs: pairs})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Changer.Created.After(out[j].Changer.Created) })
	return out
}

func pairText(ps [][2]string) string {
	var s []string
	for _, p := range ps {
		s = append(s, p[0]+" ~ "+p[1])
	}
	return strings.Join(s, ", ")
}

func who(c Claim) string {
	if c.UserLogin != "" && !strings.Contains(c.Agent, c.UserLogin) {
		return c.Agent + " for " + c.UserLogin
	}
	return c.Agent
}

// RenderDependencies formats both directions for an agent: who is changing
// what it uses, and who uses what it is changing. Empty when there is neither.
func RenderDependencies(dependencies, dependents []Dependency, now time.Time) string {
	if len(dependencies) == 0 && len(dependents) == 0 {
		return ""
	}
	var b strings.Builder
	if len(dependencies) > 0 {
		fmt.Fprintf(&b, "%d active claim(s) are changing what you use:\n", len(dependencies))
		for _, d := range dependencies {
			o := d.Changer
			fmt.Fprintf(&b, "\n  [%s] %s in %s — lease %s left\n", o.ID, who(o), o.Repo, o.TTL(now))
			fmt.Fprintf(&b, "    what: %s\n", o.What)
			if o.Interface != "" {
				fmt.Fprintf(&b, "    interface changes: %s\n", o.Interface)
			} else {
				b.WriteString("    interface changes: not stated — ask before building on it\n")
			}
			fmt.Fprintf(&b, "    you use: %s\n", pairText(d.Pairs))
		}
		b.WriteString("\n")
	}
	if len(dependents) > 0 {
		fmt.Fprintf(&b, "%d active claim(s) use what you are changing:\n", len(dependents))
		for _, d := range dependents {
			o := d.User
			fmt.Fprintf(&b, "\n  [%s] %s in %s — lease %s left\n", o.ID, who(o), o.Repo, o.TTL(now))
			fmt.Fprintf(&b, "    what: %s\n", o.What)
			fmt.Fprintf(&b, "    they use: %s\n", pairText(d.Pairs))
		}
		b.WriteString("\nState what callers will notice with `deconflict amend --interface '…'` — it is " +
			"the line they are shown. If the change breaks them, agree an order instead of racing: " +
			"`deconflict negotiate request` makes them participants, with their work waiting on your " +
			"published interface.\n")
	}
	b.WriteString("\nThis is advisory — nothing is blocked.\n")
	return b.String()
}
