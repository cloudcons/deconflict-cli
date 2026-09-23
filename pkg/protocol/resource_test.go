package protocol

import "testing"

func TestContends(t *testing.T) {
	for _, c := range []struct {
		a, b LockMode
		want bool
	}{
		{Shared, Shared, false},
		{Shared, Exclusive, true},
		{Exclusive, Shared, true},
		{Exclusive, Exclusive, true},
	} {
		if got := Contends(c.a, c.b); got != c.want {
			t.Errorf("Contends(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestNormalizeResourceName(t *testing.T) {
	for in, want := range map[string]string{" Staging ": "staging", "orders-db": "orders-db", "eu.staging_2": "eu.staging_2"} {
		if got, err := NormalizeResourceName(in); err != nil || got != want {
			t.Errorf("NormalizeResourceName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// A slash would split the URL path the name travels in; the rest would not
	// survive a shell unquoted.
	for _, bad := range []string{"", "-staging", "eu/staging", "staging db", "stag*"} {
		if _, err := NormalizeResourceName(bad); err == nil {
			t.Errorf("NormalizeResourceName(%q) accepted", bad)
		}
	}
}
