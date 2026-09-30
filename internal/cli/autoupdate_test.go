package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func approvedRegistry(t *testing.T, version string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/settings" {
			_ = json.NewEncoder(w).Encode(map[string]any{"default_lease": "8h", "max_lease": "72h", "client_version": version})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DECONFLICT_TOKEN", "t")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	return srv.URL
}

func captureStarts(t *testing.T, fail error) *[]string {
	t.Helper()
	var started []string
	old := startAutoUpdate
	startAutoUpdate = func(v string) error { started = append(started, v); return fail }
	t.Cleanup(func() { startAutoUpdate = old })
	return &started
}

// The organization's version is the rollout: a client behind it — or ahead of
// it, which is a rollback — starts updating itself, once, and says so.
func TestAutoUpdateFollowsTheOrganizationBothWays(t *testing.T) {
	for _, tc := range []struct{ installed, approved string }{{"0.1.17", "0.1.18"}, {"0.1.18", "0.1.17"}} {
		t.Run(tc.installed+"->"+tc.approved, func(t *testing.T) {
			dsn := approvedRegistry(t, tc.approved)
			withVersion(t, tc.installed)
			started := captureStarts(t, nil)
			now := time.Now()

			msg, pinned := autoUpdateContext(dsn, now)
			if !pinned || len(*started) != 1 || (*started)[0] != tc.approved || !strings.Contains(msg, "updating itself") {
				t.Fatalf("first session: pinned=%v started=%v msg=%q", pinned, *started, msg)
			}
			// A second session while it runs does not start another.
			if msg, _ := autoUpdateContext(dsn, now.Add(time.Minute)); msg != "" || len(*started) != 1 {
				t.Fatalf("second session started again: %v %q", *started, msg)
			}
			// Once it lands, the next session says so, once.
			finishAutoUpdate(tc.approved, nil)
			withVersion(t, tc.approved)
			if msg, _ := autoUpdateContext(dsn, now.Add(time.Hour)); !strings.Contains(msg, "was updated to "+tc.approved) {
				t.Fatalf("after the update: %q", msg)
			}
			if msg, _ := autoUpdateContext(dsn, now.Add(2*time.Hour)); msg != "" {
				t.Fatalf("reported twice: %q", msg)
			}
		})
	}
}

// A failed update is reported once, and retried only after a pause.
func TestAutoUpdateReportsAFailureOnce(t *testing.T) {
	dsn := approvedRegistry(t, "0.1.18")
	withVersion(t, "0.1.17")
	started := captureStarts(t, nil)
	now := time.Now()
	autoUpdateContext(dsn, now)
	finishAutoUpdate("0.1.18", errors.New("cannot write to /opt/deconflict"))
	msg, _ := autoUpdateContext(dsn, now.Add(time.Minute))
	if !strings.Contains(msg, "failed") || !strings.Contains(msg, "/opt/deconflict") {
		t.Fatalf("failure not reported: %q", msg)
	}
	if len(*started) != 1 {
		t.Fatalf("retried at once after failing: %v", *started)
	}
	autoUpdateContext(dsn, now.Add(time.Hour))
	if len(*started) != 2 {
		t.Fatalf("never retried: %v", *started)
	}
}

// No approved version is the old behaviour; opting out is respected; a
// development build is somebody working on the client and is left alone.
func TestAutoUpdateStaysOutOfTheWay(t *testing.T) {
	started := captureStarts(t, nil)

	dsn := approvedRegistry(t, "")
	withVersion(t, "0.1.17")
	if msg, pinned := autoUpdateContext(dsn, time.Now()); pinned || msg != "" {
		t.Errorf("no approved version: pinned=%v %q", pinned, msg)
	}

	dsn = approvedRegistry(t, "0.1.18")
	t.Setenv("DECONFLICT_NO_AUTO_UPDATE", "1")
	if msg, _ := autoUpdateContext(dsn, time.Now()); !strings.Contains(msg, "automatic updates are off") {
		t.Errorf("opted out: %q", msg)
	}
	t.Setenv("DECONFLICT_NO_AUTO_UPDATE", "")

	withVersion(t, "dev")
	if msg, pinned := autoUpdateContext(dsn, time.Now()); !pinned || msg != "" {
		t.Errorf("development build: pinned=%v %q", pinned, msg)
	}
	if len(*started) != 0 {
		t.Errorf("started an update: %v", *started)
	}
}
