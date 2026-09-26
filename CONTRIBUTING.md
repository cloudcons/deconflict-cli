# Contributing to deconflict

Thanks for helping. This is the client: the binary people install, the hooks it
writes into their coding agents, and the wire types it shares with the registry.
That shapes most of the rules below. The binary edits other people's agent
configuration, and old copies of it keep talking to newer registries.

## Before you start

- **Bugs:** open an issue using the bug template. Include `deconflict version`,
  your OS, and which agent (Claude Code, Codex) is involved.
- **Features:** open an issue first to discuss it. A change to the wire
  contract or to what `install` writes is easier to get right before the code
  exists.
- **Security problems:** do not open an issue. See [SECURITY.md](SECURITY.md).

## Development

You need Go at the version in `go.mod`. Nothing else: there is no database, no
container and no code generation.

```console
$ go build ./cmd/deconflict
$ go vet ./...
$ go test -race ./...
```

To try a change without touching your real agent configuration, point `HOME` at
a scratch directory and use `--dry-run`:

```console
$ HOME=$(mktemp -d) ./deconflict install --agent all --dry-run
```

CI runs the same checks, builds for Linux, macOS and Windows on amd64 and
arm64, and runs the install scripts against the latest release.

## Rules this project keeps

### No dependencies

The module imports only the standard library. There is no `go.sum`, and CI
fails if one appears or if `go mod graph` names another module. A tool that
edits `~/.claude/settings.json` should be auditable in an afternoon. If you
need something a library would give you, write the small part you need.

### Compatibility

`pkg/` is a published interface, and the JSON in `pkg/protocol` is a contract
with registries you do not control:

- Add fields. Do not remove, rename or repurpose a field or a constant value
  that has shipped.
- A client older than the registry it talks to has to keep working, and so does
  a newer one.

### Other people's configuration

`deconflict install` writes into files that belong to the user and to other
tools. A change to the installer must keep its guarantees:

- Leave entries it did not write alone.
- Write atomically, and only if every file can be written.
- Keep a `.deconflict.bak` of each file it changes.
- When it cannot be sure an edit is safe, refuse that file, say why, and print
  what to add by hand.

Add a test for any new file shape the installer learns to handle.

## Pull requests

- Keep a pull request to one change, with tests.
- Explain *why* in the description and in comments. What the code does is
  visible; why it does it that way usually is not.
- Add a line under **Unreleased** in [CHANGELOG.md](CHANGELOG.md) for anything a
  user would notice.
- CI must pass.

By contributing, you agree that your contribution is licensed under the
project's [Apache License 2.0](LICENSE) (section 5 of the license), without any
additional terms.

## Releases

Maintainers tag `vX.Y.Z` on `main`. The release workflow builds the archives,
`.deb` and `.rpm` packages and checksums, publishes the GitHub release and the
apt and yum repositories, and verifies an install from each. The Homebrew tap
picks up the release within half an hour.

## Conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
