package protocol

import "testing"

func TestParseAddress(t *testing.T) {
	good := map[string]Address{
		"auth/claude-1":               {Agent: "auth/claude-1"},
		"@room":                       {Group: "room"},
		"@all":                        {Group: "all"},
		"@delegator":                  {Group: "delegator"},
		"@runtime:codex":              {Group: "runtime", Runtime: "codex"},
		"@paths:api:db/migrations/**": {Group: "paths", Repo: "api", Glob: "db/migrations/**"},
	}
	for in, want := range good {
		got, err := ParseAddress(in)
		if err != nil || got != want {
			t.Errorf("ParseAddress(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "@", "@room:x", "@runtime", "@paths:api", "@paths::x", "@everyone"} {
		if _, err := ParseAddress(in); err == nil {
			t.Errorf("ParseAddress(%q) accepted", in)
		}
	}
}

func TestPostKindsAgreeWithTheirReplies(t *testing.T) {
	for _, k := range []PostKind{KindAnswer, KindOffer} {
		if !k.Reply() || k.Root() || !k.RepliesTo().Root() {
			t.Errorf("%s must be a reply to a root kind", k)
		}
	}
	if KindUpdate.Reply() || KindUpdate.Root() {
		t.Error("clients must not be able to write updates")
	}
}
