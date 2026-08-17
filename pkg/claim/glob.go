package claim

import "strings"

// Pattern intersection: do two path globs share at least one path?
//
// This is deliberately NOT "does this glob match this file". Claims are
// declared as globs before the files are known, so overlap detection has to
// compare glob against glob without consulting a working tree — the server
// holding the claims does not have one.
//
// Supported: literal segments, `*` (within a segment), `**` (zero or more
// segments). Not supported: `?`, character classes. Both are rare in a
// hand-written claim and their absence fails toward reporting an overlap,
// never toward hiding one.

// Normalize expands a user-written path into the patterns it should claim.
//
// A bare path with no glob metacharacters is expanded to itself *and*
// everything under it: `src/auth` claims the file `src/auth` and the tree
// `src/auth/**`. Engineers write the directory and mean the subtree; making
// them type `/**` is a rule they will forget exactly once.
func Normalize(p string) []string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	if strings.ContainsAny(p, "*") {
		return []string{p}
	}
	if looksLikeFile(p) {
		return []string{p}
	}
	return []string{p, p + "/**"}
}

// looksLikeFile guesses from the name alone, because the claim may be written
// for a path that does not exist yet and the registry has no working tree to
// consult. A dot inside the last segment means a file; a leading dot does not
// (`.github` is a directory).
func looksLikeFile(p string) bool {
	last := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		last = p[i+1:]
	}
	return strings.Contains(strings.TrimPrefix(last, "."), ".")
}

// NormalizeAll expands and de-duplicates a list of user-written paths.
func NormalizeAll(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		for _, n := range Normalize(p) {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// PatternsOverlap reports whether any pattern in a shares a path with any
// pattern in b.
func PatternsOverlap(a, b []string) []([2]string) {
	var hits [][2]string
	for _, pa := range a {
		for _, pb := range b {
			if Overlap(pa, pb) {
				hits = append(hits, [2]string{pa, pb})
			}
		}
	}
	return hits
}

// Overlap reports whether two path globs can both match the same path.
func Overlap(a, b string) bool {
	return segsOverlap(split(a), split(b))
}

func split(p string) []string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func segsOverlap(a, b []string) bool {
	switch {
	case len(a) == 0 && len(b) == 0:
		return true
	case len(a) == 0:
		return allDoubleStar(b)
	case len(b) == 0:
		return allDoubleStar(a)
	}
	if a[0] == "**" {
		// `**` matches zero segments, or one-or-more from b.
		return segsOverlap(a[1:], b) || segsOverlap(a, b[1:])
	}
	if b[0] == "**" {
		return segsOverlap(a, b[1:]) || segsOverlap(a[1:], b)
	}
	return segOverlap(a[0], b[0]) && segsOverlap(a[1:], b[1:])
}

func allDoubleStar(s []string) bool {
	for _, x := range s {
		if x != "**" {
			return false
		}
	}
	return true
}

// segOverlap: can two single-segment globs (literals plus `*`) match the same
// string? Same shape as the segment-list version, one level down.
func segOverlap(a, b string) bool {
	switch {
	case a == "" && b == "":
		return true
	case a == "":
		return allStars(b)
	case b == "":
		return allStars(a)
	}
	if a[0] == '*' {
		return segOverlap(a[1:], b) || segOverlap(a, b[1:])
	}
	if b[0] == '*' {
		return segOverlap(a, b[1:]) || segOverlap(a[1:], b)
	}
	if a[0] != b[0] {
		return false
	}
	return segOverlap(a[1:], b[1:])
}

func allStars(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '*' {
			return false
		}
	}
	return true
}

// Match reports whether a concrete path is matched by a glob. Used by the
// hooks, which do know a real filename.
func Match(pattern, path string) bool {
	return Overlap(pattern, path)
}

// Covers reports whether *every* path matched by inner is also matched by
// outer — containment, not intersection.
//
// The distinction is load-bearing and easy to get wrong: `src/auth/token.go`
// intersects `src/**/*.go`, but it does not cover it. Reading intersection as
// coverage would let one narrow "not touching" line mark an entire overlap as
// benign, which is exactly the warning the tool exists to raise.
//
// This is deliberately conservative and incomplete: it answers yes only in the
// cases it can be sure of, and an unsure answer is "not covered", which keeps
// the overlap visible. Over-warning is recoverable; under-warning is the
// failure this tool exists to prevent.
func Covers(outer, inner string) bool {
	if outer == "**" {
		return true
	}
	if !strings.ContainsAny(inner, "*") {
		// A concrete path: plain matching is exact.
		return Overlap(outer, inner)
	}
	// Both are patterns. The only case we can be certain about is a subtree
	// claim (`prefix/**`) against a pattern living wholly under that same
	// literal prefix.
	os, is := split(outer), split(inner)
	if len(os) < 2 || os[len(os)-1] != "**" {
		return false
	}
	prefix := os[:len(os)-1]
	if len(is) < len(prefix) {
		return false
	}
	for i, seg := range prefix {
		if strings.ContainsAny(seg, "*") || seg != is[i] {
			return false
		}
	}
	return true
}
