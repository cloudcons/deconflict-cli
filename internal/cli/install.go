package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// `deconflict install` — wire the hooks and the agent instructions into a
// repository, for whichever agents are actually in use.
//
// This exists because the alternative is a README section, and a README section
// is adopted by whoever reads it. The registry's picture of who is working where
// is only as complete as its adoption, so the install has to be one command
// rather than four files to hand-edit.
//
// Two rules govern everything below.
//
// It merges, never overwrites. A settings.json holds somebody's whole workflow;
// arriving and replacing it would be a fair description of the problem this
// tool exists to prevent. Every write reads what is there, adds what is missing,
// and leaves the rest untouched.
//
// It is idempotent and re-runnable. Groups are recognised by the command they
// run, so a second install upgrades a stale binary path instead of appending a
// duplicate hook that then fires twice.

type installChange struct {
	path   string
	what   string
	action string // "wrote", "updated", "already set"
}

func cmdInstall(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(out)
	agent := fs.String("agent", "auto", "claude|codex|all|auto (auto installs for the agents it finds)")
	scope := fs.String("scope", "project", "project (this repo) or user (every repo you open)")
	dir := fs.String("dir", ".", "repository to install into")
	withSkill := fs.Bool("skill", true, "install the skill that teaches the claiming workflow")
	withDoc := fs.Bool("doc", true, "add the announce-yourself rule to CLAUDE.md / AGENTS.md")
	dry := fs.Bool("dry-run", false, "print what would change and write nothing")
	if err := fs.Parse(args); err != nil {
		return err
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

	var changes []installChange
	add := func(c installChange, err error) error {
		if err != nil {
			return err
		}
		changes = append(changes, c)
		return nil
	}

	for _, a := range targets {
		switch a {
		case "claude":
			settings := filepath.Join(root, ".claude", "settings.json")
			if *scope == "user" {
				settings = filepath.Join(home, ".claude", "settings.json")
			}
			if err := add(installClaudeHooks(settings, bin, *dry)); err != nil {
				return err
			}
			if *withSkill {
				skillDir := filepath.Join(root, ".claude", "skills", "deconflict")
				if *scope == "user" {
					skillDir = filepath.Join(home, ".claude", "skills", "deconflict")
				}
				if err := add(installSkill(filepath.Join(skillDir, "SKILL.md"), *dry)); err != nil {
					return err
				}
			}
			if *withDoc {
				if err := add(installGuidance(filepath.Join(root, "CLAUDE.md"), *dry)); err != nil {
					return err
				}
			}

		case "codex":
			// Codex reads hooks from the home directory only; there is no
			// per-repository form, so --scope does not apply to it.
			if err := add(installCodexHooks(filepath.Join(home, ".codex", "hooks.json"), bin, *dry)); err != nil {
				return err
			}
			if err := add(enableCodexHooks(filepath.Join(home, ".codex", "config.toml"), *dry)); err != nil {
				return err
			}
			if *withDoc {
				if err := add(installGuidance(filepath.Join(root, "AGENTS.md"), *dry)); err != nil {
					return err
				}
			}
		}
	}

	for _, c := range changes {
		fmt.Fprintf(out, "  %-12s %s — %s\n", c.action, rel(root, home, c.path), c.what)
	}
	if *dry {
		fmt.Fprintln(out, "\n(dry run — nothing was written)")
		return nil
	}
	fmt.Fprintf(out, "\nInstalled for: %s\n", strings.Join(targets, ", "))
	fmt.Fprintln(out, "Sign in with `deconflict login` if this registry has accounts enabled;")
	fmt.Fprintln(out, "the hooks stay silent when nobody else is standing on your files.")
	return nil
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

// ---------- Claude Code ----------

func installClaudeHooks(path, bin string, dry bool) (installChange, error) {
	doc, err := readJSONObject(path)
	if err != nil {
		return installChange{}, err
	}
	before := jsonString(doc)

	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	upsertHookGroup(hooks, "SessionStart", "", bin+" hook session-start")
	upsertHookGroup(hooks, "PreToolUse", "Edit|Write|NotebookEdit", bin+" hook pre-tool")
	doc["hooks"] = hooks

	return writeIfChanged(path, before, doc, "session-start + pre-write hooks", dry)
}

// upsertHookGroup adds one hook, or updates the one already installed.
//
// Ours is recognised by the verb it ends in, not by the whole command line —
// that is what lets an install after a move rewrite a stale absolute path
// instead of adding a second hook that runs the same check twice.
func upsertHookGroup(hooks map[string]any, event, matcher, command string) {
	verb := command[strings.LastIndex(command, " ")+1:]
	groups, _ := hooks[event].([]any)

	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := entry["command"].(string)
			if strings.HasSuffix(cmd, " hook "+verb) {
				entry["command"] = command
				if matcher != "" {
					group["matcher"] = matcher
				}
				return
			}
		}
	}

	group := map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
			"timeout": 10,
		}},
	}
	if matcher != "" {
		group["matcher"] = matcher
	}
	hooks[event] = append(groups, group)
}

// ---------- Codex ----------

func installCodexHooks(path, bin string, dry bool) (installChange, error) {
	doc, err := readJSONObject(path)
	if err != nil {
		return installChange{}, err
	}
	before := jsonString(doc)

	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	upsertHookGroup(hooks, "SessionStart", "", bin+" hook session-start")
	// apply_patch is how Codex describes an edit; Edit|Write are carried too so
	// one file serves a machine that runs both agents.
	upsertHookGroup(hooks, "PreToolUse", "apply_patch|Edit|Write", bin+" hook pre-tool")
	doc["hooks"] = hooks

	return writeIfChanged(path, before, doc, "session-start + pre-write hooks", dry)
}

var codexFeatures = regexp.MustCompile(`(?m)^\[features\]\s*$`)
var codexHooksSet = regexp.MustCompile(`(?m)^\s*codex_hooks\s*=`)

// enableCodexHooks turns the engine on. Without this the hooks file is read by
// nobody and the install looks like it worked.
//
// Edited as text rather than parsed: this is one boolean in a file the user owns
// and comments in, and a TOML round-trip would reformat all of it to add a line.
func enableCodexHooks(path string, dry bool) (installChange, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return installChange{}, err
	}
	body := string(raw)
	if codexHooksSet.MatchString(body) {
		return installChange{path, "codex_hooks feature", "already set"}, nil
	}

	if loc := codexFeatures.FindStringIndex(body); loc != nil {
		end := strings.Index(body[loc[1]:], "\n")
		at := loc[1]
		if end >= 0 {
			at += end + 1
		}
		body = body[:at] + "codex_hooks = true\n" + body[at:]
	} else {
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += "\n[features]\ncodex_hooks = true\n"
	}
	if dry {
		return installChange{path, "codex_hooks feature", "would set"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return installChange{}, err
	}
	return installChange{path, "codex_hooks feature", "updated"}, os.WriteFile(path, []byte(body), 0o644)
}

// ---------- skill and guidance ----------

func installSkill(path string, dry bool) (installChange, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return installChange{}, err
	}
	if string(existing) == skillMarkdown {
		return installChange{path, "claiming workflow skill", "already set"}, nil
	}
	if dry {
		return installChange{path, "claiming workflow skill", "would write"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return installChange{}, err
	}
	action := "wrote"
	if len(existing) > 0 {
		action = "updated"
	}
	return installChange{path, "claiming workflow skill", action}, os.WriteFile(path, []byte(skillMarkdown), 0o644)
}

const guidanceStart = "<!-- deconflict:start -->"
const guidanceEnd = "<!-- deconflict:end -->"

// installGuidance puts the announce-yourself rule where it is always in context.
//
// The skill is not enough on its own: a skill is loaded when the agent judges it
// relevant, and an agent that has not yet learned this repository has parallel
// writers cannot make that judgement. The rule that has to survive that gap is
// short, so it goes in the always-loaded file, and the procedure it points at
// stays in the skill.
//
// Fenced by markers so a re-install replaces its own block and nothing else.
func installGuidance(path string, dry bool) (installChange, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return installChange{}, err
	}
	body := string(raw)
	block := guidanceStart + "\n" + guidanceMarkdown + guidanceEnd + "\n"

	var updated string
	switch start := strings.Index(body, guidanceStart); {
	case start >= 0:
		end := strings.Index(body, guidanceEnd)
		if end < 0 {
			return installChange{}, fmt.Errorf("%s: %s without %s — refusing to guess where the block ends", path, guidanceStart, guidanceEnd)
		}
		updated = body[:start] + block + body[end+len(guidanceEnd)+1:]
	case body == "":
		updated = block
	default:
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		updated = body + "\n" + block
	}

	name := filepath.Base(path)
	if updated == body {
		return installChange{path, name + " rule", "already set"}, nil
	}
	if dry {
		return installChange{path, name + " rule", "would update"}, nil
	}
	action := "wrote"
	if len(raw) > 0 {
		action = "updated"
	}
	return installChange{path, name + " rule", action}, os.WriteFile(path, []byte(updated), 0o644)
}

// ---------- shared ----------

func readJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Refusing is the whole point: this file is somebody's configuration and
		// a parse failure means we do not understand it well enough to add to it.
		return nil, fmt.Errorf("%s: %w (fix or move it, then run install again)", path, err)
	}
	return doc, nil
}

func jsonString(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func writeIfChanged(path, before string, doc map[string]any, what string, dry bool) (installChange, error) {
	after := jsonString(doc)
	if after == before {
		return installChange{path, what, "already set"}, nil
	}
	if dry {
		return installChange{path, what, "would update"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return installChange{}, err
	}
	action := "updated"
	if before == "{}" {
		action = "wrote"
	}
	return installChange{path, what, action}, os.WriteFile(path, []byte(after+"\n"), 0o644)
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
