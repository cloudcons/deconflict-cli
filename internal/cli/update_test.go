package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub stands in for github.com/cloudcons/deconflict-cli/releases: the
// /latest redirect and a release's downloads.
type fakeGitHub struct {
	latest string
	files  map[string][]byte // "v0.1.9/SHA256SUMS" -> body
	hits   atomic.Int32
}

func (f *fakeGitHub) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		switch {
		case r.URL.Path == "/latest":
			http.Redirect(w, r, "/cloudcons/deconflict-cli/releases/tag/"+f.latest, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			b, ok := f.files[strings.TrimPrefix(r.URL.Path, "/download/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := releasesURL
	releasesURL = srv.URL
	t.Cleanup(func() { releasesURL = old })
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

func TestLatestTagFollowsTheRedirectWithoutTheAPI(t *testing.T) {
	gh := &fakeGitHub{latest: "v0.1.9"}
	gh.start(t)
	tag, err := latestTag(time.Second)
	if err != nil || tag != "v0.1.9" {
		t.Fatalf("latestTag = %q, %v", tag, err)
	}
	gh.latest = "nightly"
	if _, err := latestTag(time.Second); err == nil {
		t.Fatal("a tag that is not a version was accepted")
	}
}

func TestUpdateCheckExitCodes(t *testing.T) {
	gh := &fakeGitHub{latest: "v0.1.9"}
	gh.start(t)

	withVersion(t, "0.1.8")
	var out bytes.Buffer
	err := cmdUpdate([]string{"--check"}, &out, &out)
	if n, ok := Code(err); !ok || n != 10 {
		t.Fatalf("behind: err = %v, want exit 10", err)
	}
	if !strings.Contains(out.String(), "0.1.9 is available (this is 0.1.8)") {
		t.Fatalf("output: %s", out.String())
	}

	Version = "0.1.9"
	out.Reset()
	if err := cmdUpdate([]string{"--check"}, &out, &out); err != nil {
		t.Fatalf("current: %v", err)
	}
	if !strings.Contains(out.String(), "0.1.9 is the latest") {
		t.Fatalf("output: %s", out.String())
	}
}

// fakeMachine describes a machine to channel detection.
type fakeMachine struct {
	exe      string
	goos     string
	tools    map[string]bool
	owns     map[string]bool // "dpkg" / "rpm" claims the binary
	module   string
	gobin    string
	root     bool
	homebrew bool
}

func (m fakeMachine) env() updateEnv {
	return updateEnv{
		exe:  m.exe,
		goos: m.goos,
		root: m.root,
		lookPath: func(name string) (string, error) {
			if m.tools[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		probe: func(name string, args ...string) bool { return m.owns[name] },
		output: func(name string, args ...string) string {
			if name == "go" && len(args) == 2 && args[1] == "GOBIN" {
				return m.gobin
			}
			return ""
		},
		buildInfo: func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Path: m.module}}, m.module != ""
		},
		getenv: func(string) string { return "" },
		home:   "/home/ana",
	}
}

func TestDetectChannel(t *testing.T) {
	cases := []struct {
		name    string
		m       fakeMachine
		pin     string
		want    string
		cmds    string
		self    bool
		wantErr bool
	}{
		{name: "homebrew", m: fakeMachine{exe: "/opt/homebrew/Cellar/deconflict/0.1.8/bin/deconflict", goos: "darwin"},
			want: "Homebrew", cmds: "brew upgrade cloudcons/tap/deconflict"},
		{name: "linuxbrew", m: fakeMachine{exe: "/home/linuxbrew/.linuxbrew/Cellar/deconflict/0.1.8/bin/deconflict", goos: "linux",
			tools: map[string]bool{"dpkg": true}, owns: map[string]bool{"dpkg": false}},
			want: "Homebrew", cmds: "brew upgrade cloudcons/tap/deconflict"},
		{name: "homebrew refuses a pin", m: fakeMachine{exe: "/opt/homebrew/Cellar/deconflict/0.1.8/bin/deconflict", goos: "darwin"},
			pin: "0.1.7", wantErr: true},
		{name: "apt", m: fakeMachine{exe: "/usr/bin/deconflict", goos: "linux", tools: map[string]bool{"dpkg": true}, owns: map[string]bool{"dpkg": true}},
			want: "apt", cmds: "sudo apt-get update; sudo apt-get install --only-upgrade -y deconflict"},
		{name: "apt as root, pinned", m: fakeMachine{exe: "/usr/bin/deconflict", goos: "linux", root: true, tools: map[string]bool{"dpkg": true}, owns: map[string]bool{"dpkg": true}},
			pin: "0.1.9", want: "apt", cmds: "apt-get update; apt-get install --only-upgrade -y deconflict=0.1.9"},
		{name: "dnf", m: fakeMachine{exe: "/usr/bin/deconflict", goos: "linux", tools: map[string]bool{"rpm": true, "dnf": true}, owns: map[string]bool{"rpm": true}},
			want: "dnf", cmds: "sudo dnf upgrade -y deconflict"},
		{name: "yum without dnf", m: fakeMachine{exe: "/usr/bin/deconflict", goos: "linux", tools: map[string]bool{"rpm": true}, owns: map[string]bool{"rpm": true}},
			want: "yum", cmds: "sudo yum upgrade -y deconflict"},
		{name: "rpm installed on a Debian box does not own it", m: fakeMachine{exe: "/usr/local/bin/deconflict", goos: "linux",
			tools: map[string]bool{"rpm": true, "dpkg": true}}, self: true},
		{name: "go install", m: fakeMachine{exe: "/home/ana/go/bin/deconflict", goos: "linux", module: module, tools: map[string]bool{"go": true}},
			want: "go install", cmds: "go install github.com/cloudcons/deconflict-cli/cmd/deconflict@latest"},
		{name: "go install into GOBIN, pinned", m: fakeMachine{exe: "/opt/tools/deconflict", goos: "darwin", module: module, gobin: "/opt/tools", tools: map[string]bool{"go": true}},
			pin: "0.1.8", want: "go install", cmds: "go install github.com/cloudcons/deconflict-cli/cmd/deconflict@v0.1.8"},
		{name: "release build copied elsewhere is not go install", m: fakeMachine{exe: "/home/ana/.local/bin/deconflict", goos: "linux", module: module, tools: map[string]bool{"go": true}},
			self: true},
		{name: "install script", m: fakeMachine{exe: "/home/ana/.local/bin/deconflict", goos: "linux"}, self: true},
		{name: "windows", m: fakeMachine{exe: `C:\Users\ana\AppData\Local\Programs\deconflict\deconflict.exe`, goos: "windows"}, self: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch, err := detectChannel(c.m.env(), c.pin)
			if c.wantErr {
				if err == nil {
					t.Fatalf("got %+v, want an error", ch)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ch.self != c.self {
				t.Fatalf("self = %v, want %v (%+v)", ch.self, c.self, ch)
			}
			if c.self {
				return
			}
			var cmds []string
			for _, cmd := range ch.cmds {
				cmds = append(cmds, strings.Join(cmd, " "))
			}
			if ch.name != c.want || strings.Join(cmds, "; ") != c.cmds {
				t.Fatalf("got %q: %s", ch.name, strings.Join(cmds, "; "))
			}
		})
	}
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipped(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name)
	w.Write(body)
	zw.Close()
	return buf.Bytes()
}

func sumLine(name string, body []byte) string {
	s := sha256.Sum256(body)
	return hex.EncodeToString(s[:]) + "  " + name + "\n"
}

func TestSelfUpdateReplacesTheBinary(t *testing.T) {
	archive := tarGz(t, "deconflict", []byte("new binary"))
	gh := &fakeGitHub{latest: "v0.1.9", files: map[string][]byte{
		"v0.1.9/deconflict_0.1.9_linux_amd64.tar.gz": archive,
		"v0.1.9/SHA256SUMS": []byte(sumLine("deconflict_0.1.9_linux_amd64.tar.gz", archive) +
			sumLine("deconflict_0.1.9_darwin_arm64.tar.gz", []byte("other"))),
	}}
	gh.start(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "deconflict")
	os.WriteFile(exe, []byte("old binary"), 0o755)

	var out bytes.Buffer
	if err := selfUpdate(exe, "linux", "amd64", "v0.1.9", false, &out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(exe)
	if string(b) != "new binary" {
		t.Fatalf("binary is %q", b)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("not executable: %v", fi.Mode())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("left files behind: %v", entries)
	}
}

func TestSelfUpdateRefusesABadChecksum(t *testing.T) {
	archive := tarGz(t, "deconflict", []byte("tampered"))
	gh := &fakeGitHub{latest: "v0.1.9", files: map[string][]byte{
		"v0.1.9/deconflict_0.1.9_linux_amd64.tar.gz": archive,
		"v0.1.9/SHA256SUMS":                          []byte(sumLine("deconflict_0.1.9_linux_amd64.tar.gz", []byte("the real one"))),
	}}
	gh.start(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "deconflict")
	os.WriteFile(exe, []byte("old binary"), 0o755)

	err := selfUpdate(exe, "linux", "amd64", "v0.1.9", false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" {
		t.Fatalf("the old binary was touched: %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("left files behind: %v", entries)
	}
}

func TestSelfUpdateWindowsMovesTheRunningExeAside(t *testing.T) {
	archive := zipped(t, "deconflict.exe", []byte("new exe"))
	gh := &fakeGitHub{latest: "v0.1.9", files: map[string][]byte{
		"v0.1.9/deconflict_0.1.9_windows_arm64.zip": archive,
		"v0.1.9/SHA256SUMS":                         []byte(sumLine("deconflict_0.1.9_windows_arm64.zip", archive)),
	}}
	gh.start(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "deconflict.exe")
	os.WriteFile(exe, []byte("old exe"), 0o755)

	if err := selfUpdate(exe, "windows", "arm64", "v0.1.9", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new exe" {
		t.Fatalf("exe is %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old exe" {
		t.Fatalf(".old is %q", b)
	}
}

func TestSelfUpdateDryRunTouchesNothing(t *testing.T) {
	gh := &fakeGitHub{latest: "v0.1.9"}
	gh.start(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "deconflict")
	os.WriteFile(exe, []byte("old"), 0o755)
	var out bytes.Buffer
	if err := selfUpdate(exe, "linux", "amd64", "v0.1.9", true, &out); err != nil {
		t.Fatal(err)
	}
	if gh.hits.Load() != 0 || !strings.Contains(out.String(), "would download") {
		t.Fatalf("dry run downloaded (%d hits): %s", gh.hits.Load(), out.String())
	}
}

func TestUpdateNudgeAtMostOncePerDay(t *testing.T) {
	gh := &fakeGitHub{latest: "v0.1.9"}
	gh.start(t)
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LocalAppData", cache)
	t.Setenv("DECONFLICT_NO_UPDATE_CHECK", "")
	withVersion(t, "0.1.8")

	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	msg := updateContext(now)
	if msg != "deconflict 0.1.9 is available (this is 0.1.8) — run `deconflict update`." {
		t.Fatalf("first session: %q", msg)
	}
	if gh.hits.Load() != 1 {
		t.Fatalf("hits = %d", gh.hits.Load())
	}
	// Later the same day: neither asked again nor said again.
	if msg := updateContext(now.Add(3 * time.Hour)); msg != "" {
		t.Fatalf("second session the same day: %q", msg)
	}
	if gh.hits.Load() != 1 {
		t.Fatalf("checked again within a day: %d hits", gh.hits.Load())
	}
	// Next day: checked and said once more.
	if msg := updateContext(now.Add(25 * time.Hour)); msg == "" || gh.hits.Load() != 2 {
		t.Fatalf("next day: %q, %d hits", msg, gh.hits.Load())
	}
	// Up to date: nothing to say.
	Version = "0.1.9"
	if msg := updateContext(now.Add(72 * time.Hour)); msg != "" {
		t.Fatalf("current version was nudged: %q", msg)
	}
	// Switched off: no request at all.
	t.Setenv("DECONFLICT_NO_UPDATE_CHECK", "1")
	Version = "0.1.8"
	before := gh.hits.Load()
	if msg := updateContext(now.Add(200 * time.Hour)); msg != "" || gh.hits.Load() != before {
		t.Fatalf("DECONFLICT_NO_UPDATE_CHECK=1 still checked: %q", msg)
	}
}

func TestUpdateNudgeGivesUpQuietly(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer slow.Close()
	old, oldTimeout := releasesURL, updateCheckTimeout
	releasesURL, updateCheckTimeout = slow.URL, 100*time.Millisecond
	defer func() { releasesURL, updateCheckTimeout = old, oldTimeout }()
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("DECONFLICT_NO_UPDATE_CHECK", "")
	withVersion(t, "0.1.8")

	start := time.Now()
	if msg := updateContext(time.Now()); msg != "" {
		t.Fatalf("msg = %q", msg)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("a slow GitHub held the session start for %v", d)
	}
}

func TestInstalledTargets(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	write := func(p, body string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := installedTargets(root, home); len(got) != 0 {
		t.Fatalf("nothing installed, found %+v", got)
	}

	// Claude, project scope, installed without the skill; somebody else's hook
	// beside ours must not be mistaken for it, nor theirs alone count.
	write(filepath.Join(root, ".claude", "settings.json"), `{"hooks":{"PreToolUse":[
	  {"matcher":"Bash","hooks":[{"type":"command","command":"lefthook hook pre-tool"}]},
	  {"matcher":"Edit|Write|NotebookEdit","hooks":[{"type":"command","command":"/usr/local/bin/deconflict hook pre-tool"}]}]}}`)
	write(filepath.Join(root, "CLAUDE.md"), "# rules\n"+guidanceStart+"\nx\n"+guidanceEnd+"\n")
	// Codex, user scope: hooks under home, skill under ~/.agents.
	write(filepath.Join(home, ".codex", "config.toml"), "[mcp_servers.deconflict]\ncommand = \"deconflict\"\nargs = [\"mcp\"]\n")
	write(filepath.Join(home, ".agents", "skills", "deconflict", "SKILL.md"), "skill")

	got := fmt.Sprint(installedTargets(root, home))
	want := fmt.Sprint([]installTarget{
		{"claude", "project", root, false, true},
		{"codex", "user", root, true, false},
	})
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	// Only somebody else's hook: not installed.
	root2 := t.TempDir()
	write(filepath.Join(root2, ".claude", "settings.json"), `{"hooks":{"PreToolUse":[{"hooks":[{"command":"lefthook hook pre-tool"}]}]}}`)
	for _, tg := range installedTargets(root2, t.TempDir()) {
		t.Fatalf("found %+v from somebody else's hook", tg)
	}

	// Codex hooks with no skill or rule anywhere: refreshed as they were.
	home3 := t.TempDir()
	write(filepath.Join(home3, ".codex", "hooks.json"), `{"hooks":{"SessionStart":[{"hooks":[{"command":"deconflict hook session-start"}]}]}}`)
	got = fmt.Sprint(installedTargets(t.TempDir(), home3))
	if !strings.Contains(got, "codex user") || !strings.Contains(got, "false false") {
		t.Fatalf("codex hooks only: %s", got)
	}
}
