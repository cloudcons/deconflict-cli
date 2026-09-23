package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/claim"
	"github.com/cloudcons/deconflict-cli/pkg/protocol"
)

const resourceUsage = `deconflict resource — shared resources and advisory locks on them

  list                       every resource, and who holds it right now
  add <name>                 add a resource to the catalog [--kind env] [--description …]
  remove <name>              remove a resource and its locks (admins)
  lock <name>                same as ` + "`deconflict lock`" + `
  unlock <name>              same as ` + "`deconflict unlock`" + `
  release <lock-id>          release one lock by id — yours, or anyone's as an admin
  renew <lock-id> [--ttl 2h] extend a lock

A lock never blocks anybody. Taking one that someone else holds succeeds, names
them, and exits 3 — the same contract as an overlapping claim.
`

func cmdResource(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, resourceUsage)
		return nil
	}
	switch args[0] {
	case "list", "ls":
		return resourceList(args[1:], out)
	case "add", "create":
		return resourceAdd(args[1:], out)
	case "remove", "rm", "delete":
		return resourceRemove(args[1:], out)
	case "lock":
		return cmdLock(args[1:], out)
	case "unlock":
		return cmdUnlock(args[1:], out)
	case "release":
		return resourceRelease(args[1:], out)
	case "renew":
		return resourceRenew(args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, resourceUsage)
		return nil
	default:
		return fmt.Errorf("unknown resource command %q", args[0])
	}
}

func resourcePath(name string, rest ...string) string {
	return "/v1/resources/" + url.PathEscape(strings.TrimSpace(name)) + strings.Join(rest, "")
}

// lockLine is one holder, the way an agent reads it: who, how, how long, why.
func lockLine(l protocol.ResourceLock, now time.Time) string {
	who := l.AgentID
	if l.DelegatedLogin != "" {
		who += " for " + l.DelegatedLogin
	}
	s := fmt.Sprintf("%s — %s, %s left", who, l.Mode, claim.Claim{Expires: l.ExpiresAt}.TTL(now))
	if l.Reason != "" {
		s += ": " + l.Reason
	}
	return s
}

func resourceList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("resource list", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	var items []protocol.Resource
	if err := h.JSON(http.MethodGet, "/v1/resources", nil, &items); err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(out, items)
	}
	if len(items) == 0 {
		fmt.Fprintln(out, "no shared resources yet — add one with `deconflict resource add staging --kind env`")
		return nil
	}
	now := time.Now().UTC()
	for _, r := range items {
		label := r.Name
		if r.Kind != "" {
			label += " (" + r.Kind + ")"
		}
		if len(r.Locks) == 0 {
			fmt.Fprintf(out, "%-28s free\n", label)
		} else {
			fmt.Fprintf(out, "%-28s held by %d\n", label, len(r.Locks))
		}
		if r.Description != "" {
			fmt.Fprintf(out, "    %s\n", r.Description)
		}
		for _, l := range r.Locks {
			fmt.Fprintf(out, "    [%s] %s\n", l.ID, lockLine(l, now))
		}
	}
	return nil
}

func resourceAdd(args []string, out io.Writer) error {
	name, args := takeID(args)
	fs := flag.NewFlagSet("resource add", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	kind := fs.String("kind", "", "env, database, service, … (free-form)")
	desc := fs.String("description", "", "what it is and where it lives")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: deconflict resource add <name> [--kind env] [--description …]")
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	var r protocol.Resource
	if err := h.JSON(http.MethodPost, "/v1/resources", protocol.ResourceInput{Name: name, Kind: *kind, Description: *desc}, &r); err != nil {
		return err
	}
	fmt.Fprintf(out, "added %s\n", r.Name)
	return nil
}

func resourceRemove(args []string, out io.Writer) error {
	name, args := takeID(args)
	fs := flag.NewFlagSet("resource remove", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: deconflict resource remove <name>")
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	if err := h.JSON(http.MethodDelete, resourcePath(name), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", name)
	return nil
}

// cmdLock takes an advisory lock. Contention is reported and exits 3; it is
// never a refusal.
func cmdLock(args []string, out io.Writer) error {
	name, args := takeID(args)
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	agent := fs.String("agent", agentID(), "agent id holding the lock")
	shared := fs.Bool("shared", false, "use it alongside others (tests against staging) rather than alone")
	mode := fs.String("mode", "", "exclusive (default) or shared")
	reason := fs.String("reason", "", "what you are doing with it — shown to anyone who contends")
	ttl := fs.String("ttl", "", "lease, e.g. 45m (default and ceiling are the organization's lease policy)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: deconflict lock <resource> [--shared] [--reason …] [--ttl 1h]")
	}
	m := protocol.LockMode(strings.TrimSpace(*mode))
	if *shared {
		m = protocol.Shared
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	var res protocol.LockResult
	if err := h.JSON(http.MethodPost, resourcePath(name, "/locks"), protocol.LockInput{AgentID: *agent, Mode: m, Reason: *reason, TTL: *ttl}, &res); err != nil {
		return err
	}
	if *asJSON {
		if err := encodeJSON(out, res); err != nil {
			return err
		}
	} else {
		fmt.Fprint(out, renderLock(res, time.Now().UTC()))
	}
	if len(res.Conflicts) > 0 {
		return exitCode{3}
	}
	return nil
}

func renderLock(res protocol.LockResult, now time.Time) string {
	var b strings.Builder
	l := res.Lock
	fmt.Fprintf(&b, "locked %s %s  (%s, lease %s)\n", l.Resource, l.ID, l.Mode, claim.Claim{Expires: l.ExpiresAt}.TTL(now))
	if len(res.Conflicts) == 0 {
		b.WriteString("nobody else holds it.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\n%d other holder(s) contend with this:\n", len(res.Conflicts))
	for _, c := range res.Conflicts {
		fmt.Fprintf(&b, "  [%s] %s\n", c.ID, lockLine(c, now))
	}
	b.WriteString("\nThis is advisory — you were not blocked. Before doing anything that would break " +
		"their use of it, coordinate: `deconflict message send --to <agent> --kind resource.contended --body …`, " +
		"or release yours with `deconflict unlock " + l.Resource + "`.\n")
	return b.String()
}

func cmdUnlock(args []string, out io.Writer) error {
	name, args := takeID(args)
	fs := flag.NewFlagSet("unlock", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	agent := fs.String("agent", agentID(), "release this agent's locks")
	all := fs.Bool("all", false, "release every lock your account holds on it, whichever agent took it")
	reason := fs.String("reason", "", "why, for the record")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name == "" {
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("usage: deconflict unlock <resource> [--all] [--reason …]")
	}
	in := protocol.UnlockInput{AgentID: *agent, Reason: *reason}
	if *all {
		in.AgentID = ""
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	var released []protocol.ResourceLock
	if err := h.JSON(http.MethodPost, resourcePath(name, "/unlock"), in, &released); err != nil {
		return err
	}
	if len(released) == 0 {
		who := "this agent (" + in.AgentID + ")"
		if in.AgentID == "" {
			who = "your account"
		}
		fmt.Fprintf(out, "%s held no lock on %s — nothing to release\n", who, name)
		return nil
	}
	for _, l := range released {
		fmt.Fprintf(out, "released %s %s\n", l.Resource, l.ID)
	}
	return nil
}

func resourceRelease(args []string, out io.Writer) error {
	id, args := takeID(args)
	fs := flag.NewFlagSet("resource release", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	reason := fs.String("reason", "", "why, for the record")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" {
		id = fs.Arg(0)
	}
	if id == "" {
		return fmt.Errorf("usage: deconflict resource release <lock-id> [--reason …]")
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	var l protocol.ResourceLock
	if err := h.JSON(http.MethodPost, "/v1/locks/"+url.PathEscape(id)+"/release", protocol.UnlockInput{Reason: *reason}, &l); err != nil {
		return err
	}
	fmt.Fprintf(out, "released %s %s (%s)\n", l.Resource, l.ID, l.AgentID)
	return nil
}

func resourceRenew(args []string, out io.Writer) error {
	id, args := takeID(args)
	fs := flag.NewFlagSet("resource renew", flag.ContinueOnError)
	dsn := fs.String("store", "", "store DSN")
	ttl := fs.String("ttl", "", "new lease from now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" {
		id = fs.Arg(0)
	}
	if id == "" {
		return fmt.Errorf("usage: deconflict resource renew <lock-id> [--ttl 1h]")
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	var l protocol.ResourceLock
	if err := h.JSON(http.MethodPost, "/v1/locks/"+url.PathEscape(id)+"/renew", protocol.RenewInput{TTL: *ttl}, &l); err != nil {
		return err
	}
	fmt.Fprintf(out, "renewed %s %s, %s left\n", l.Resource, l.ID, claim.Claim{Expires: l.ExpiresAt}.TTL(time.Now().UTC()))
	return nil
}

// resourceContext is what a session is told at start about shared resources:
// the ones other agents hold right now, and nothing when nobody holds anything.
// Every failure is silence — a registry without resources, an older registry
// without the route, a file store — for the same reason as every other hook.
func resourceContext(dsn string) string {
	h, err := negotiationClient(dsn)
	if err != nil {
		return ""
	}
	var items []protocol.Resource
	if err := h.JSON(http.MethodGet, "/v1/resources", nil, &items); err != nil {
		return ""
	}
	me := agentID()
	now := time.Now().UTC()
	var lines []string
	for _, r := range items {
		for _, l := range r.Locks {
			if l.AgentID == me {
				continue
			}
			lines = append(lines, fmt.Sprintf("  %s: %s", r.Name, lockLine(l, now)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Shared resources other agents hold right now (advisory locks):\n")
	for i, line := range lines {
		if i == 8 {
			fmt.Fprintf(&b, "  ...and %d more (`deconflict resource list`).\n", len(lines)-i)
			break
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nBefore deploying to, migrating, or resetting one of these, take a lock yourself " +
		"(`deconflict lock <name> --reason …`, or `--shared` if you only use it) — it tells you who you would disturb.\n")
	return b.String()
}
