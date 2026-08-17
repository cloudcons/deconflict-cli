// Package cli implements the `claims` command.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudcons/agentclaims/internal/claim"
	"github.com/cloudcons/agentclaims/internal/gitinfo"
	"github.com/cloudcons/agentclaims/internal/store"
)

const usage = `claims — advisory intention claims for agents working one repo in parallel

  claims claim   --paths <glob,...> --what <text> [--why ...] [--not ...]
  claims check   [--paths <glob,...>] [--file <path>]   what overlaps this area
  claims list    [--all] [--repo <id>] [--json]
  claims release [id] [--reason merged|abandoned|superseded] [--pr <url>]
  claims renew   [id] [--ttl 8h]
  claims status                          my claim, with git-derived progress
  claims reconcile [--apply]             close claims whose branch is merged
  claims pr-overlap --repo o/n --pr N [--comment]   which open PRs share files
  claims settings [--json]               what the operator has configured
  claims serve   [--addr :7777]          shared registry + control panel
  claims hook    <session-start|pre-tool|user-prompt>

Store: $AGENTCLAIMS_STORE (file:/path or http://host:port), default local file.
`

// Run dispatches a subcommand. Returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "claim":
		err = cmdClaim(rest, stdout)
	case "check":
		err = cmdCheck(rest, stdout)
	case "list":
		err = cmdList(rest, stdout)
	case "release":
		err = cmdRelease(rest, stdout)
	case "renew":
		err = cmdRenew(rest, stdout)
	case "status":
		err = cmdStatus(rest, stdout)
	case "reconcile":
		err = cmdReconcile(rest, stdout)
	case "settings":
		err = cmdSettings(rest, stdout)
	case "pr-overlap":
		err = cmdPROverlap(rest, stdout)
	case "serve":
		err = cmdServe(rest, stdout)
	case "hook":
		err = cmdHook(rest, stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		// An overlap is a report, not a failure: it carries its own exit code
		// and has already printed a human-readable account to stdout.
		if n, ok := Code(err); ok {
			return n
		}
		fmt.Fprintf(stderr, "claims: %v\n", err)
		return 1
	}
	return 0
}

func openStore(dsn string) (store.Store, error) { return store.Open(dsn) }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func agentID() string {
	if v := os.Getenv("AGENTCLAIMS_AGENT"); v != "" {
		return v
	}
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	host, _ := os.Hostname()
	if user == "" {
		user = "unknown"
	}
	return user + "@" + host
}

// currentPath is where this worktree remembers its own claim id, so release
// and renew never need one typed. Per-worktree by construction: `git rev-parse
// --git-dir` differs for every worktree of a repo.
func currentPath(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--absolute-git-dir").Output()
	if err == nil {
		if g := strings.TrimSpace(string(out)); g != "" {
			return filepath.Join(g, "agentclaims-current")
		}
	}
	return filepath.Join(filepath.Dir(store.DefaultPath()), "current")
}

func readCurrent(dir string) string {
	b, err := os.ReadFile(currentPath(dir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeCurrent(dir, id string) {
	p := currentPath(dir)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(id+"\n"), 0o644)
}

func cmdClaim(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	paths := fs.String("paths", "", "comma-separated globs this task will touch (required)")
	not := fs.String("not", "", "comma-separated globs this task will NOT touch")
	what := fs.String("what", "", "what you are doing (required)")
	why := fs.String("why", "", "why — the reason another agent needs to judge overlap")
	iface := fs.String("interface", "", "expected interface/schema changes others would notice")
	prio := fs.String("priority", "", "priority label, free-form")
	task := fs.String("task", "", "external ticket reference")
	ttl := fs.Duration("ttl", 0, "lease duration; the claim decays after this (default: operator setting)")
	dsn := fs.String("store", "", "store DSN")
	asJSON := fs.Bool("json", false, "emit the claim as JSON")
	force := fs.Bool("force", false, "claim even if it overlaps (default: still claims, exits 3)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *paths == "" || *what == "" {
		return fmt.Errorf("--paths and --what are required")
	}
	cwd, _ := os.Getwd()
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	cfg := st.Settings()
	if *ttl == 0 {
		*ttl = cfg.DefaultLease.Std()
	}
	if max := cfg.MaxLease.Std(); max > 0 && *ttl > max {
		fmt.Fprintf(out, "lease capped at the configured maximum of %s\n", max)
		*ttl = max
	}
	if cfg.RequireWhy && strings.TrimSpace(*why) == "" {
		return fmt.Errorf("--why is required here (operator setting): another agent needs it to judge the overlap")
	}
	if cfg.RequireNot && strings.TrimSpace(*not) == "" {
		return fmt.Errorf("--not is required here (operator setting): state what you will NOT touch, " +
			"or others must back off from your whole area")
	}
	now := time.Now().UTC()
	c := claim.Claim{
		ID:        claim.NewID(),
		Repo:      gitinfo.Repo(cwd),
		Agent:     agentID(),
		Branch:    gitinfo.Branch(cwd),
		HeadSHA:   gitinfo.HeadSHA(cwd),
		Paths:     claim.NormalizeAll(splitList(*paths)),
		NotPaths:  claim.NormalizeAll(splitList(*not)),
		What:      strings.TrimSpace(*what),
		Why:       strings.TrimSpace(*why),
		Interface: strings.TrimSpace(*iface),
		Priority:  *prio,
		Task:      *task,
		Created:   now,
		Expires:   now.Add(*ttl),
	}
	c.Host, _ = os.Hostname()

	// Look before writing, but write regardless: the claim is an
	// announcement, and an overlap is a thing both sides should see.
	var conflicts []claim.Conflict
	if evs, err := st.Events(); err == nil {
		conflicts = claim.FindOverlaps(claim.Fold(evs), c.Repo, c.Paths, now, nil, cfg.IgnorePaths)
	} else {
		fmt.Fprintf(out, "warning: could not read registry (%v) — claiming anyway\n", err)
	}
	if err := st.Append(claim.Event{Op: "claim", TS: now, Claim: c}); err != nil {
		// Loud, but not blocking: the work should go ahead, and the agent
		// should know its announcement is invisible to everyone else.
		fmt.Fprintf(os.Stderr,
			"claims: WARNING — claim was NOT recorded (%v).\n"+
				"        Other agents cannot see it. Tell your operator, and say so on the PR.\n", err)
		return nil
	}
	writeCurrent(cwd, c.ID)

	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"claim": c, "conflicts": conflicts})
	}
	fmt.Fprintf(out, "claimed %s  %s  (lease %s)\n", c.ID, strings.Join(c.Paths, ", "), *ttl)
	if len(conflicts) == 0 {
		fmt.Fprintln(out, "no overlapping claims.")
		return nil
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, claim.Render(conflicts, now))
	if !*force {
		// Exit 3 = claimed, but overlapping. A wrapper or CI step can key on
		// it; a human can ignore it. Nothing is blocked either way.
		return exitCode{3}
	}
	return nil
}

type exitCode struct{ n int }

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", e.n) }

// Code lets main map a sentinel error to a process exit status.
func Code(err error) (int, bool) {
	if e, ok := err.(exitCode); ok {
		return e.n, true
	}
	return 0, false
}

func cmdCheck(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	paths := fs.String("paths", "", "comma-separated globs to check")
	file := fs.String("file", "", "a single concrete path to check")
	dsn := fs.String("store", "", "store DSN")
	asJSON := fs.Bool("json", false, "emit JSON")
	quiet := fs.Bool("quiet", false, "print nothing when there is no overlap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	list := splitList(*paths)
	if *file != "" {
		list = append(list, *file)
	}
	if len(list) == 0 {
		return fmt.Errorf("--paths or --file is required")
	}
	cwd, _ := os.Getwd()
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	evs, err := st.Events()
	if err != nil {
		// Advisory means never blocking. A registry that is unreachable
		// produces no warnings; it must not produce a failed command that a
		// wrapper or an agent reads as "stop".
		fmt.Fprintf(os.Stderr, "claims: registry unreachable (%v) — proceeding without overlap information\n", err)
		return nil
	}
	now := time.Now().UTC()
	mine := map[string]bool{}
	if id := readCurrent(cwd); id != "" {
		mine[id] = true
	}
	conflicts := claim.FindOverlaps(claim.Fold(evs), gitinfo.Repo(cwd), claim.NormalizeAll(list), now, mine, st.Settings().IgnorePaths)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(conflicts)
	}
	if len(conflicts) == 0 {
		if !*quiet {
			fmt.Fprintln(out, "no overlapping claims.")
		}
		return nil
	}
	fmt.Fprint(out, claim.Render(conflicts, now))
	return exitCode{3}
}

func cmdList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	all := fs.Bool("all", false, "include released and expired claims")
	repo := fs.String("repo", "", "filter by repo id (default: this repo; \"-\" for every repo)")
	dsn := fs.String("store", "", "store DSN")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	evs, err := st.Events()
	if err != nil {
		return err
	}
	want := *repo
	if want == "" {
		want = gitinfo.Repo(cwd)
	}
	if want == "-" {
		want = ""
	}
	now := time.Now().UTC()
	var sel []claim.Claim
	for _, c := range claim.Fold(evs) {
		if want != "" && c.Repo != want {
			continue
		}
		if !*all && !c.Active(now) {
			continue
		}
		sel = append(sel, c)
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(sel)
	}
	if len(sel) == 0 {
		fmt.Fprintln(out, "no claims.")
		return nil
	}
	for _, c := range sel {
		state := "active"
		switch {
		case c.Released != nil:
			state = "released:" + c.ReleaseReason
		case c.Stale(now):
			state = "expired"
		}
		fmt.Fprintf(out, "%s  %-10s %-22s %s ago\n", c.ID, state, c.Agent, c.Age(now))
		fmt.Fprintf(out, "    %s\n", c.What)
		fmt.Fprintf(out, "    paths: %s\n", strings.Join(c.Paths, ", "))
		if len(c.NotPaths) > 0 {
			fmt.Fprintf(out, "    not:   %s\n", strings.Join(c.NotPaths, ", "))
		}
	}
	return nil
}

func loadOne(st store.Store, id string) (claim.Claim, error) {
	evs, err := st.Events()
	if err != nil {
		return claim.Claim{}, err
	}
	for _, c := range claim.Fold(evs) {
		if c.ID == id {
			return c, nil
		}
	}
	return claim.Claim{}, fmt.Errorf("no claim %q", id)
}

func cmdRelease(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	reason := fs.String("reason", "merged", "merged | abandoned | superseded")
	pr := fs.String("pr", "", "PR URL to record")
	dsn := fs.String("store", "", "store DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	id := fs.Arg(0)
	if id == "" {
		id = readCurrent(cwd)
	}
	if id == "" {
		return fmt.Errorf("no claim id given and none recorded for this worktree")
	}
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	c, err := loadOne(st, id)
	if err != nil {
		return err
	}
	c.ReleaseReason = *reason
	c.PR = *pr
	if err := st.Append(claim.Event{Op: "release", TS: time.Now().UTC(), Claim: c}); err != nil {
		return err
	}
	if readCurrent(cwd) == id {
		_ = os.Remove(currentPath(cwd))
	}
	fmt.Fprintf(out, "released %s (%s)\n", id, *reason)
	return nil
}

func cmdRenew(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("renew", flag.ContinueOnError)
	ttl := fs.Duration("ttl", 8*time.Hour, "new lease from now")
	dsn := fs.String("store", "", "store DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	id := fs.Arg(0)
	if id == "" {
		id = readCurrent(cwd)
	}
	if id == "" {
		return fmt.Errorf("no claim id given and none recorded for this worktree")
	}
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	c, err := loadOne(st, id)
	if err != nil {
		return err
	}
	c.Expires = time.Now().UTC().Add(*ttl)
	if err := st.Append(claim.Event{Op: "renew", TS: time.Now().UTC(), Claim: c}); err != nil {
		return err
	}
	fmt.Fprintf(out, "renewed %s until %s\n", id, c.Expires.Local().Format(time.Kitchen))
	return nil
}

// cmdStatus reports my claim with progress derived from git rather than from
// anything the agent said about itself.
func cmdStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	id := readCurrent(cwd)
	if id == "" {
		fmt.Fprintln(out, "no claim held in this worktree.")
		return nil
	}
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	c, err := loadOne(st, id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	base := gitinfo.BaseRef(cwd)
	changed := gitinfo.ChangedPaths(cwd, base)
	fmt.Fprintf(out, "claim %s  %s ago  lease %s left\n", c.ID, c.Age(now), c.TTL(now))
	fmt.Fprintf(out, "  what:    %s\n", c.What)
	fmt.Fprintf(out, "  claimed: %s\n", strings.Join(c.Paths, ", "))
	fmt.Fprintf(out, "  branch:  %s (%d commits ahead of %s)\n", c.Branch, gitinfo.AheadBy(cwd, c.Branch, base), base)
	fmt.Fprintf(out, "  touched: %d file(s)\n", len(changed))

	// Files edited outside the declared area. Not an error — a prompt to widen
	// the claim so the announcement keeps matching reality.
	var outside []string
	for _, f := range changed {
		hit := false
		for _, p := range c.Paths {
			if claim.Match(p, f) {
				hit = true
				break
			}
		}
		if !hit {
			outside = append(outside, f)
		}
	}
	if len(outside) > 0 {
		fmt.Fprintf(out, "\n  %d changed file(s) fall OUTSIDE your claim:\n", len(outside))
		for _, f := range outside {
			fmt.Fprintf(out, "    %s\n", f)
		}
		fmt.Fprintln(out, "  Re-claim with the wider path set so others can see it.")
	}
	return nil
}

// cmdReconcile closes claims whose branch has landed in the published base.
// Derived, not reported: "done" is the most common lie in an agent system, and
// merged-into-base is a fact git already holds.
func cmdReconcile(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "actually release; default reports only")
	dsn := fs.String("store", "", "store DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	evs, err := st.Events()
	if err != nil {
		return err
	}
	repo := gitinfo.Repo(cwd)
	base := gitinfo.BaseRef(cwd)
	if base == "" {
		return fmt.Errorf("no published base branch found (set AGENTCLAIMS_BASE)")
	}
	now := time.Now().UTC()
	n := 0
	for _, c := range claim.Fold(evs) {
		if c.Repo != repo || !c.Active(now) || c.Branch == "" {
			continue
		}
		if !gitinfo.Landed(cwd, c.Branch, base, c.HeadSHA) {
			continue
		}
		n++
		fmt.Fprintf(out, "%s %s (%s) — branch %s is in %s\n",
			map[bool]string{true: "releasing", false: "would release"}[*apply], c.ID, c.Agent, c.Branch, base)
		if *apply {
			c.ReleaseReason = "merged"
			if err := st.Append(claim.Event{Op: "release", TS: now, Claim: c}); err != nil {
				return err
			}
		}
	}
	if n == 0 {
		fmt.Fprintln(out, "nothing to reconcile.")
	}
	return nil
}

// cmdSettings shows what the registry is configured to do. Read-only on
// purpose: settings are changed in the control panel or by editing the file,
// and a CLI writer would be a third path to keep consistent.
func cmdSettings(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("settings", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	cfg := st.Settings()
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(cfg)
	}
	fmt.Fprintf(out, "registry:       %s\n", st.Describe())
	fmt.Fprintf(out, "default lease:  %s (max %s)\n", cfg.DefaultLease.Std(), cfg.MaxLease.Std())
	fmt.Fprintf(out, "auto-release:   %v (grace %s)\n", cfg.AutoReleaseStale, cfg.StaleGrace.Std())
	fmt.Fprintf(out, "required:       %s\n", requiredFields(cfg.RequireWhy, cfg.RequireNot))
	fmt.Fprintf(out, "webhook:        %s\n", map[bool]string{true: "configured", false: "none"}[cfg.WebhookURL != "" && cfg.WebhookOnOverlap])
	fmt.Fprintf(out, "never overlap:  %s\n", strings.Join(cfg.IgnorePaths, ", "))
	return nil
}

func requiredFields(why, not bool) string {
	var f []string
	if why {
		f = append(f, "--why")
	}
	if not {
		f = append(f, "--not")
	}
	if len(f) == 0 {
		return "nothing beyond --paths and --what"
	}
	return strings.Join(f, ", ")
}
