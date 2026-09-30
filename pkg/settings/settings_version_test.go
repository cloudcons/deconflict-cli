package settings

import "testing"

func TestClientVersionMustBeARelease(t *testing.T) {
	for in, want := range map[string]string{"": "", "0.1.18": "0.1.18", "v0.1.18": "0.1.18", " 1.2.3 ": "1.2.3"} {
		s := Defaults()
		s.ClientVersion = in
		if err := s.Validate(); err != nil || s.ClientVersion != want {
			t.Errorf("%q: got %q, %v; want %q", in, s.ClientVersion, err, want)
		}
	}
	for _, bad := range []string{"latest", "0.1", "0.1.18-rc1", "https://example.com/deconflict"} {
		s := Defaults()
		s.ClientVersion = bad
		if err := s.Validate(); err == nil {
			t.Errorf("%q was accepted as a client version", bad)
		}
	}
}
