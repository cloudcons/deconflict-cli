package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/client"
)

// Updates over the air, by organization.
//
// Every release used to be followed by a hand-run rollout per managed machine:
// a pin bumped in a playbook, a skill regenerated and committed, a playbook run
// from an operator's laptop. The registry now carries the one decision that
// matters — which client release this organization runs, in its settings — and
// each client brings itself to it at session start, in the background. Setting
// it is the rollout; setting it back is the rollback. There is no canary: the
// approved version is the gate.
//
// It only ever installs a published release, checked against the release's
// SHA256SUMS by `deconflict update`; the registry names a version, never a URL.
// DECONFLICT_NO_AUTO_UPDATE=1 opts a machine out.

// autoUpdateRetry is how long a failed or running attempt holds off the next.
const autoUpdateRetry = 30 * time.Minute

// autoUpdateState is the last attempt, kept beside the update-check cache.
type autoUpdateState struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	Error     string    `json:"error,omitempty"`
	Done      bool      `json:"done,omitempty"`
	Reported  bool      `json:"reported,omitempty"`
}

func autoUpdateStatePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "deconflict", "auto-update.json")
}

func readAutoUpdate() autoUpdateState {
	var st autoUpdateState
	if p := autoUpdateStatePath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &st)
		}
	}
	return st
}

func writeAutoUpdate(st autoUpdateState) {
	p := autoUpdateStatePath()
	if p == "" {
		return
	}
	if b, err := json.Marshal(st); err == nil {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, b, 0o644)
	}
}

// approvedVersion is the organization's client version, or "" when it has
// none, the registry is not an http one, or it cannot be reached.
func approvedVersion(dsn string) string {
	st, err := openStore(dsn)
	if err != nil {
		return ""
	}
	if _, ok := st.(*client.HTTPStore); !ok {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(st.Settings().ClientVersion), "v")
}

// autoUpdateContext is what session start says about the organization's
// version: that an update to it has started, or that the last one failed. It
// reports whether the organization has approved a version at all, which
// replaces the "a newer release is available" nudge — an organization that
// pins a version does not want its agents told to run past it.
func autoUpdateContext(dsn string, now time.Time) (string, bool) {
	approved := approvedVersion(dsn)
	if approved == "" {
		return "", false
	}
	current := strings.TrimPrefix(clientVersion(), "v")
	if _, ok := semver(current); !ok {
		return "", true // a development build is somebody working on the client
	}
	st := readAutoUpdate()
	if approved == current {
		if st.Version == approved && st.Done && !st.Reported {
			st.Reported = true
			writeAutoUpdate(st)
			return fmt.Sprintf("deconflict was updated to %s, the version this organization runs.", approved), true
		}
		return "", true
	}
	if os.Getenv("DECONFLICT_NO_AUTO_UPDATE") == "1" {
		return fmt.Sprintf("This organization runs deconflict %s (this is %s); automatic updates are off here — run `deconflict update --version %s`.", approved, current, approved), true
	}
	if st.Version == approved && st.Error != "" && !st.Reported {
		st.Reported = true
		writeAutoUpdate(st)
		return fmt.Sprintf("The automatic update to deconflict %s (the version this organization runs) failed: %s\nRun `deconflict update --version %s` to see it through.", approved, st.Error, approved), true
	}
	if st.Version == approved && now.Sub(st.StartedAt) < autoUpdateRetry {
		return "", true // one attempt at a time, and not in a loop when it fails
	}
	if err := startAutoUpdate(approved); err != nil {
		writeAutoUpdate(autoUpdateState{Version: approved, StartedAt: now, Error: err.Error()})
		return "", true
	}
	writeAutoUpdate(autoUpdateState{Version: approved, StartedAt: now})
	return fmt.Sprintf("deconflict is updating itself from %s to %s, the version this organization runs, in the background. The next session uses it.", current, approved), true
}

// startAutoUpdate runs `deconflict update --version <v> --auto` detached, so
// session start never waits on a download. Its output goes to a log beside
// the state file. A variable so tests can see it called without running it.
var startAutoUpdate = spawnAutoUpdate

func spawnAutoUpdate(version string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(filepath.Dir(autoUpdateStatePath()), "auto-update.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(self, "update", "--version", version, "--auto")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = log, log, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// finishAutoUpdate records how an --auto run ended, for the next session.
func finishAutoUpdate(version string, err error) {
	st := readAutoUpdate()
	st.Version = version
	st.Done = err == nil
	st.Reported = false
	st.Error = ""
	if err != nil {
		st.Error = strings.TrimSpace(err.Error())
	}
	writeAutoUpdate(st)
}
