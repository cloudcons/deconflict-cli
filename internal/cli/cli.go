// Package cli implements the `deconflict` command.
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

	"github.com/cloudcons/deconflict-cli/pkg/claim"
	"github.com/cloudcons/deconflict-cli/internal/gitinfo"
	"github.com/cloudcons/deconflict-cli/pkg/client"
)

// Version is stamped at build time:
//
//	go build -ldflags "-X github.com/cloudcons/deconflict-cli/internal/cli.Version=$(git describe --tags)"
//
// It exists so that a client and a registry that no longer ship as one binary
// can be told apart in a bug report. "dev" is what an unstamped local build
// says, which is the honest answer for one.
var Version = "dev"

// movedToServer answers the three commands that need a database credential.
// They are not gone, and saying where they went is the difference between a
// one-minute correction and a bug report.
func movedToServer(cmd string) error {
	return fmt.Errorf("`deconflict %s` is a registry operator's command and now lives in the deconflict-server binary — this is the client", cmd)
}

const usage = `deconflict — advisory intention claims for agents working one repo in parallel

Claiming
  deconflict claim   --paths <glob,...> --what <text> [--why ...] [--not ...] [--uses <glob,...>] [--task <ref>]
  deconflict check   [--paths <glob,...>] [--file <path>]   what overlaps this area
  deconflict list    [--all] [--repo <id>] [--json]
  deconflict release [id] [--reason merged|abandoned|superseded] [--pr <url>]
  deconflict amend   [id] --paths <globs> [--not <globs>] [--what …] [--why …]
  deconflict renew   [id] [--ttl 8h]
  deconflict status                          my claim, with git-derived progress
  deconflict reconcile [--apply]             close claims whose branch is merged

Shared resources (environments, databases, deploy slots)
  deconflict lock     <resource> [--shared] [--reason …] [--ttl 1h]
                                             advisory lock; exit 3 if someone else holds it
  deconflict unlock   <resource> [--all]
  deconflict resource <list|add|remove|release|renew>

Agent coordination
  deconflict negotiate request --paths <glob,...> --objective <text>
  deconflict negotiate <list|show|propose|accept|checkpoint|recover|complete|plans>
  deconflict negotiate <invite|ask|resolve|defer|answer>
                                             negotiate access and commitments
  deconflict message <register|send|inbox|watch|ack|presence>
                                             interoperable agent mailbox
  deconflict cooler  <rooms|say|ask|need|offer|note|status|roll|needs|…>
                                             the watercooler: questions, needs, heads-ups
  deconflict mcp                              MCP server for Claude Code and Codex

Account (registries with accounts enabled)
  deconflict login   [--server URL]          sign in through a browser, once
  deconflict logout  [--all]
  deconflict whoami                          who this machine is, and what is enabled
  deconflict token   <list|create|revoke>    personal API tokens for agents

Work tracker
  deconflict task    <resolve|search>        look up a work item before claiming it
  deconflict plugins <list|providers|test|deliveries|retry>

Everything else
  deconflict settings [--json]               what the operator has configured
  deconflict install [--agent claude|codex|all] [--scope project|user] [--dry-run]
                                             hooks + skill, for the agents you run
  deconflict uninstall [--agent …] [--scope …] [--dry-run]
                                             take out only what install put in
  deconflict update  [--check] [--dry-run] [--version X.Y.Z]
                                             upgrade through whatever installed it, then
                                             refresh the skills, hooks and MCP entries
  deconflict hook    <session-start|pre-tool|user-prompt>
  deconflict version

Store: $DECONFLICT_STORE (file:/path or http://host:port), default local file.

Running a registry is the deconflict-server binary's job: serve, genkey and
rotate-key live there, because each of them needs a database credential.
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
	case "amend":
		err = cmdAmend(rest, stdout)
	case "status":
		err = cmdStatus(rest, stdout)
	case "reconcile":
		err = cmdReconcile(rest, stdout)
	case "resource", "resources":
		err = cmdResource(rest, stdout)
	case "lock":
		err = cmdLock(rest, stdout)
	case "unlock":
		err = cmdUnlock(rest, stdout)
	case "negotiate", "coordination":
		err = cmdNegotiate(rest, stdout)
	case "message", "messages", "agent":
		err = cmdMessage(rest, stdout)
	case "cooler", "watercooler":
		err = cmdCooler(rest, stdout)
	case "mcp":
		err = cmdMCP(rest, os.Stdin, stdout)
	case "settings":
		err = cmdSettings(rest, stdout)
	case "serve":
		err = movedToServer(cmd)
	case "hook":
		err = cmdHook(rest, stdout)
	case "install":
		err = cmdInstall(rest, stdout)
	case "update", "upgrade":
		err = cmdUpdate(rest, stdout, stderr)
	case "uninstall":
		err = cmdUninstall(rest, stdout)
	case "login":
		err = cmdLogin(rest, stdout)
	case "logout":
		err = cmdLogout(rest, stdout)
	case "whoami":
		err = cmdWhoami(rest, stdout)
	case "token", "tokens":
		err = cmdToken(rest, stdout)
	case "plugins", "integrations":
		err = cmdPlugins(rest, stdout)
	case "task", "tasks":
		err = cmdTask(rest, stdout)
	case "genkey", "rotate-key":
		err = movedToServer(cmd)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "deconflict %s\n", Version)
		return 0
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

func openStore(dsn string) (client.Store, error) { return client.Open(dsn) }

// encodeJSON is how every --json flag in this package answers. One indentation,
// decided once: three copies of this under three names had grown up across the
// package, and a machine-readable output that formats itself differently
// depending on which subcommand produced it is one nobody can diff.
func encodeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// resolveClaimID works out which claim a command is about: the one named on the
// command line, before or after the flags, or failing that the one this
// worktree recorded when it was announced.
func resolveClaimID(id string, fs *flag.FlagSet, dir string) (string, error) {
	if id == "" {
		id = fs.Arg(0)
	}
	if id == "" {
		id = readCurrent(dir)
	}
	if id == "" {
		return "", fmt.Errorf("no claim id given and none recorded for this worktree")
	}
	return id, nil
}

// takeID pulls a leading positional claim id out of the argument list.
//
// Go's flag package stops parsing at the first non-flag argument, so
// `claims release <id> --reason abandoned` would parse zero flags and silently
// record the default reason. Silently storing the wrong reason is worse than
// erroring, so the id is removed before the flags are parsed.
func takeID(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// agentID is who this process acts as, unless DECONFLICT_AGENT says otherwise.
//
// The default is user@host/<worktree>/<runtime>. It was user@host, which named
// every session on a machine the same agent: on a client VM where each session
// ran as the same Unix user, one session could not settle another's question
// (the registry saw an agent answering itself), and a watercooler post never
// reached the other sessions because nobody is delivered their own posts.
//
// The worktree is what separates agents working side by side, and unlike a
// session id it survives a restart, so the claims, mailbox and questions an
// agent leaves behind are still its own when it comes back. The runtime is
// there because Claude Code and Codex may share a worktree. Both parts are
// read the same way from a hook and from a command the agent runs, which is
// what keeps the identity a hook registers equal to the one the agent uses.
func agentID() string {
	if v := os.Getenv("DECONFLICT_AGENT"); v != "" {
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
	id := user + "@" + host
	cwd, _ := os.Getwd()
	if root := gitinfo.Root(cwd); root != "" {
		id += "/" + filepath.Base(root)
	}
	switch rt := runtimeName(); rt {
	case "agent":
	case "claude-code":
		id += "/claude"
	default:
		id += "/" + rt
	}
	return id
}

// currentPath is where this worktree remembers its own claim id, so release
// and renew never need one typed. Per-worktree by construction: `git rev-parse
// --git-dir` differs for every worktree of a repo.
func currentPath(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--absolute-git-dir").Output()
	if err == nil {
		if g := strings.TrimSpace(string(out)); g != "" {
			return filepath.Join(g, "deconflict-current")
		}
	}
	return filepath.Join(filepath.Dir(client.DefaultPath()), "current")
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
	uses := fs.String("uses", "", "comma-separated globs this task depends on without editing (\"<repo>:<glob>\" for another repo)")
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
		Uses:      claim.NormalizeUses(splitList(*uses)),
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
	var dependencies, dependents []claim.Dependency
	if evs, err := st.Events(); err == nil {
		all := claim.Fold(evs)
		conflicts = claim.FindOverlaps(all, c.Repo, c.Paths, now, nil, cfg.IgnorePaths)
		dependencies = claim.DependenciesOf(all, c, now)
		dependents = claim.DependentsOf(all, c, now)
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
	notes := claimNotes(st, c.Repo, c.Paths)
	needs := claimNeeds(st, c.Repo, c.Paths)

	if *asJSON {
		return encodeJSON(out, map[string]any{"claim": c, "conflicts": conflicts, "dependencies": dependencies, "dependents": dependents, "heads_up": notes, "needs": needs})
	}
	fmt.Fprintf(out, "claimed %s  %s  (lease %s)\n", c.ID, strings.Join(c.Paths, ", "), *ttl)
	if len(c.Uses) > 0 {
		fmt.Fprintf(out, "  uses %s\n", strings.Join(c.Uses, ", "))
	}
	// Heads-ups are read, not contended: they never change the exit code.
	if len(notes) > 0 {
		fmt.Fprintln(out)
		fmt.Fprint(out, renderNotes(notes))
	}
	if len(needs) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Other agents want work done on the ground you just claimed:")
		fmt.Fprint(out, renderNeeds(needs, now))
		fmt.Fprintln(out, "If one is within what you are doing, offer: `deconflict cooler offer <need> --body '<how>'`.")
	}
	if len(conflicts) == 0 && len(dependencies) == 0 && len(dependents) == 0 {
		fmt.Fprintln(out, "no overlapping claims.")
		return nil
	}
	if len(conflicts) > 0 {
		fmt.Fprintln(out)
		fmt.Fprint(out, claim.Render(conflicts, now))
	}
	if deps := claim.RenderDependencies(dependencies, dependents, now); deps != "" {
		fmt.Fprintln(out)
		fmt.Fprint(out, deps)
	}
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
	all := claim.Fold(evs)
	conflicts := claim.FindOverlaps(all, gitinfo.Repo(cwd), claim.NormalizeAll(list), now, mine, st.Settings().IgnorePaths)
	// Who would be broken by changing these paths, even though none of them
	// edits here. The probe stands in for a claim on exactly these paths.
	probe := claim.Claim{Repo: gitinfo.Repo(cwd), Agent: agentID(), Paths: claim.NormalizeAll(list)}
	var dependents []claim.Dependency
	for _, d := range claim.DependentsOf(all, probe, now) {
		if !mine[d.User.ID] {
			dependents = append(dependents, d)
		}
	}
	if *asJSON {
		// The bare list of overlaps is a shape callers already parse, so
		// dependents are not added to it; they are in the text output.
		return encodeJSON(out, conflicts)
	}
	if len(conflicts) == 0 && len(dependents) == 0 {
		if !*quiet {
			fmt.Fprintln(out, "no overlapping claims.")
		}
		return nil
	}
	if len(conflicts) > 0 {
		fmt.Fprint(out, claim.Render(conflicts, now))
	}
	if deps := claim.RenderDependencies(nil, dependents, now); deps != "" {
		if len(conflicts) > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprint(out, deps)
	}
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
		return encodeJSON(out, sel)
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

func loadOne(st client.Store, id string) (claim.Claim, error) {
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
	id, args := takeID(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	id, err := resolveClaimID(id, fs, cwd)
	if err != nil {
		return err
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
	id, args := takeID(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	id, err := resolveClaimID(id, fs, cwd)
	if err != nil {
		return err
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

// cmdAmend corrects a claim in place.
//
// A claim is a prediction made before the work, and the work is what tells you
// the prediction was wrong: a command needs a flag, the flag needs help text,
// the help text lives in a file you never thought about. Until now the only way
// to say so was to release the claim and announce a new one, which cost the work
// its identity — one agent went through three ids in seven minutes correcting
// itself, and an audit trail of three claims reads as three pieces of work.
//
// The log stays append-only. An amendment is an event like any other; folding it
// replaces the declared area and leaves everything else — the id, the lease, the
// history — alone. Paths are replaced rather than merged, because a correction
// is sometimes narrower than the guess that preceded it.
func cmdAmend(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("amend", flag.ContinueOnError)
	paths := fs.String("paths", "", "the area this claim actually covers, comma separated")
	not := fs.String("not", "", "paths inside that area you are still not touching")
	what := fs.String("what", "", "restate what the work is")
	why := fs.String("why", "", "restate why")
	iface := fs.String("interface", "", "restate the interface changes")
	uses := fs.String("uses", "", "replace what this work depends on without editing")
	dsn := fs.String("store", "", "store DSN")
	id, args := takeID(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	id, err := resolveClaimID(id, fs, cwd)
	if err != nil {
		return err
	}
	if *paths == "" && *not == "" && *what == "" && *why == "" && *iface == "" && *uses == "" {
		return fmt.Errorf("nothing to amend: pass at least one of --paths, --not, --uses, --what, --why or --interface")
	}
	st, err := openStore(*dsn)
	if err != nil {
		return err
	}
	c, err := loadOne(st, id)
	if err != nil {
		return err
	}
	if c.Released != nil {
		return fmt.Errorf("claim %s is already released; announce a new one", id)
	}
	before := len(c.Paths)
	if *paths != "" {
		c.Paths = claim.NormalizeAll(splitList(*paths))
	}
	if *not != "" {
		c.NotPaths = claim.NormalizeAll(splitList(*not))
	}
	if *what != "" {
		c.What = strings.TrimSpace(*what)
	}
	if *why != "" {
		c.Why = strings.TrimSpace(*why)
	}
	if *iface != "" {
		c.Interface = strings.TrimSpace(*iface)
	}
	if *uses != "" {
		c.Uses = claim.NormalizeUses(splitList(*uses))
	}
	if err := st.Append(claim.Event{Op: "amend", TS: time.Now().UTC(), Claim: c}); err != nil {
		return err
	}
	fmt.Fprintf(out, "amended %s  %s\n", id, strings.Join(c.Paths, ", "))
	if *paths != "" && len(c.Paths) != before {
		fmt.Fprintf(out, "  declared area went from %d to %d path(s); the claim, its lease and its history are unchanged\n", before, len(c.Paths))
	}
	// An amendment can create an overlap that did not exist when the claim was
	// first made, and that is exactly the thing worth saying out loud.
	if evs, err := st.Events(); err == nil {
		all := claim.Fold(evs)
		conflicts := claim.FindOverlaps(all, c.Repo, c.Paths, time.Now().UTC(),
			map[string]bool{c.ID: true}, st.Settings().IgnorePaths)
		if len(conflicts) > 0 {
			fmt.Fprintf(out, "\n%s", claim.Render(conflicts, time.Now().UTC()))
		}
		now := time.Now().UTC()
		if deps := claim.RenderDependencies(claim.DependenciesOf(all, c, now), claim.DependentsOf(all, c, now), now); deps != "" {
			fmt.Fprintf(out, "\n%s", deps)
		}
	}
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
	// Measured from the branch tip this claim was made at, not from the
	// published base.
	//
	// "origin/main...HEAD" resolves its merge base to origin/main whenever the
	// branch descends from it, so on a branch stacked on unmerged work the
	// diff is the whole stack: on one real repository that reported 28 files
	// outside a claim, of which the agent had touched one. The signal the
	// report exists to give was buried in work somebody else had already done.
	//
	// The claim recorded where the branch stood when it was announced, which
	// is exactly the question being asked — what have I touched since I said
	// what I would touch. Falls back to the base for claims made before this
	// was recorded, and for a worktree where that commit no longer resolves.
	since := base
	if c.HeadSHA != "" && gitinfo.Resolves(cwd, c.HeadSHA) {
		since = c.HeadSHA
	}
	changed := gitinfo.ChangedPaths(cwd, since)
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
		fmt.Fprintln(out, "  Amend the claim so the announcement keeps matching reality:")
		fmt.Fprintf(out, "    deconflict amend --paths '%s'\n", strings.Join(append(append([]string{}, c.Paths...), outside...), ","))
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
		return fmt.Errorf("no published base branch found (set DECONFLICT_BASE)")
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
		return encodeJSON(out, cfg)
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
