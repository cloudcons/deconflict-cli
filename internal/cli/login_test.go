package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// loginServer approves on the first poll and binds the token to boundSlug.
func loginServer(t *testing.T, boundSlug string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/cli-auth":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dev1", "user_code": "ABCD-EFGH", "verification_url": srv.URL + "/cli-auth", "interval": 1})
		case r.URL.Path == "/v1/cli-auth":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "token": "tok_1"})
		case r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{"login": "ana"}, "org_id": "org_x",
				"org": map[string]string{"id": "org_x", "slug": boundSlug, "name": strings.ToUpper(boundSlug)}, "mode": "full"})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

// The approval link carries the code and the organization asked for, and the
// result says which organization the token is bound to.
func TestLoginNamesTheOrganization(t *testing.T) {
	srv := loginServer(t, "superlinked")
	defer srv.Close()
	t.Setenv("DECONFLICT_CREDENTIALS", t.TempDir()+"/credentials.json")
	t.Setenv("DECONFLICT_TOKEN", "")

	var out bytes.Buffer
	if err := cmdLogin([]string{"--server", srv.URL, "--org", "Superlinked"}, &out); err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
	for _, want := range []string{"/cli-auth?code=ABCD-EFGH&org=superlinked", "signed in as ana in organization SUPERLINKED (superlinked)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// Approving a different organization than the one asked for is loud: the
// token cannot be moved afterwards.
func TestLoginWarnsWhenTheApprovedOrganizationDiffers(t *testing.T) {
	srv := loginServer(t, "dragos-boca")
	defer srv.Close()
	t.Setenv("DECONFLICT_CREDENTIALS", t.TempDir()+"/credentials.json")
	t.Setenv("DECONFLICT_TOKEN", "")

	var out bytes.Buffer
	err := cmdLogin([]string{"--server", srv.URL, "--org", "superlinked"}, &out)
	if n, ok := Code(err); !ok || n != 3 {
		t.Fatalf("mismatched organization returned %v, want exit 3", err)
	}
	if !strings.Contains(out.String(), `you asked for "superlinked" but approved "dragos-boca"`) {
		t.Errorf("no warning:\n%s", out.String())
	}
}
