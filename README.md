# deconflict

Coordination and negotiation for autonomous software-engineering agents working
in shared repositories. **This is the client** — the binary you install, the
hooks it writes into your agent, and the wire types both halves agree on. The
registry it talks to is a separate program.

Six engineers each driving a coding agent do not collide because they are
careless. They collide because nobody knows who would care, agents edit wider
than a human would, and the first evidence of an overlap is a merge conflict or
a silently reverted fix.

**It never locks anything.** No agent is ever blocked, no command ever fails
because someone else got there first. If the registry is unreachable, the client
degrades to "no warnings" and gets out of the way — an advisory system that can
stop your work is worse than no system at all.

## Install

macOS and Linux, with Homebrew:

```console
$ brew install cloudcons/tap/deconflict
```

Debian, Ubuntu and other apt systems:

```console
$ curl -1sLf https://dl.cloudsmith.io/public/cloudcons/deconflict/setup.deb.sh | sudo bash
$ sudo apt-get install deconflict
```

Fedora, RHEL and other dnf/yum systems:

```console
$ sudo curl -fsSLo /etc/yum.repos.d/deconflict.repo \
    https://raw.githubusercontent.com/cloudcons/deconflict-cli/main/packaging/deconflict.repo
$ sudo dnf install deconflict
```

Anywhere else, the install script puts the binary in `~/.local/bin`
(`--dir` to change it, `--version` to pin one) after checking it against the
release's `SHA256SUMS`:

```console
$ curl -fsSL https://raw.githubusercontent.com/cloudcons/deconflict-cli/main/install.sh | sh
```

On Windows, in PowerShell:

```powershell
PS> irm https://raw.githubusercontent.com/cloudcons/deconflict-cli/main/install.ps1 | iex
```

Or from source, with Go:

```console
$ go install github.com/cloudcons/deconflict-cli/cmd/deconflict@latest
```

Every [release](https://github.com/cloudcons/deconflict-cli/releases) also
carries the archives, `.deb` and `.rpm` files and their checksums directly.

Package repository hosting is graciously provided by
[Cloudsmith](https://cloudsmith.com), free for open-source projects.

Then wire it into the agents you run:

```console
$ deconflict install --agent all
```

That writes session-start and pre-tool hooks, an MCP server entry, and a skill
describing how to use them. `--dry-run` shows every change first, and nothing is
written outside your own agent configuration.

It edits files other tools own, so it only ever changes its own entries. Other
hooks, MCP servers, comments and key order are left as they were. If a file is
in a shape it cannot edit with certainty, such as unparseable JSON, an inline
`[mcp_servers]` table, or unbalanced markers, it writes nothing at all. Instead
it prints exactly what to add by hand. Before changing an existing file, it
saves the previous version beside it as `<name>.deconflict.bak`.

The skill goes to `.claude/skills/deconflict/` for Claude Code and to
`.agents/skills/deconflict/` for Codex. With `--scope user`, everything goes
under your home directory and nothing into the current one. That includes the
MCP entry in `~/.claude.json` and the rule in `~/.claude/CLAUDE.md` or
`~/.codex/AGENTS.md`.

The skill ships inside the binary, so upgrading the client does not change the
copy already on disk. Each copy is stamped with the version and revision of the
text that wrote it. The session-start hook names any copy that is out of date,
along with the command that refreshes it. Re-running `deconflict install` after
an upgrade refreshes the skill, hooks, MCP entry and rule together.

`deconflict uninstall` takes the same flags and removes only what install put
in. It leaves a skill you have edited in place, and it leaves Codex's
`codex_hooks` switch on, because other tools' hooks depend on it too.

## Use

```console
$ deconflict claim --paths 'src/auth' --not 'src/auth/middleware.go' \
    --what 'rework token refresh to use rotating refresh tokens'
```

A claim says what you are touching and, just as importantly, what you are *not*.
The `--not` field is the one that does the real work: it lets someone proceed
alongside you instead of backing off from the whole module, and it is the field
a human would never think to write down.

By default claims go to a local file, which is all one machine needs. To share a
registry with a team:

```console
$ deconflict login --server https://registry.example.com
```

Say what you depend on but will not edit, and whoever changes it hears about
you — the refactor that would break a caller who never touched the refactored
file:

```console
$ deconflict claim --paths 'app/checkout' --uses 'lib/parser,github.com/acme/lib:pkg/**' \
    --what 'wire the parser into checkout'
```

Shared things that are not files — staging, a database, a deploy slot — take an
advisory lock instead, on a shared registry:

```console
$ deconflict lock staging --reason 'deploying #88'
$ deconflict unlock staging
```

A lock is always granted. When somebody else holds the resource it names them
and exits 3, the same as an overlapping claim.

Run `deconflict help` for the rest: overlap checks, leases, negotiated
agreements, the durable agent mailbox, shared resources, and work-tracker
lookups.

## No dependencies

This module imports nothing outside the standard library — there is no `go.sum`
in this repository, and that is deliberate rather than incidental. A tool that
edits `~/.claude/settings.json` should be auditable in an afternoon.

## Layout

| Path            | What it is                                                     |
| --------------- | -------------------------------------------------------------- |
| `pkg/protocol`  | the wire contract: JSON types, no state, no connections        |
| `pkg/claim`     | the claim model and overlap logic                              |
| `pkg/client`    | the local-file and HTTP registry backends                      |
| `pkg/settings`  | operator-tunable defaults, as clients see them                 |
| `internal/cli`  | the commands, the hooks, the MCP server, the installer         |

`pkg/` is a published interface. Fields get added; a field or a constant value
that has shipped is not removed or repurposed, because an installed client older
than the registry it is talking to has to keep working.

## Running a registry

`serve`, `genkey` and `rotate-key` are not here. Each needs a database
credential, so each belongs to the server binary rather than to a client anyone
can install.

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
