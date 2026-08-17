package settings

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDurationRoundTrip(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{8 * time.Hour, "8h"},
		{72 * time.Hour, "72h"},
		{90 * time.Minute, "1h30m"},
		{30 * time.Second, "30s"}, // the trim-the-string bug produced "3"
		{0, "0s"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1h2m3s"},
	}
	for _, c := range cases {
		b, err := json.Marshal(Duration(c.in))
		if err != nil {
			t.Fatal(err)
		}
		var got string
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("marshal %s = %q, want %q", c.in, got, c.want)
		}
		// Everything we emit must parse back to the same value, or the
		// settings file stops being editable by hand.
		var back Duration
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal %q: %v", got, err)
		}
		if back.Std() != c.in {
			t.Errorf("round trip %s -> %q -> %s", c.in, got, back.Std())
		}
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	s := Defaults()
	s.DefaultLease = Duration(200 * time.Hour) // longer than MaxLease
	if err := s.Validate(); err == nil {
		t.Error("expected default lease > max lease to be rejected")
	}
	s = Defaults()
	s.WebhookURL = "ftp://nope"
	if err := s.Validate(); err == nil {
		t.Error("expected a non-http webhook url to be rejected")
	}
	s = Defaults()
	s.IgnorePaths = []string{" ", "", "**/go.sum"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(s.IgnorePaths) != 1 {
		t.Errorf("blank ignore lines survived: %#v", s.IgnorePaths)
	}
}
