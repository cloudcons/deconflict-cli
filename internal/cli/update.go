package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/cloudcons/deconflict-cli/internal/gitinfo"
)

// `deconflict update` is the two steps an upgrade always was, done in order by
// the thing that knows both: replace the binary through whatever put it there,
// then have the new binary rewrite the skills, hooks and MCP entries it finds
// already installed. Doing only the first left every agent reading the old
// skill until somebody remembered the second.

// releasesURL is where releases live. A variable so tests can stand a server
// in for GitHub.
var releasesURL = "https://github.com/cloudcons/deconflict-cli/releases"

const module = "github.com/cloudcons/deconflict-cli"

// latestTag asks which release is latest without the API: /releases/latest
// redirects to the tag, and the unauthenticated API allows sixty calls an hour
// per address, which an office behind one NAT spends before lunch.
func latestTag(timeout time.Duration) (string, error) {
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(releasesURL + "/latest")
	if err != nil {
		return "", fmt.Errorf("could not reach GitHub to find the latest release: %w", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := path.Base(loc)
	if loc == "" || !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("could not work out the latest release (HTTP %d, Location %q)", resp.StatusCode, loc)
	}
	if _, ok := semver(tag); !ok {
		return "", fmt.Errorf("the latest release is tagged %q, which is not a version", tag)
	}
	return tag, nil
}

// channel is how this binary was installed, and so how it is upgraded.
type channel struct {
	name string
	// cmds run in order, attached to the terminal so sudo can prompt. Empty
	// for the self-update, which this process does itself.
	cmds [][]string
	self bool
}

// updateEnv is everything channel detection looks at, so tests can describe a
// machine instead of needing one.
type updateEnv struct {
	exe       string // the running binary, symlinks resolved
	goos      string
	root      bool                                   // running as root: no sudo
	lookPath  func(string) (string, error)           // exec.LookPath
	probe     func(name string, args ...string) bool // a command succeeds
	output    func(name string, args ...string) string
	buildInfo func() (*debug.BuildInfo, bool)
	getenv    func(string) string
	home      string
}

func realUpdateEnv() (updateEnv, error) {
	exe, err := os.Executable()
	if err != nil {
		return updateEnv{}, fmt.Errorf("cannot find this binary: %w", err)
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	home, _ := os.UserHomeDir()
	return updateEnv{
		exe:      exe,
		goos:     runtime.GOOS,
		root:     runtime.GOOS != "windows" && os.Geteuid() == 0,
		lookPath: exec.LookPath,
		probe: func(name string, args ...string) bool {
			return exec.Command(name, args...).Run() == nil
		},
		output: func(name string, args ...string) string {
			b, _ := exec.Command(name, args...).Output()
			return strings.TrimSpace(string(b))
		},
		buildInfo: debug.ReadBuildInfo,
		getenv:    os.Getenv,
		home:      home,
	}, nil
}

func (e updateEnv) sudo(args ...string) []string {
	if e.root {
		return args
	}
	return append([]string{"sudo"}, args...)
}

// detectChannel works out what installed the running binary. Each package
// manager is asked about this exact file rather than guessed from its
// directory: /usr/bin/deconflict could as well have been copied there by hand,
// and upgrading the package would then change a different file.
//
// pin is a version to install ("" for the latest), without the v.
func detectChannel(e updateEnv, pin string) (channel, error) {
	has := func(name string) bool { _, err := e.lookPath(name); return err == nil }
	slash := filepath.ToSlash(e.exe)

	if strings.Contains(slash, "/Cellar/deconflict/") {
		if pin != "" {
			return channel{}, errors.New("Homebrew installs only the latest release; drop --version, or install that version with the install script")
		}
		return channel{name: "Homebrew", cmds: [][]string{{"brew", "upgrade", "cloudcons/tap/deconflict"}}}, nil
	}

	if e.goos == "linux" && has("dpkg") && e.probe("dpkg", "-S", e.exe) {
		pkg := "deconflict"
		if pin != "" {
			pkg = "deconflict=" + pin
		}
		return channel{name: "apt", cmds: [][]string{
			e.sudo("apt-get", "update"),
			e.sudo("apt-get", "install", "--only-upgrade", "-y", pkg),
		}}, nil
	}

	if e.goos == "linux" && has("rpm") && e.probe("rpm", "-qf", e.exe) {
		tool := "dnf"
		if !has("dnf") {
			tool = "yum"
		}
		cmd := e.sudo(tool, "upgrade", "-y", "deconflict")
		if pin != "" {
			// upgrade does not go backwards; install of a named version does both.
			cmd = e.sudo(tool, "install", "-y", "deconflict-"+pin)
		}
		return channel{name: tool, cmds: [][]string{cmd}}, nil
	}

	if goInstalled(e) {
		version := "latest"
		if pin != "" {
			version = "v" + pin
		}
		return channel{name: "go install", cmds: [][]string{{"go", "install", module + "/cmd/deconflict@" + version}}}, nil
	}

	return channel{name: "release archive (install script or by hand)", self: true}, nil
}

// goInstalled is `go install`: this module's build, sitting where go install
// puts binaries. Both, because a release build carries the same module path
// and is often copied into ~/go/bin by people who keep their tools there.
func goInstalled(e updateEnv) bool {
	info, ok := e.buildInfo()
	if !ok || info.Main.Path != module {
		return false
	}
	var bins []string
	if b := e.getenv("GOBIN"); b != "" {
		bins = append(bins, b)
	}
	if gp := e.getenv("GOPATH"); gp != "" {
		for _, p := range filepath.SplitList(gp) {
			bins = append(bins, filepath.Join(p, "bin"))
		}
	}
	if _, err := e.lookPath("go"); err == nil {
		if b := e.output("go", "env", "GOBIN"); b != "" {
			bins = append(bins, b)
		}
		if gp := e.output("go", "env", "GOPATH"); gp != "" {
			for _, p := range filepath.SplitList(gp) {
				bins = append(bins, filepath.Join(p, "bin"))
			}
		}
	}
	if e.home != "" {
		bins = append(bins, filepath.Join(e.home, "go", "bin"))
	}
	dir := filepath.Dir(e.exe)
	for _, b := range bins {
		if r, err := filepath.EvalSymlinks(b); err == nil {
			b = r
		}
		if filepath.Clean(b) == dir {
			return true
		}
	}
	return false
}

func cmdUpdate(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(errOut)
	check := fs.Bool("check", false, "only say whether a newer release exists (exit 10 if one does)")
	dry := fs.Bool("dry-run", false, "print what would be run, downloaded and rewritten; change nothing")
	pin := fs.String("version", "", "install this version instead of the latest (e.g. 0.1.9)")
	force := fs.Bool("force", false, "reinstall even if already current, or replace a development build")
	if err := fs.Parse(args); err != nil {
		return err
	}
	current := clientVersion()

	var tag string
	if *pin != "" {
		tag = "v" + strings.TrimPrefix(*pin, "v")
		if _, ok := semver(tag); !ok {
			return fmt.Errorf("--version %q is not a version like 0.1.9", *pin)
		}
	} else {
		var err error
		if tag, err = latestTag(10 * time.Second); err != nil {
			return err
		}
	}
	_, isRelease := semver(current)
	behind := !isRelease || newer(tag, current)

	if *check {
		if !isRelease {
			fmt.Fprintf(out, "This is a development build (%s); the latest release is %s.\n", current, tag)
			return exitCode{10}
		}
		if behind {
			fmt.Fprintf(out, "deconflict %s is available (this is %s) — run `deconflict update`.\n", strings.TrimPrefix(tag, "v"), current)
			return exitCode{10}
		}
		fmt.Fprintf(out, "deconflict %s is the latest release.\n", current)
		return nil
	}

	env, err := realUpdateEnv()
	if err != nil {
		return err
	}
	// Exec'ed afterwards to refresh the config, so it has to be the path that
	// will hold the new binary: a Homebrew upgrade moves the real file to a new
	// Cellar directory and repoints the symlink we were started through.
	launcher, _ := os.Executable()

	upgrade := false
	switch {
	case *pin != "":
		upgrade = strings.TrimPrefix(tag, "v") != strings.TrimPrefix(current, "v") || *force
		if !upgrade {
			fmt.Fprintf(out, "deconflict %s is already installed.\n", current)
		}
	case !isRelease && !*force:
		fmt.Fprintf(out, "This is a development build (%s), so it is left in place — --force replaces it with %s.\n", current, tag)
	case behind || *force:
		upgrade = true
	default:
		fmt.Fprintf(out, "deconflict %s is the latest release.\n", current)
	}

	if upgrade {
		ch, err := detectChannel(env, strings.TrimPrefix(*pin, "v"))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Upgrading %s → %s, installed through %s (%s).\n", current, strings.TrimPrefix(tag, "v"), ch.name, env.exe)
		if ch.self {
			err = selfUpdate(env.exe, env.goos, runtime.GOARCH, tag, *dry, out)
		} else {
			err = runChannel(ch, *dry, out, errOut)
		}
		if err != nil {
			return err
		}
	}

	return refreshInstalled(launcher, *dry, out, errOut)
}

func runChannel(ch channel, dry bool, out, errOut io.Writer) error {
	for _, c := range ch.cmds {
		fmt.Fprintf(out, "  $ %s\n", strings.Join(c, " "))
		if dry {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, errOut
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", strings.Join(c, " "), err)
		}
	}
	return nil
}

// selfUpdate replaces a binary that no package manager owns with the release
// archive for this platform, checked against the release's SHA256SUMS before
// anything is written. The new file is written beside the old one and renamed
// over it, so a running copy (an agent's MCP server) keeps its inode instead of
// having the file rewritten under it, and a failure part-way leaves the old
// binary untouched.
func selfUpdate(exe, goos, goarch, tag string, dry bool, out io.Writer) error {
	version := strings.TrimPrefix(tag, "v")
	name := fmt.Sprintf("deconflict_%s_%s_%s", version, goos, goarch)
	archive, binName := name+".tar.gz", "deconflict"
	if goos == "windows" {
		archive, binName = name+".zip", "deconflict.exe"
	}
	base := releasesURL + "/download/" + tag + "/"
	dir := filepath.Dir(exe)

	if dry {
		fmt.Fprintf(out, "  would download %s%s, check it against SHA256SUMS, and replace %s\n", base, archive, exe)
		return nil
	}

	// Find out it cannot be written before downloading anything.
	probe, err := os.CreateTemp(dir, ".deconflict-update-*")
	if err != nil {
		return notWritable(dir, err)
	}
	probe.Close()
	os.Remove(probe.Name())

	sums, err := download(base+"SHA256SUMS", 1<<20)
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[1] == archive || f[1] == "*"+archive) {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s is not listed in the release's SHA256SUMS; is %s built for %s/%s?", archive, tag, goos, goarch)
	}
	body, err := download(base+archive, 256<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s — nothing was replaced", archive, want, got)
	}
	bin, err := extract(body, archive, binName)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".deconflict-new-*")
	if err != nil {
		return notWritable(dir, err)
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if goos == "windows" {
		// A running .exe cannot be replaced, but it can be renamed out of the way.
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return err
	}
	fmt.Fprintf(out, "  replaced %s\n", exe)
	return nil
}

func notWritable(dir string, err error) error {
	return fmt.Errorf("cannot write to %s (%v). Rerun as `sudo deconflict update`, or reinstall somewhere you own:\n"+
		"  curl -fsSL https://raw.githubusercontent.com/cloudcons/deconflict-cli/main/install.sh | sh", dir, err)
}

func download(url string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download %s: larger than %d bytes", url, limit)
	}
	return b, nil
}

// extract pulls the one binary out of a release archive, which holds nothing
// else.
func extract(body []byte, archive, binName string) ([]byte, error) {
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == binName {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 256<<20))
			}
		}
		return nil, fmt.Errorf("%s holds no %s", archive, binName)
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s holds no %s", archive, binName)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == binName {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
}

// ---------- refreshing what is installed ----------

// installTarget is one `deconflict install` that has evidently been run: an
// agent, a scope, and whether the skill and the always-loaded rule were part
// of it. Re-running with the same choices is the refresh; re-running with the
// defaults would add a skill somebody installed without on purpose.
type installTarget struct {
	agent, scope, root string
	skill, doc         bool
}

func (t installTarget) args(dry bool) []string {
	a := []string{"install", "--agent", t.agent, "--scope", t.scope, "--dir", t.root,
		fmt.Sprintf("--skill=%t", t.skill), fmt.Sprintf("--doc=%t", t.doc)}
	if dry {
		a = append(a, "--dry-run")
	}
	return a
}

// installedTargets finds our own entries, the same ones install writes and
// uninstall removes, in this repository and under the home directory.
func installedTargets(root, home string) []installTarget {
	var out []installTarget
	add := func(agent, scope, dir string, any, skill, doc bool) {
		if any || skill || doc {
			out = append(out, installTarget{agent, scope, dir, skill, doc})
		}
	}
	project := root != "" && filepath.Clean(root) != filepath.Clean(home)

	if project {
		add("claude", "project", root,
			hasOwnHooks(filepath.Join(root, ".claude", "settings.json")) || hasJSONMCP(filepath.Join(root, ".mcp.json")),
			exists(filepath.Join(root, ".claude", "skills", "deconflict", "SKILL.md")),
			hasGuidance(filepath.Join(root, "CLAUDE.md")))
	}
	add("claude", "user", root,
		hasOwnHooks(filepath.Join(home, ".claude", "settings.json")) || hasJSONMCP(filepath.Join(home, ".claude.json")),
		exists(filepath.Join(home, ".claude", "skills", "deconflict", "SKILL.md")),
		hasGuidance(filepath.Join(home, ".claude", "CLAUDE.md")))

	// Codex's hooks and MCP server live under home whichever scope wrote them,
	// so they say Codex is installed but not where; the skill and the rule say
	// which scope.
	codexHome := hasOwnHooks(filepath.Join(home, ".codex", "hooks.json")) || hasCodexMCP(filepath.Join(home, ".codex", "config.toml"))
	before := len(out)
	if project {
		add("codex", "project", root, false,
			exists(filepath.Join(root, ".agents", "skills", "deconflict", "SKILL.md")),
			hasGuidance(filepath.Join(root, "AGENTS.md")))
	}
	add("codex", "user", root, false,
		exists(filepath.Join(home, ".agents", "skills", "deconflict", "SKILL.md")),
		hasGuidance(filepath.Join(home, ".codex", "AGENTS.md")))
	if codexHome && len(out) == before {
		out = append(out, installTarget{"codex", "user", root, false, false})
	}
	return out
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func hasGuidance(p string) bool {
	b, err := os.ReadFile(p)
	return err == nil && strings.Contains(string(b), guidanceStart)
}

func hasOwnHooks(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	doc, err := parseJSONDoc(string(b))
	if err != nil {
		return false
	}
	hooks, err := childObject(doc.root, "hooks", false)
	if err != nil || hooks == nil {
		return false
	}
	for _, event := range hooks.keys {
		groups, _ := hooks.vals[event].([]any)
		for _, g := range groups {
			group, ok := g.(*jobj)
			if !ok {
				continue
			}
			entries, _ := group.vals["hooks"].([]any)
			for _, h := range entries {
				if entry, ok := h.(*jobj); ok {
					if cmd, ok := jstring(entry.vals["command"]); ok && ownAnyHook(cmd, "") {
						return true
					}
				}
			}
		}
	}
	return false
}

func hasJSONMCP(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	doc, err := parseJSONDoc(string(b))
	if err != nil {
		return false
	}
	servers, err := childObject(doc.root, "mcpServers", false)
	if err != nil || servers == nil {
		return false
	}
	_, ok := servers.get("deconflict")
	return ok
}

func hasCodexMCP(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	doc, err := parseTOMLDoc(string(b))
	if err != nil {
		return false
	}
	i, err := ourCodexTable(doc)
	return err == nil && i >= 0
}

// refreshInstalled re-runs install, through the binary now on disk rather than
// this process, for every place install evidently ran. This process may be the
// version that was just replaced, and its skill text is the old one.
func refreshInstalled(launcher string, dry bool, out, errOut io.Writer) error {
	cwd, _ := os.Getwd()
	root := gitinfo.Root(cwd)
	if root == "" {
		root = cwd
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	targets := installedTargets(root, home)
	if len(targets) == 0 {
		fmt.Fprintln(out, "\nNo deconflict hooks, MCP entries or skills are installed here or under your home directory.")
		fmt.Fprintln(out, "Set them up with: deconflict install --agent all")
		return nil
	}
	bin := launcher
	if _, err := os.Stat(bin); err != nil {
		if p, err := exec.LookPath("deconflict"); err == nil {
			bin = p
		}
	}
	if dry {
		fmt.Fprintln(out, "\nThen the agent configuration would be refreshed (shown here as this binary would write it):")
	} else {
		fmt.Fprintln(out, "\nRefreshing the agent configuration already installed:")
	}
	var failed []string
	for _, t := range targets {
		a := t.args(dry)
		fmt.Fprintf(out, "\n  $ deconflict %s\n", strings.Join(a, " "))
		cmd := exec.Command(bin, a...)
		cmd.Stdout, cmd.Stderr = out, errOut
		if err := cmd.Run(); err != nil {
			failed = append(failed, t.agent+" ("+t.scope+")")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("refreshing %s did not complete; see above", strings.Join(failed, ", "))
	}
	return nil
}
