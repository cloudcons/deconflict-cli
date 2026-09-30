package claim

import (
	"strings"
	"testing"
	"time"
)

// Correcting a claim used to cost it its identity. The only way to widen a
// declared area was to release the claim and announce a new one, so one agent
// went through three ids in seven minutes correcting itself as the work taught
// it what it touched — and an audit trail of three claims reads as three
// pieces of work rather than one that was described badly at first.
func TestAmendCorrectsAClaimWithoutEndingIt(t *testing.T) {
	now := time.Now().UTC()
	original := Claim{
		ID: "c1", Repo: "acme/api", Agent: "ana/claude",
		Paths: []string{"internal/auth/**"}, NotPaths: []string{"internal/audit/**"},
		What: "rework refresh", Why: "HOME-412",
		Created: now.Add(-time.Hour), Expires: now.Add(time.Hour),
	}
	widened := original
	widened.Paths = []string{"internal/auth/**", "internal/cli/install_content.go"}
	widened.NotPaths = nil

	folded := Fold([]Event{
		{Op: "claim", TS: now.Add(-time.Hour), Claim: original},
		{Op: "amend", TS: now, Claim: widened},
	})
	if len(folded) != 1 {
		t.Fatalf("an amendment produced %d claims; it must correct the one that exists", len(folded))
	}
	got := folded[0]
	if got.ID != "c1" {
		t.Errorf("amend changed the claim id to %q", got.ID)
	}
	if len(got.Paths) != 2 {
		t.Errorf("declared area = %v, want the widened set", got.Paths)
	}
	// Everything the amendment did not speak to survives it.
	if got.Why != "HOME-412" || !got.Created.Equal(original.Created) || !got.Expires.Equal(original.Expires) {
		t.Errorf("amend disturbed the claim's history or lease: %+v", got)
	}
	// A correction is sometimes narrower than the guess, so exclusions are
	// replaced rather than merged.
	if len(got.NotPaths) != 0 {
		t.Errorf("amend kept exclusions the amendment dropped: %v", got.NotPaths)
	}
	if got.Released != nil {
		t.Error("amend released the claim; it is a correction, not a funeral")
	}
}

func TestAmendOfAnUnknownClaimIsIgnored(t *testing.T) {
	now := time.Now().UTC()
	folded := Fold([]Event{{Op: "amend", TS: now, Claim: Claim{ID: "ghost", Paths: []string{"**"}}}})
	if len(folded) != 0 {
		t.Fatalf("an amendment conjured a claim that was never announced: %+v", folded)
	}
}

// An overlap warning ends in what to do, addressed to the other agent, not in
// a referral to the operator.
func TestRenderEndsInWhatToDo(t *testing.T) {
	now := time.Now()
	other := Claim{ID: "c-ana", Agent: "ana/claude", What: "rework the contract", Created: now, Expires: now.Add(time.Hour)}
	got := Render([]Conflict{{Other: other, Pairs: [][2]string{{"telemetry/**", "telemetry/contract.yaml"}}}}, now)
	for _, want := range []string{"--to 'ana/claude'", "negotiate with c-ana", "NOT touching line covers"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "operator") {
		t.Errorf("warning still refers the overlap to the operator:\n%s", got)
	}
}
