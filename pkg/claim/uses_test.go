package claim

import (
	"testing"
	"time"
)

func active(id, repo, agent string, paths, uses, not []string) Claim {
	now := time.Now()
	return Claim{ID: id, Repo: repo, Agent: agent, UserID: "u_" + agent, Paths: NormalizeAll(paths),
		NotPaths: NormalizeAll(not), Uses: NormalizeUses(uses), Created: now, Expires: now.Add(time.Hour)}
}

// The case overlaps cannot see: a refactor and the code that calls it, where
// only one side edits anything.
func TestARefactorFindsTheCodeThatUsesIt(t *testing.T) {
	now := time.Now()
	caller := active("b", "acme/app", "bo", []string{"app/checkout"}, []string{"lib/parser"}, nil)
	refactor := active("c", "acme/app", "cy", []string{"lib/parser/lexer.go"}, nil, nil)
	bystander := active("d", "acme/app", "di", []string{"docs"}, nil, nil)
	all := []Claim{caller, refactor, bystander}

	if got := FindOverlaps(all, "acme/app", refactor.Paths, now, map[string]bool{"c": true}, nil); len(got) != 0 {
		t.Fatalf("a path overlap was found where none exists: %+v", got)
	}
	deps := DependentsOf(all, refactor, now)
	if len(deps) != 1 || deps[0].User.ID != "b" {
		t.Fatalf("dependents of the refactor = %+v, want the caller", deps)
	}
	back := DependenciesOf(all, caller, now)
	if len(back) != 1 || back[0].Changer.ID != "c" {
		t.Fatalf("dependencies of the caller = %+v, want the refactor", back)
	}
	if n := len(LiveDependencies(all, now)); n != 1 {
		t.Fatalf("live dependencies = %d, want 1", n)
	}
}

// A library in one repository used from another is the usual shape.
func TestUsesCrossRepositories(t *testing.T) {
	now := time.Now()
	caller := active("b", "acme/app", "bo", []string{"src"}, []string{"acme/lib:pkg/parser/**"}, nil)
	sameNameOtherRepo := active("x", "acme/app", "xi", []string{"pkg/parser"}, nil, nil)
	refactor := active("c", "acme/lib", "cy", []string{"pkg/parser/lexer.go"}, nil, nil)
	all := []Claim{caller, sameNameOtherRepo, refactor}

	deps := DependenciesOf(all, caller, now)
	if len(deps) != 1 || deps[0].Changer.ID != "c" {
		t.Fatalf("got %+v, want only the claim in acme/lib", deps)
	}
}

// What the changer says it is not touching is not a dependency.
func TestNotPathsDischargeAConcreteUse(t *testing.T) {
	now := time.Now()
	caller := active("b", "r", "bo", []string{"app"}, []string{"lib/api.go"}, nil)
	refactor := active("c", "r", "cy", []string{"lib"}, nil, []string{"lib/api.go"})
	if deps := DependentsOf([]Claim{caller, refactor}, refactor, now); len(deps) != 0 {
		t.Fatalf("a disclaimed file still reported: %+v", deps)
	}
}

// An agent's own claims, released ones, and expired ones say nothing.
func TestDependenciesIgnoreSelfAndInactive(t *testing.T) {
	now := time.Now()
	mine := active("a1", "r", "bo", []string{"app"}, []string{"lib"}, nil)
	myOther := active("a2", "r", "bo", []string{"lib"}, nil, nil)
	gone := active("c", "r", "cy", []string{"lib"}, nil, nil)
	gone.Expires = now.Add(-time.Minute)
	if deps := DependenciesOf([]Claim{mine, myOther, gone}, mine, now); len(deps) != 0 {
		t.Fatalf("got %+v, want none", deps)
	}
}

// An amendment from a client that predates Uses must not erase them.
func TestAmendWithoutUsesKeepsThem(t *testing.T) {
	c := active("b", "r", "bo", []string{"app"}, []string{"lib"}, nil)
	amended := c
	amended.Uses = nil
	amended.What = "narrower"
	got := Fold([]Event{{Op: "claim", TS: c.Created, Claim: c}, {Op: "amend", TS: c.Created, Claim: amended}})
	if len(got) != 1 || len(got[0].Uses) == 0 || got[0].What != "narrower" {
		t.Fatalf("after amend: %+v", got)
	}
}
