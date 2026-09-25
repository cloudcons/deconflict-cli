package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// `deconflict install` — wire the hooks and the agent instructions into a
// repository, for whichever agents are actually in use. `deconflict uninstall`
// takes them out again.
//
// This exists because the alternative is a README section, and a README section
// is adopted by whoever reads it. The registry's picture of who is working where
// is only as complete as its adoption, so the install has to be one command
// rather than four files to hand-edit.
//
// Every file it touches belongs to somebody else — the user, Claude Code, Codex,
// Orca — so four rules govern everything below.
//
// It merges, never overwrites. A settings.json holds somebody's whole workflow;
// arriving and replacing it would be a fair description of the problem this
// tool exists to prevent. Every edit reads what is there, changes only its own
// entries, and leaves the rest byte-for-byte alone where it can.
//
// It refuses rather than guesses. A file it cannot read with certainty is not
// edited; the install stops and prints what to add by hand. A wrong guess in
// config.toml stops Codex from starting, which is a worse outcome than a
// paragraph asking the user to paste three lines.
//
// It is all or nothing. Every file is worked out before any is written, so a
// refusal on the fourth file does not leave the first three half-installed.
// Writes go through a temporary file and a rename, and the previous version of
// each file it changes is kept beside it as <name>.deconflict.bak.
//
// It is idempotent and re-runnable. Its own hooks are recognised by the program
// they run, so a second install upgrades a stale binary path instead of
// appending a duplicate hook that then fires twice.

type installChange struct {
	path   string
	what   string
	action string
}

// refusal is a file the installer would not edit, with what to add by hand.
type refusal struct {
	path, what, reason, snippet string
}

// fileEdit is one file's planned content. original is what was on disk when it
// was first read; content is what it will be once every edit to it is applied.
type fileEdit struct {
	path      string
	existed   bool
	original  string
	content   string
	mode      os.FileMode
	remove    bool
	removeDir bool // uninstall: also remove the (then empty) directory
	backup    bool
}

func (f *fileEdit) dirty() bool {
	if f.remove {
		return f.existed
	}
	return f.content != f.original
}

type installPlan struct {
	uninstall bool
	dry       bool
	files     map[string]*fileEdit
	order     []string
	changes   []installChange
	refusals  []refusal
	notes     []string
	backups   []string
}

func newInstallPlan(uninstall, dry bool) *installPlan {
	return &installPlan{uninstall: uninstall, dry: dry, files: map[string]*fileEdit{}}
}

// file returns the planned state of a file, reading it the first time. Two
// edits to one file — config.toml gets the feature flag and the MCP entry — see
// each other's work.
func (p *installPlan) file(path string) (*fileEdit, error) {
	if f, ok := p.files[path]; ok {
		return f, nil
	}
	f := &fileEdit{path: path, mode: 0o644}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		f.existed = true
		f.original = string(raw)
		f.backup = true
		if st, err := os.Stat(path); err == nil {
			f.mode = st.Mode().Perm()
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	f.content = f.original
	p.files[path] = f
	p.order = append(p.order, path)
	return f, nil
}

// change records one logical edit and the word the summary uses for it. The
// dry-run word mirrors the real one — "would write" against "wrote" — so a dry
// run reads as a prediction of the run it is predicting.
func (p *installPlan) change(f *fileEdit, after, what string) {
	before := f.content
	var action string
	switch {
	case after == before && p.uninstall:
		action = "not present"
	case after == before:
		action = "already set"
	case p.uninstall:
		action = "removed"
	case before == "" && !f.existed:
		action = "wrote"
	default:
		action = "updated"
	}
	if p.dry && after != before {
		action = map[string]string{"removed": "would remove", "wrote": "would write", "updated": "would update"}[action]
	}
	f.content = after
	p.changes = append(p.changes, installChange{f.path, what, action})
}

func (p *installPlan) removeFile(f *fileEdit, what string) {
	f.remove = true
	action := "removed"
	if p.dry {
		action = "would remove"
	}
	p.changes = append(p.changes, installChange{f.path, what, action})
}

func (p *installPlan) left(path, what, why string) {
	p.changes = append(p.changes, installChange{path, what, "left as is"})
	p.notes = append(p.notes, why)
}

func (p *installPlan) refuse(path, what, reason, snippet string) {
	p.refusals = append(p.refusals, refusal{path, what, reason, snippet})
}

// commit writes the plan. It first checks that nothing changed underneath it:
// Claude Code rewrites ~/.claude.json while it runs, and writing a version read
// a moment earlier would undo whatever Claude Code just saved.
func (p *installPlan) commit() error {
	var dirty []*fileEdit
	for _, path := range p.order {
		if f := p.files[path]; f.dirty() {
			dirty = append(dirty, f)
		}
	}
	for _, f := range dirty {
		raw, err := os.ReadFile(f.path)
		now, existsNow := string(raw), err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if existsNow != f.existed || now != f.original {
			return fmt.Errorf("%s changed while this ran; nothing was written — run it again", f.path)
		}
	}
	for _, f := range dirty {
		if f.existed && f.backup {
			bak := f.path + ".deconflict.bak"
			if err := atomicWrite(bak, []byte(f.original), f.mode); err != nil {
				return fmt.Errorf("backing up %s: %w", f.path, err)
			}
			p.backups = append(p.backups, bak)
		}
		if f.remove {
			if err := os.Remove(resolved(f.path)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if f.removeDir {
				_ = os.Remove(filepath.Dir(f.path)) // only succeeds if it is empty
			}
			continue
		}
		if err := atomicWrite(f.path, []byte(f.content), f.mode); err != nil {
			return err
		}
	}
	return nil
}

// resolved follows a symlink to the file it names. Dotfiles kept in a
// repository and linked into place are common, and renaming over the link
// would quietly replace it with a copy the repository no longer sees.
func resolved(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// atomicWrite replaces a file in one step, so an agent starting at the wrong
// moment reads the old file or the new one and never half of each.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	target := resolved(path)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// ---------- the command ----------

func cmdInstall(args []string, out io.Writer) error   { return runInstaller(args, out, false) }
func cmdUninstall(args []string, out io.Writer) error { return runInstaller(args, out, true) }

func runInstaller(args []string, out io.Writer, uninstall bool) error {
	name := "install"
	if uninstall {
		name = "uninstall"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	agent := fs.String("agent", "auto", "claude|codex|all|auto (auto picks the agents it finds)")
	scope := fs.String("scope", "project", "project (this repo) or user (every repo you open)")
	dir := fs.String("dir", ".", "repository to "+name+" into")
	withSkill := fs.Bool("skill", true, "include the skill that teaches the workflow (Claude Code and Codex)")
	withDoc := fs.Bool("doc", true, "include the announce-yourself rule in CLAUDE.md / AGENTS.md")
	dry := fs.Bool("dry-run", false, "print what would change and write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// An unrecognised scope used to fall through to project scope, so a typo
	// wrote into the current directory what was meant for the home directory.
	if *scope != "project" && *scope != "user" {
		return fmt.Errorf("unknown --scope %q (project or user)", *scope)
	}

	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no home directory: %w", err)
	}
	targets, err := resolveAgents(*agent, home)
	if err != nil {
		return err
	}
	bin := hookBinary()
	user := *scope == "user"

	p := newInstallPlan(uninstall, *dry)
	for _, a := range targets {
		switch a {
		case "claude":
			// With --scope user nothing is written under the current directory:
			// the point of user scope is every repository, and dropping a
			// .mcp.json and a CLAUDE.md into whichever one you ran it from is a
			// side effect nobody asked for.
			settings, mcp, skill, guidance :=
				filepath.Join(root, ".claude", "settings.json"),
				filepath.Join(root, ".mcp.json"),
				filepath.Join(root, ".claude", "skills", "deconflict", "SKILL.md"),
				filepath.Join(root, "CLAUDE.md")
			if user {
				settings, mcp, skill, guidance =
					filepath.Join(home, ".claude", "settings.json"),
					filepath.Join(home, ".claude.json"), // user-scoped MCP servers live here
					filepath.Join(home, ".claude", "skills", "deconflict", "SKILL.md"),
					filepath.Join(home, ".claude", "CLAUDE.md")
			}
			p.hooks(settings, bin, claudeEditMatcher)
			p.jsonMCP(mcp, bin)
			if *withSkill {
				p.skill(skill)
			}
			if *withDoc {
				p.guidance(guidance)
			}

		case "codex":
			// Codex reads hooks and MCP servers from the home directory only;
			// there is no per-repository form, so --scope does not apply to them.
			cfg := filepath.Join(home, ".codex", "config.toml")
			p.hooks(filepath.Join(home, ".codex", "hooks.json"), bin, codexEditMatcher)
			if !uninstall {
				// Left on by uninstall: other tools' hooks in the same file
				// (Orca's, for one) depend on the same switch.
				p.codexFeature(cfg)
			}
			p.codexMCP(cfg, bin)
			// Skills, unlike hooks, do have a per-repository form: Codex reads
			// them from .agents/skills, the shared location other agent tools
			// install into as well. Without this a Codex agent had the one-line
			// rule in AGENTS.md and never the workflow it points at.
			skill, guidance := filepath.Join(root, ".agents", "skills", "deconflict", "SKILL.md"), filepath.Join(root, "AGENTS.md")
			if user {
				skill, guidance = filepath.Join(home, ".agents", "skills", "deconflict", "SKILL.md"), filepath.Join(home, ".codex", "AGENTS.md")
			}
			if *withSkill {
				p.skill(skill)
			}
			if *withDoc {
				p.guidance(guidance)
			}
		}
	}

	for _, c := range p.changes {
		fmt.Fprintf(out, "  %-12s %s — %s\n", c.action, rel(root, home, c.path), c.what)
	}
	if len(p.refusals) > 0 {
		fmt.Fprintf(out, "\nNothing was written. %s cannot safely edit these, so they need a hand:\n", name)
		for _, r := range p.refusals {
			fmt.Fprintf(out, "\n  %s — %s\n    %s\n", rel(root, home, r.path), r.what, r.reason)
			if r.snippet != "" {
				verb := "Add this yourself, then run it again:"
				if uninstall {
					verb = "Remove this yourself:"
				}
				fmt.Fprintf(out, "    %s\n\n%s\n", verb, indent(r.snippet, "      "))
			}
		}
		return fmt.Errorf("%d file(s) need attention; nothing was written", len(p.refusals))
	}
	if *dry {
		fmt.Fprintln(out, "\n(dry run — nothing was written)")
		for _, n := range p.notes {
			fmt.Fprintf(out, "\n%s\n", n)
		}
		return nil
	}
	if err := p.commit(); err != nil {
		return err
	}
	if len(p.backups) > 0 {
		fmt.Fprintln(out, "\nThe previous version of each changed file is kept beside it:")
		for _, b := range p.backups {
			fmt.Fprintf(out, "  %s\n", rel(root, home, b))
		}
	}
	for _, n := range p.notes {
		fmt.Fprintf(out, "\n%s\n", n)
	}
	if uninstall {
		fmt.Fprintf(out, "\nUninstalled for: %s\n", strings.Join(targets, ", "))
		return nil
	}
	for _, target := range targets {
		if target != "codex" {
			continue
		}
		hooksPath := filepath.Join(home, ".codex", "hooks.json")
		if missing := untrustedCodexHooks(hooksPath, filepath.Join(home, ".codex", "config.toml")); len(missing) > 0 {
			fmt.Fprintf(out, "\nCodex will not run these hooks yet: %s\n", strings.Join(missing, ", "))
			fmt.Fprint(out, "  Codex trusts a hook command by hash and has no entry for ours, so it is\n"+
				"  installed, enabled, and silently skipped — you would get no overlap warning\n"+
				"  at all. Start codex once interactively and approve the hook when it asks, or\n"+
				"  for unattended runs pass --dangerously-bypass-hook-trust.\n")
		}
	}

	fmt.Fprintf(out, "\nInstalled for: %s\n", strings.Join(targets, ", "))
	fmt.Fprintln(out, "Sign in with `deconflict login` if this registry has accounts enabled;")
	fmt.Fprintln(out, "the hooks stay silent when nobody else is standing on your files.")
	return nil
}

func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

// resolveAgents decides which agents to install for. "auto" looks for evidence
// rather than asking: installing Codex hooks on a machine with no Codex writes a
// file nothing will ever read, and installing neither is worse than installing
// both.
func resolveAgents(choice, home string) ([]string, error) {
	switch choice {
	case "claude":
		return []string{"claude"}, nil
	case "codex":
		return []string{"codex"}, nil
	case "all":
		return []string{"claude", "codex"}, nil
	case "auto":
		var found []string
		if present("claude", filepath.Join(home, ".claude")) {
			found = append(found, "claude")
		}
		if present("codex", filepath.Join(home, ".codex")) {
			found = append(found, "codex")
		}
		if len(found) == 0 {
			// Neither on PATH is not proof of absence — an agent may be
			// installed for a different user, or run from a container. Claude
			// Code is the common case, so default to it and say so.
			return []string{"claude"}, nil
		}
		return found, nil
	default:
		return nil, fmt.Errorf("unknown --agent %q (claude, codex, all, auto)", choice)
	}
}

func present(bin, dir string) bool {
	if _, err := exec.LookPath(bin); err == nil {
		return true
	}
	_, err := os.Stat(dir)
	return err == nil
}

// hookBinary is what the hook command line will say. A bare name is nicer to
// read and survives the binary being upgraded in place — but only if the agent's
// PATH will actually find it, and a hook that cannot be found fails silently at
// session start, which is the worst way for this to be broken. So the bare name
// is used only when it resolves to this very executable.
func hookBinary() string {
	self, err := os.Executable()
	if err != nil {
		return "deconflict"
	}
	self, _ = filepath.EvalSymlinks(self)
	if p, err := exec.LookPath("deconflict"); err == nil {
		if p, err = filepath.EvalSymlinks(p); err == nil && p == self {
			return "deconflict"
		}
	}
	return self
}

// ---------- hooks ----------

const (
	claudeEditMatcher = "Edit|Write|NotebookEdit"
	// Codex's carries Edit|Write as well as its own apply_patch, so one file
	// serves a machine that runs both agents.
	codexEditMatcher = "apply_patch|Edit|Write"
)

var hookVerbs = []struct{ event, verb string }{
	{"SessionStart", "session-start"},
	{"PreToolUse", "pre-tool"},
}

// shellWord quotes a path for a hook command line, which the agent hands to a
// shell. An unquoted path with a space in it — "Application Support", a
// Windows user name — ran the wrong program or none.
func shellWord(s string) string {
	safe := true
	for _, c := range s {
		if !(c == '/' || c == '.' || c == '_' || c == '-' || c == '+' || c == ':' || c == '@' || c == '%' || c == ',' || c == '=' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			safe = false
			break
		}
	}
	switch {
	case safe && s != "":
		return s
	case !strings.ContainsAny(s, "\"$`!\\"):
		return `"` + s + `"`
	default:
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
}

func hookCommand(bin, verb, extra string) string {
	cmd := shellWord(bin) + " hook " + verb
	if extra != "" {
		cmd += " " + extra
	}
	return cmd
}

// firstWord splits a command line into its program, unquoted, and the rest.
func firstWord(cmd string) (string, string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", ""
	}
	if q := cmd[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(cmd[1:], q); end >= 0 {
			return cmd[1 : end+1], strings.TrimSpace(cmd[end+2:])
		}
		return "", ""
	}
	if i := strings.IndexAny(cmd, " \t"); i >= 0 {
		return cmd[:i], strings.TrimSpace(cmd[i:])
	}
	return cmd, ""
}

// ownHook reports whether a hook command is one this installer wrote for verb,
// and returns any flags after the verb so an update keeps them.
//
// Ownership is decided by the program, not the tail of the line. Matching on
// "ends in hook pre-tool" took over `lefthook hook pre-tool`, rewrote it to
// ours and changed its matcher; the user's hook was simply gone.
func ownHook(cmd, verb, bin string) (string, bool) {
	prog, rest := firstWord(cmd)
	if prog == "" {
		return "", false
	}
	base := prog[strings.LastIndexAny(prog, `/\`)+1:]
	if base != "deconflict" && base != "deconflict.exe" && prog != bin {
		return "", false
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 || fields[0] != "hook" || fields[1] != verb {
		return "", false
	}
	after := strings.TrimSpace(rest[strings.Index(rest, verb)+len(verb):])
	return after, true
}

func ownAnyHook(cmd, bin string) bool {
	for _, h := range hookVerbs {
		if _, ok := ownHook(cmd, h.verb, bin); ok {
			return true
		}
	}
	return false
}

// childObject returns o[key] as an object, creating it if asked. A value of
// any other type is an error: replacing somebody's "hooks" because it was not
// the shape we expected is how their hooks would be lost.
func childObject(o *jobj, key string, create bool) (*jobj, error) {
	v, ok := o.get(key)
	if !ok {
		if !create {
			return nil, nil
		}
		n := newJobj()
		o.set(key, n)
		return n, nil
	}
	obj, ok := v.(*jobj)
	if !ok {
		return nil, fmt.Errorf("%q is not a JSON object, which is the only shape this knows how to add to", key)
	}
	return obj, nil
}

func childArray(o *jobj, key string) ([]any, bool, error) {
	v, ok := o.get(key)
	if !ok {
		return nil, false, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, true, fmt.Errorf("%q is not a list, which is the only shape this knows how to add to", key)
	}
	return arr, true, nil
}

// hooks installs or removes the two hooks in an agent's JSON settings. Claude
// and Codex take the identical file shape and differ only in what they call an
// edit.
func (p *installPlan) hooks(path, bin, matcher string) {
	const what = "session-start + pre-write hooks"
	snippet := hookSnippet(bin, matcher)
	f, err := p.file(path)
	if err != nil {
		p.refuse(path, what, err.Error(), snippet)
		return
	}
	doc, err := parseJSONDoc(f.content)
	if err != nil {
		p.refuse(path, what, "cannot read it as JSON ("+err.Error()+"); fix or move it, or add the hooks by hand.", snippet)
		return
	}
	hooks, err := childObject(doc.root, "hooks", !p.uninstall)
	if err != nil {
		p.refuse(path, what, err.Error(), snippet)
		return
	}
	if hooks != nil {
		for _, h := range hookVerbs {
			m := ""
			if h.event == "PreToolUse" {
				m = matcher
			}
			if err := placeHook(hooks, h.event, m, bin, h.verb, !p.uninstall); err != nil {
				p.refuse(path, what, err.Error(), snippet)
				return
			}
		}
		if p.uninstall && len(hooks.keys) == 0 {
			doc.root.del("hooks")
		}
	}
	after := f.content
	if doc.changed() {
		after = doc.encode()
	}
	p.change(f, after, what)
}

func hookSnippet(bin, matcher string) string {
	entry := func(verb string) string {
		return `{"type": "command", "command": ` + string(jstr(hookCommand(bin, verb, ""))) + `, "timeout": 10}`
	}
	return `"hooks": {
  "SessionStart": [{"hooks": [` + entry("session-start") + `]}],
  "PreToolUse": [{"matcher": "` + matcher + `", "hooks": [` + entry("pre-tool") + `]}]
}`
}

// placeHook leaves exactly one copy of our hook for an event, in a group of its
// own, or none when uninstalling.
//
// Our hook is updated where it stands when it already has its own group, since
// Codex trusts hooks by their position and moving one makes it untrusted. One
// found in a group shared with somebody else's hooks is moved out rather than
// the group's matcher being widened to ours, which would have made their hook
// fire on edits it was never meant to see. Stale copies — an old binary path, a
// second install from a different directory — are collapsed into one, keeping
// any flags the first carried.
func placeHook(hooks *jobj, event, matcher, bin, verb string, keep bool) error {
	groups, exists, err := childArray(hooks, event)
	if err != nil {
		return err
	}
	type found struct {
		group, entry int
		dedicated    bool
	}
	var ours []found
	extra, haveExtra := "", false
	for gi, g := range groups {
		group, ok := g.(*jobj)
		if !ok {
			continue // not a shape we write, so not ours
		}
		entries, _, err := childArray(group, "hooks")
		if err != nil {
			continue
		}
		for ei, e := range entries {
			entry, ok := e.(*jobj)
			if !ok {
				continue
			}
			v, _ := entry.get("command")
			cmd, _ := jstring(v)
			if x, mine := ownHook(cmd, verb, bin); mine {
				ours = append(ours, found{gi, ei, len(entries) == 1})
				if !haveExtra {
					extra, haveExtra = x, true
				}
			}
		}
	}
	command := hookCommand(bin, verb, extra)

	keeper := -1
	if keep {
		for i, f := range ours {
			if f.dedicated {
				keeper = i
				break
			}
		}
	}
	drop := map[[2]int]bool{}
	for i, f := range ours {
		if i != keeper {
			drop[[2]int{f.group, f.entry}] = true
		}
	}

	var out []any
	for gi, g := range groups {
		group, ok := g.(*jobj)
		if !ok {
			out = append(out, g)
			continue
		}
		entries, _, err := childArray(group, "hooks")
		if err != nil {
			out = append(out, g)
			continue
		}
		if keeper >= 0 && ours[keeper].group == gi {
			entry := entries[ours[keeper].entry].(*jobj)
			entry.set("command", jstr(command))
			// The group is ours alone, so its matcher is ours to set.
			if matcher != "" {
				group.set("matcher", jstr(matcher))
			}
			out = append(out, group)
			continue
		}
		var kept []any
		removed := false
		for ei, e := range entries {
			if drop[[2]int{gi, ei}] {
				removed = true
				continue
			}
			kept = append(kept, e)
		}
		if !removed {
			out = append(out, g)
			continue
		}
		if len(kept) == 0 {
			continue // the group held only our hook
		}
		group.set("hooks", kept)
		out = append(out, group)
	}
	if keep && keeper < 0 {
		group := newJobj()
		if matcher != "" {
			group.set("matcher", jstr(matcher))
		}
		entry := newJobj()
		entry.set("type", jstr("command"))
		entry.set("command", jstr(command))
		entry.set("timeout", jnum(10))
		group.set("hooks", []any{entry})
		out = append(out, group)
	}

	switch {
	case len(out) > 0:
		hooks.set(event, out)
	case exists && len(groups) > 0:
		hooks.del(event) // it held only ours
	}
	return nil
}

// untrustedCodexHooks reports the hooks we installed that Codex will not run.
//
// Codex trusts a hook command by hash, keyed on where it sits in hooks.json:
// [hooks.state."<hooks.json>:<event>:<group>:<index>"]. We append ours as a new
// group, so on any machine that already has a hook for the same event ours
// lands at an index no trust entry covers. Codex then runs the trusted hook and
// ignores ours, and the install reports success over a hook that never fires.
//
// That is not hypothetical. A Codex agent edited a file another agent had
// claimed while this hook was installed, enabled, and generating exactly the
// right warning — which Codex discarded unread, because it was untrusted.
//
// We deliberately do not write the trust entry ourselves. That hash is Codex
// asking a person to vouch for a command before it runs on every edit, and a
// tool that forges its own entry has quietly helped itself to that. Reporting
// the gap is the honest thing this can do.
func untrustedCodexHooks(hooksPath, configPath string) []string {
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		return nil
	}
	doc, err := parseJSONDoc(string(raw))
	if err != nil {
		return nil
	}
	hooks, err := childObject(doc.root, "hooks", false)
	if err != nil || hooks == nil {
		return nil
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	trusted := string(config)
	bin := hookBinary()

	var missing []string
	for event, snake := range codexEvents {
		groups, _, _ := childArray(hooks, event)
		for i, g := range groups {
			group, ok := g.(*jobj)
			if !ok {
				continue
			}
			entries, _, _ := childArray(group, "hooks")
			for j, e := range entries {
				entry, ok := e.(*jobj)
				if !ok {
					continue
				}
				v, _ := entry.get("command")
				cmd, _ := jstring(v)
				if !ownAnyHook(cmd, bin) {
					continue // somebody else's hook; their trust is their business
				}
				key := fmt.Sprintf("%s:%s:%d:%d", hooksPath, snake, i, j)
				if !strings.Contains(trusted, key) {
					missing = append(missing, event)
				}
			}
		}
	}
	sort.Strings(missing)
	return missing
}

// codexEvents maps the hook event names we install to the snake_case form Codex
// uses in its trust keys.
var codexEvents = map[string]string{"SessionStart": "session_start", "PreToolUse": "pre_tool_use"}

// ---------- MCP ----------

// jsonMCP adds or removes our server in a Claude Code JSON file: .mcp.json for
// one repository, ~/.claude.json for every one. The second is a large file
// Claude Code owns and rewrites constantly; only mcpServers.deconflict in it
// is ever touched.
func (p *installPlan) jsonMCP(path, bin string) {
	const what = "Deconflict MCP mailbox"
	snippet := `"mcpServers": {
  "deconflict": {"type": "stdio", "command": ` + string(jstr(bin)) + `, "args": ["mcp"]}
}`
	f, err := p.file(path)
	if err != nil {
		p.refuse(path, what, err.Error(), snippet)
		return
	}
	doc, err := parseJSONDoc(f.content)
	if err != nil {
		p.refuse(path, what, "cannot read it as JSON ("+err.Error()+"); fix or move it, or add the server by hand.", snippet)
		return
	}
	servers, err := childObject(doc.root, "mcpServers", !p.uninstall)
	if err != nil {
		p.refuse(path, what, err.Error(), snippet)
		return
	}
	switch {
	case servers == nil:
	case p.uninstall:
		servers.del("deconflict")
		if len(servers.keys) == 0 {
			doc.root.del("mcpServers")
		}
	default:
		v, _ := servers.get("deconflict")
		entry, ok := v.(*jobj)
		if !ok {
			entry = newJobj()
			servers.set("deconflict", entry)
		}
		entry.set("type", jstr("stdio"))
		entry.set("command", jstr(bin))
		// Arguments after "mcp" are somebody's choice (a --store, say) and survive.
		if args, ok := entry.get("args"); !ok || !startsWithMCP(args) {
			entry.set("args", []any{jstr("mcp")})
		}
	}
	after := f.content
	if doc.changed() {
		after = doc.encode()
	}
	p.change(f, after, what)
}

func startsWithMCP(v any) bool {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return false
	}
	s, ok := jstring(arr[0])
	return ok && s == "mcp"
}

// ---------- Codex config.toml ----------

func codexMCPSnippet(bin string) string {
	return "[mcp_servers.deconflict]\ncommand = " + strconv.Quote(bin) + "\nargs = [\"mcp\"]"
}

func (p *installPlan) codexMCP(path, bin string) {
	const what = "Deconflict MCP mailbox"
	f, err := p.file(path)
	if err != nil {
		p.refuse(path, what, err.Error(), codexMCPSnippet(bin))
		return
	}
	doc, err := parseTOMLDoc(f.content)
	if err != nil {
		p.refuse(path, what, "cannot read it as TOML ("+err.Error()+").", codexMCPSnippet(bin))
		return
	}
	if p.uninstall {
		err = removeCodexMCP(doc)
	} else {
		err = setCodexMCP(doc, bin)
	}
	if err != nil {
		p.refuse(path, what, err.Error(), codexMCPSnippet(bin))
		return
	}
	p.change(f, doc.String(), what)
}

var mcpArgs = regexp.MustCompile(`^\[\s*"mcp"\s*[,\]]`)

// ourCodexTable finds [mcp_servers.deconflict], refusing every shape of it
// other than the one this writes: a single table with plain keys. Dotted keys
// and inline tables can define the same thing, and adding a table next to them
// is a duplicate definition Codex will not load.
func ourCodexTable(d *tomlDoc) (int, error) {
	ours := []string{"mcp_servers", "deconflict"}
	header, headers := -1, 0
	for i, st := range d.stmts {
		switch st.kind {
		case tomlTable:
			if pathEq(st.path, ours) {
				header, headers = i, headers+1
			}
		case tomlArrayTable:
			if pathHasPrefix(st.path, ours[:1]) {
				return 0, errors.New("mcp_servers is written as an array of tables, which this does not know how to edit.")
			}
		case tomlKeyValue:
			if pathEq(st.path, ours[:1]) {
				return 0, errors.New("mcp_servers is written as an inline table, so a [mcp_servers.deconflict] table cannot be added next to it.")
			}
			if len(st.table) < len(ours) && pathHasPrefix(st.path, ours) {
				return 0, errors.New("mcp_servers.deconflict is written with dotted keys or as an inline table, which this does not edit.")
			}
		}
	}
	if headers > 1 {
		return 0, errors.New("[mcp_servers.deconflict] appears more than once.")
	}
	return header, nil
}

func setCodexMCP(d *tomlDoc, bin string) error {
	header, err := ourCodexTable(d)
	if err != nil {
		return err
	}
	command := "command = " + strconv.Quote(bin)
	if header < 0 {
		lines := strings.Split(codexMCPSnippet(bin), "\n")
		if strings.TrimSpace(d.String()) != "" {
			lines = append([]string{""}, lines...)
		}
		d.splice(len(d.lines), len(d.lines)-1, lines)
		return nil
	}

	type edit struct {
		first, last int
		lines       []string
	}
	var edits []edit
	var inserts []string
	commandAt, argsAt, end := -1, -1, d.stmts[header].last
	for j := header + 1; j < len(d.stmts) && d.stmts[j].kind == tomlKeyValue; j++ {
		st := d.stmts[j]
		if len(st.key) == 1 {
			switch st.key[0] {
			case "command":
				commandAt = j
			case "args":
				argsAt = j
			}
		}
		end = st.last
	}
	if commandAt < 0 {
		inserts = append(inserts, command)
	} else if st := d.stmts[commandAt]; st.value != strconv.Quote(bin) {
		edits = append(edits, edit{st.first, st.last, []string{command}})
	}
	if argsAt < 0 {
		inserts = append(inserts, `args = ["mcp"]`)
	} else if st := d.stmts[argsAt]; !mcpArgs.MatchString(st.value) {
		edits = append(edits, edit{st.first, st.last, []string{`args = ["mcp"]`}})
	}
	if len(inserts) > 0 {
		edits = append(edits, edit{end + 1, end, inserts})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].first > edits[j].first })
	for _, e := range edits {
		d.splice(e.first, e.last, e.lines)
	}
	return nil
}

// removeCodexMCP takes out our table and any of its sub-tables, and the blank
// line the install put before it.
func removeCodexMCP(d *tomlDoc) error {
	if _, err := ourCodexTable(d); err != nil {
		return err
	}
	ours := []string{"mcp_servers", "deconflict"}
	for h := len(d.stmts) - 1; h >= 0; h-- {
		st := d.stmts[h]
		if st.kind != tomlTable || !pathHasPrefix(st.path, ours) {
			continue
		}
		first, last := st.first, d.sectionEnd(h)
		if first > 0 && strings.TrimSpace(d.lines[first-1]) == "" {
			first--
		}
		d.splice(first, last, nil)
	}
	return nil
}

// codexFeature turns Codex's hook engine on. Without it hooks.json is read by
// nobody and the install looks like it worked.
func (p *installPlan) codexFeature(path string) {
	const what = "codex_hooks feature"
	const snippet = "[features]\ncodex_hooks = true"
	f, err := p.file(path)
	if err != nil {
		p.refuse(path, what, err.Error(), snippet)
		return
	}
	doc, err := parseTOMLDoc(f.content)
	if err != nil {
		p.refuse(path, what, "cannot read it as TOML ("+err.Error()+").", snippet)
		return
	}
	state, err := enableCodexHooks(doc)
	if err != nil {
		p.refuse(path, what, err.Error(), snippet)
		return
	}
	if state == "off" {
		// Somebody turned it off on purpose, and flipping it back would turn
		// on every other tool's hooks in hooks.json along with ours.
		p.left(path, what, "Codex hooks are switched off (codex_hooks = false in "+path+"), so the\n"+
			"deconflict hooks will not run in Codex until you set it to true.")
		return
	}
	p.change(f, doc.String(), what)
}

var inlineCodexHooks = regexp.MustCompile(`(^|[{,\s])codex_hooks\s*=\s*(\w+)`)

// enableCodexHooks sets features.codex_hooks = true, reporting "on" when it is
// (or now will be) and "off" when the user has set it to anything else. Only the
// top-level [features] table counts; a profile's own features do not switch the
// engine on.
func enableCodexHooks(d *tomlDoc) (string, error) {
	feat, want := []string{"features"}, []string{"features", "codex_hooks"}
	var defs []tomlStmt
	var inline *tomlStmt
	header, headers, rootDotted := -1, 0, -1
	for i, st := range d.stmts {
		switch st.kind {
		case tomlTable:
			if pathEq(st.path, feat) {
				header, headers = i, headers+1
			}
		case tomlArrayTable:
			if pathHasPrefix(st.path, feat) {
				return "", errors.New("[[features]] is an array of tables, which Codex does not read.")
			}
		case tomlKeyValue:
			switch {
			case pathEq(st.path, want):
				defs = append(defs, st)
			case pathEq(st.path, feat):
				st := st
				inline = &st
			}
			if len(st.table) == 0 && len(st.key) > 1 && st.key[0] == "features" {
				rootDotted = i
			}
		}
	}
	switch {
	case len(defs) > 1:
		return "", errors.New("codex_hooks is set more than once.")
	case len(defs) == 1:
		if defs[0].value == "true" {
			return "on", nil
		}
		return "off", nil
	case inline != nil:
		if m := inlineCodexHooks.FindStringSubmatch(inline.value); m != nil {
			if m[2] == "true" {
				return "on", nil
			}
			return "off", nil
		}
		return "", errors.New("features is written as an inline table, which this does not edit.")
	case headers > 1:
		return "", errors.New("[features] appears more than once.")
	case headers == 1:
		at := d.stmts[header].last + 1
		d.splice(at, at-1, []string{"codex_hooks = true"})
	case rootDotted >= 0:
		// features defined by dotted keys cannot also be given a [features]
		// header, so the flag joins them in the same form.
		at := d.stmts[rootDotted].last + 1
		d.splice(at, at-1, []string{"features.codex_hooks = true"})
	default:
		lines := []string{"[features]", "codex_hooks = true"}
		if strings.TrimSpace(d.String()) != "" {
			lines = append([]string{""}, lines...)
		}
		d.splice(len(d.lines), len(d.lines)-1, lines)
	}
	return "on", nil
}

// ---------- skill and guidance ----------

// skillHandEdited reports whether a SKILL.md differs from the text its stamp
// says it was written from. Such a copy is backed up before being replaced and
// is never deleted by uninstall: it is somebody's work now, not ours.
func skillHandEdited(body string) bool {
	m := skillStamp.FindStringSubmatch(body)
	if m == nil {
		return true // written before skills were stamped, or not by us
	}
	sum := sha256.Sum256([]byte(strings.Replace(body, m[0]+"\n", "", 1)))
	return hex.EncodeToString(sum[:])[:12] != m[2]
}

func (p *installPlan) skill(path string) {
	const what = "coordination workflow skill"
	f, err := p.file(path)
	if err != nil {
		p.refuse(path, what, err.Error(), "")
		return
	}
	edited := f.existed && skillHandEdited(f.content)
	if !p.uninstall {
		f.backup = edited
		p.change(f, renderSkill(), what)
		return
	}
	if !f.existed {
		p.change(f, f.content, what)
		return
	}
	if edited {
		p.left(path, what, "Left "+path+" in place: it has been edited since deconflict wrote it.")
		return
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		p.left(path, what, "Left "+filepath.Dir(path)+" in place: it holds files deconflict did not write.")
		return
	}
	// A pristine copy is exactly what this binary can write again, so a backup
	// would only keep the directory from being removed.
	f.backup, f.removeDir = false, true
	p.removeFile(f, what)
}

const guidanceStart = "<!-- deconflict:start -->"
const guidanceEnd = "<!-- deconflict:end -->"

// guidance puts the announce-yourself rule where it is always in context.
//
// The skill is not enough on its own: a skill is loaded when the agent judges it
// relevant, and an agent that has not yet learned this repository has parallel
// writers cannot make that judgement. The rule that has to survive that gap is
// short, so it goes in the always-loaded file, and the procedure it points at
// stays in the skill.
//
// Fenced by markers so a re-install replaces its own block and nothing else.
// Markers that are missing, repeated or out of order are refused: guessing
// which text is ours there either deletes the user's or copies it again.
func (p *installPlan) guidance(path string) {
	what := filepath.Base(path) + " rule"
	block := guidanceStart + "\n" + guidanceMarkdown + guidanceEnd + "\n"
	f, err := p.file(path)
	if err != nil {
		p.refuse(path, what, err.Error(), block)
		return
	}
	body := f.content
	starts, ends := strings.Count(body, guidanceStart), strings.Count(body, guidanceEnd)
	s, e := strings.Index(body, guidanceStart), strings.Index(body, guidanceEnd)

	var updated string
	switch {
	case starts == 0 && ends == 0:
		switch {
		case p.uninstall:
			updated = body
		case body == "":
			updated = block
		default:
			if !strings.HasSuffix(body, "\n") {
				body += "\n"
			}
			updated = body + "\n" + block
		}
	case starts == 1 && ends == 1 && s < e:
		stop := e + len(guidanceEnd)
		switch {
		case strings.HasPrefix(body[stop:], "\r\n"):
			stop += 2
		case strings.HasPrefix(body[stop:], "\n"):
			stop++
		}
		if !p.uninstall {
			updated = body[:s] + block + body[stop:]
			break
		}
		head, tail := body[:s], body[stop:]
		updated = head + tail
		if tail == "" {
			// Undo the blank line the install put before the block.
			updated = strings.TrimRight(head, "\n")
			if updated != "" {
				updated += "\n"
			}
		}
		if strings.TrimSpace(updated) == "" {
			// The file held nothing but our block, so there is nothing of
			// anybody else's to back up.
			f.backup = false
			p.removeFile(f, what)
			return
		}
	default:
		p.refuse(path, what, fmt.Sprintf("its %s / %s markers are unbalanced or out of order (%d start, %d end), so which text is ours is a guess.",
			guidanceStart, guidanceEnd, starts, ends), block)
		return
	}
	p.change(f, updated, what)
}

// rel shortens paths for the summary — an absolute path repeated six times is
// noise, and ~ is the shortest true way to say "your home".
func rel(root, home, path string) string {
	if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	if r, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.Join("~", r)
	}
	return path
}
