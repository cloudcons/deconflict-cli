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

```console
$ go install github.com/cloudcons/deconflict-cli/cmd/deconflict@latest
```

Then wire it into the agents you run:

```console
$ deconflict install --agent all
```

That writes session-start and pre-tool hooks, an MCP server entry, and a skill
describing how to use them. `--dry-run` shows every change first, and nothing is
written outside your own agent configuration.

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
