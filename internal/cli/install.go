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
	"sort"
	"strconv"
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

// codexEvents maps the hook event names we install to the snake_case form Codex
// uses in its trust keys.
var codexEvents = map[string]string{"SessionStart": "session_start", "PreToolUse": "pre_tool_use"}

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
	doc, err := readJSONObject(hooksPath)
	if err != nil {
		return nil
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	trusted := string(config)

	var missing []string
	for event, snake := range codexEvents {
		groups, _ := hooks[event].([]any)
		for i, g := range groups {
			group, ok := g.(map[string]any)
			if !ok {
				continue
			}
			entries, _ := group["hooks"].([]any)
			for j, e := range entries {
				entry, ok := e.(map[string]any)
				if !ok {
					continue
				}
				cmd, _ := entry["command"].(string)
				if !strings.Contains(cmd, " hook ") {
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

func cmdInstall(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(out)
	agent := fs.String("agent", "auto", "claude|codex|all|auto (auto installs for the agents it finds)")
	scope := fs.String("scope", "project", "project (this repo) or user (every repo you open)")
	dir := fs.String("dir", ".", "repository to install into")
	withSkill := fs.Bool("skill", true, "install the skill that teaches the workflow (Claude Code and Codex)")
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
			if err := add(installClaudeMCP(filepath.Join(root, ".mcp.json"), bin, *dry)); err != nil {
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
			if err := add(installCodexMCP(filepath.Join(home, ".codex", "config.toml"), bin, *dry)); err != nil {
				return err
			}
			// Skills, unlike hooks, do have a per-repository form: Codex reads
			// them from .agents/skills, the shared location other agent tools
			// install into as well. Without this a Codex agent had the one-line
			// rule in AGENTS.md and never the workflow it points at.
			if *withSkill {
				skillDir := filepath.Join(root, ".agents", "skills", "deconflict")
				if *scope == "user" {
					skillDir = filepath.Join(home, ".agents", "skills", "deconflict")
				}
				if err := add(installSkill(filepath.Join(skillDir, "SKILL.md"), *dry)); err != nil {
					return err
				}
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
	for _, target := range targets {
		if target != "codex" {
			continue
		}
		hooksPath := filepath.Join(home, ".codex", "hooks.json")
		if missing := untrustedCodexHooks(hooksPath, filepath.Join(home, ".codex", "config.toml")); len(missing) > 0 && !*dry {
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

func installClaudeMCP(path, bin string, dry bool) (installChange, error) {
	doc, err := readJSONObject(path)
	if err != nil {
		return installChange{}, err
	}
	before := jsonString(doc)
	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["deconflict"] = map[string]any{"type": "stdio", "command": bin, "args": []any{"mcp"}}
	doc["mcpServers"] = servers
	return writeIfChanged(path, before, doc, "Deconflict MCP mailbox", dry)
}

func installCodexMCP(path, bin string, dry bool) (installChange, error) {
	before, err := readText(path)
	if err != nil {
		return installChange{}, err
	}
	body := before
	header := "[mcp_servers.deconflict]"
	section := header + "\ncommand = " + strconv.Quote(bin) + "\nargs = [\"mcp\"]\n"
	start := strings.Index(body, header)
	if start >= 0 {
		end := len(body)
		if next := strings.Index(body[start+len(header):], "\n["); next >= 0 {
			end = start + len(header) + next + 1
		}
		old := body[start:end]
		commandLine := regexp.MustCompile(`(?m)^command\s*=.*$`)
		argsLine := regexp.MustCompile(`(?m)^args\s*=.*$`)
		updated := old
		if commandLine.MatchString(updated) {
			updated = commandLine.ReplaceAllString(updated, "command = "+strconv.Quote(bin))
		} else {
			updated += "\ncommand = " + strconv.Quote(bin)
		}
		if argsLine.MatchString(updated) {
			updated = argsLine.ReplaceAllString(updated, `args = ["mcp"]`)
		} else {
			// A raw string here would append a literal backslash-n and corrupt
			// the file, which is what this did until the quoting was fixed.
			updated += "\n" + `args = ["mcp"]`
		}
		body = body[:start] + updated + body[end:]
	} else {
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += "\n" + section
	}
	return writeTextIfChanged(path, before, body, "Deconflict MCP mailbox", dry)
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

// installHooks writes the two hooks into an agent's JSON settings. Claude and
// Codex take the identical file shape and differ only in what they call an
// edit, so the matcher is the only thing either of them supplies.
func installHooks(path, bin, editMatcher string, dry bool) (installChange, error) {
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
	upsertHookGroup(hooks, "PreToolUse", editMatcher, bin+" hook pre-tool")
	doc["hooks"] = hooks

	return writeIfChanged(path, before, doc, "session-start + pre-write hooks", dry)
}

func installClaudeHooks(path, bin string, dry bool) (installChange, error) {
	return installHooks(path, bin, "Edit|Write|NotebookEdit", dry)
}

// installCodexHooks carries Edit|Write as well as Codex's own apply_patch, so
// one file serves a machine that runs both agents.
func installCodexHooks(path, bin string, dry bool) (installChange, error) {
	return installHooks(path, bin, "apply_patch|Edit|Write", dry)
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

var codexFeatures = regexp.MustCompile(`(?m)^\[features\]\s*$`)
var codexHooksSet = regexp.MustCompile(`(?m)^\s*codex_hooks\s*=`)

// enableCodexHooks turns the engine on. Without this the hooks file is read by
// nobody and the install looks like it worked.
//
// Edited as text rather than parsed: this is one boolean in a file the user owns
// and comments in, and a TOML round-trip would reformat all of it to add a line.
func enableCodexHooks(path string, dry bool) (installChange, error) {
	before, err := readText(path)
	if err != nil {
		return installChange{}, err
	}
	body := before
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
	return writeTextIfChanged(path, before, body, "codex_hooks feature", dry)
}

// ---------- skill and guidance ----------

func installSkill(path string, dry bool) (installChange, error) {
	before, err := readText(path)
	if err != nil {
		return installChange{}, err
	}
	return writeTextIfChanged(path, before, renderSkill(), "coordination workflow skill", dry)
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
	body, err := readText(path)
	if err != nil {
		return installChange{}, err
	}
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

	return writeTextIfChanged(path, body, updated, filepath.Base(path)+" rule", dry)
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
	return writeTextIfChanged(path, before, jsonString(doc)+"\n", what, dry)
}

// readText reads a file this install may be the first to create. A missing file
// is an empty one here: every caller is about to add its own section to it.
func readText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return string(raw), nil
}

// writeTextIfChanged is the tail every edit here shares: already right, not
// writing today, or write it.
//
// The dry-run word mirrors the real one — "would write" against "wrote" — so a
// dry run reads as a prediction of the run it is predicting. There used to be
// three different words for it across four writers, which made the summary look
// like three different things were happening.
func writeTextIfChanged(path, before, after, what string, dry bool) (installChange, error) {
	// A JSON file this install created reads back as an empty object, so both
	// spellings of "there was nothing here" mean a fresh write.
	fresh := before == "" || before == "{}"
	if strings.TrimSpace(after) == strings.TrimSpace(before) {
		return installChange{path, what, "already set"}, nil
	}
	if dry {
		if fresh {
			return installChange{path, what, "would write"}, nil
		}
		return installChange{path, what, "would update"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return installChange{}, err
	}
	action := "updated"
	if fresh {
		action = "wrote"
	}
	return installChange{path, what, action}, os.WriteFile(path, []byte(after), 0o644)
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
