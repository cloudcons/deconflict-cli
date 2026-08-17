package claim

import (
	"testing"
	"time"
)

func TestOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// identical and containment
		{"src/auth/token.go", "src/auth/token.go", true},
		{"src/auth/**", "src/auth/token.go", true},
		{"src/**", "src/auth/token.go", true},
		{"**", "anything/at/all.go", true},
		// `**` matches zero segments here, so a subtree claim also overlaps the
		// directory itself. That is the safe direction: overlap detection may
		// over-report, never under-report.
		{"src/auth/**", "src/auth", true},

		// disjoint
		{"src/auth/**", "src/billing/**", false},
		{"src/auth/token.go", "src/auth/session.go", false},
		{"src/auth/*.go", "src/auth/sub/deep.go", false},

		// single-segment stars
		{"src/*/token.go", "src/auth/token.go", true},
		{"src/*/token.go", "src/auth/sub/token.go", false},
		{"src/auth/*.go", "src/auth/token.go", true},
		{"src/auth/*.go", "src/auth/token.ts", false},
		{"src/auth/*_test.go", "src/auth/token_test.go", true},
		{"src/auth/*_test.go", "src/auth/token.go", false},

		// glob against glob — the case a match-a-file matcher cannot answer
		{"src/**/handler.go", "src/api/v1/handler.go", true},
		{"src/**/*.go", "**/auth/**", true},
		{"src/auth/**", "**/*.go", true},
		{"docs/**", "src/**", false},
		{"src/a*/x.go", "src/*b/x.go", true}, // e.g. src/ab/x.go
		{"src/a*/x.go", "src/b*/x.go", false},

		// ** matching zero segments
		{"src/**/x.go", "src/x.go", true},
		{"a/**/b/**/c", "a/b/c", true},
	}
	for _, c := range cases {
		if got := Overlap(c.a, c.b); got != c.want {
			t.Errorf("Overlap(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := Overlap(c.b, c.a); got != c.want {
			t.Errorf("Overlap(%q,%q) [reversed] = %v, want %v", c.b, c.a, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	got := Normalize("src/auth")
	want := []string{"src/auth", "src/auth/**"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Normalize(src/auth) = %v, want %v", got, want)
	}
	// a bare directory claim must cover files inside it
	if !Overlap(Normalize("src/auth")[1], "src/auth/token.go") {
		t.Error("bare directory claim does not cover its subtree")
	}
	// a file is not expanded into a subtree
	if g := Normalize("src/auth/middleware.go"); len(g) != 1 {
		t.Errorf("file path expanded as a directory: %v", g)
	}
	// a dotted directory is still a directory
	if g := Normalize(".github"); len(g) != 2 {
		t.Errorf(".github should expand to a subtree: %v", g)
	}
	if g := Normalize(".github/workflows/ci.yml"); len(g) != 1 {
		t.Errorf("dotted path with a file at the end expanded: %v", g)
	}
	// an explicit glob is left alone
	if g := Normalize("src/**/*.go"); len(g) != 1 || g[0] != "src/**/*.go" {
		t.Errorf("Normalize mangled an explicit glob: %v", g)
	}
	// leading ./ and trailing / are tolerated
	if g := Normalize("./src/auth/"); len(g) != 2 || g[0] != "src/auth" {
		t.Errorf("Normalize(./src/auth/) = %v", g)
	}
}

func TestPatternsOverlapReportsPairs(t *testing.T) {
	a := []string{"src/auth/**", "docs/auth.md"}
	b := []string{"src/**/token.go", "README.md"}
	hits := PatternsOverlap(a, b)
	if len(hits) != 1 {
		t.Fatalf("want 1 overlapping pair, got %d: %v", len(hits), hits)
	}
	if hits[0][0] != "src/auth/**" || hits[0][1] != "src/**/token.go" {
		t.Errorf("wrong pair reported: %v", hits[0])
	}
}

func TestCoversIsContainmentNotIntersection(t *testing.T) {
	cases := []struct {
		outer, inner string
		want         bool
		note         string
	}{
		// The bug this exists to prevent: a narrow "not touching" line must not
		// disclaim a broad pattern it merely intersects.
		{"src/auth/middleware.go", "src/**/*.go", false, "one file does not cover every go file"},
		{"src/billing", "src/billing/**", false, "the dir alone is not the subtree"},

		{"**", "anything/**", true, "** covers everything"},
		{"src/billing/**", "src/billing/invoice.go", true, "subtree covers a file under it"},
		{"src/billing/**", "src/billing/**/*.go", true, "subtree covers a pattern under it"},
		{"src/billing/**", "src/**/*.go", false, "the inner pattern escapes the prefix"},
		{"src/billing/**", "src/auth/**", false, "different subtree"},
		{"src/*/gen/**", "src/a/gen/x.go", true, "concrete inner path is matched exactly"},
		{"src/*/**", "src/a/b.go", true, "wildcard segment still matches a concrete path"},
		{"src/*/**", "src/*/deep/**", false, "conservative: wildcard prefix is not proven"},
	}
	for _, c := range cases {
		if got := Covers(c.outer, c.inner); got != c.want {
			t.Errorf("Covers(%q,%q) = %v, want %v — %s", c.outer, c.inner, got, c.want, c.note)
		}
	}
}

func TestFindOverlapsRespectsNotTouching(t *testing.T) {
	now := time.Now().UTC()
	held := []Claim{{
		ID: "x1", Repo: "r", Agent: "ana", What: "rework auth",
		Paths:    []string{"src/auth", "src/auth/**"},
		NotPaths: []string{"src/auth/middleware.go"},
		Created:  now.Add(-time.Minute), Expires: now.Add(time.Hour),
	}}

	// A file the claimant explicitly disclaimed is free — no warning.
	if got := FindOverlaps(held, "r", []string{"src/auth/middleware.go"}, now, nil, nil); len(got) != 0 {
		t.Errorf("warned about an explicitly disclaimed file: %+v", got)
	}
	// Any other file in the area still warns.
	if got := FindOverlaps(held, "r", []string{"src/auth/token.go"}, now, nil, nil); len(got) != 1 {
		t.Errorf("want a warning for a claimed file, got %d", len(got))
	}
	// A broad pattern is not disclaimed by one narrow exclusion — the queried
	// area is wider than what the claimant promised to avoid.
	if got := FindOverlaps(held, "r", []string{"src/**/*.go"}, now, nil, nil); len(got) != 1 {
		t.Errorf("a narrow NOT line wrongly suppressed a broad query: %+v", got)
	}
}
