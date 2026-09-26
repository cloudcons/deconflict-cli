# Changelog

Notable changes to the deconflict client, newest first. Versions follow
[semantic versioning](https://semver.org); while the major version is 0, a
minor release may change behaviour, but `pkg/` stays backward compatible (see
[CONTRIBUTING.md](CONTRIBUTING.md#compatibility)). Each release's full commit
list is on its [GitHub release](https://github.com/cloudcons/deconflict-cli/releases).

## [Unreleased]

## [0.1.12] - 2026-09-26

### Changed
- The default agent id is `user@host/<worktree>/<runtime>` rather than
  `user@host`. Every session on a machine used to be the same agent, so where
  sessions shared a Unix user, one could not resolve another's question and
  none received another's watercooler posts. `DECONFLICT_AGENT` still
  overrides it. Claims, locks and mail made under the old id keep that id.

## [0.1.11] - 2026-09-26

### Added
- Contributor, security and conduct documents, and issue and pull request
  templates.

### Fixed
- The `.deb` packages now name their license: a machine-readable (DEP-5)
  `copyright` file pointing at the system's copy of the Apache-2.0 text, and a
  `License` control field, which is what package indexes such as Cloudsmith
  read.

## [0.1.10] - 2026-09-25

### Added
- `deconflict update` upgrades the binary through whatever installed it
  (Homebrew, apt, dnf/yum, `go install`, or the install script), then refreshes
  the hooks, MCP entries and skills of every agent it was installed into. The
  session-start hook mentions a newer release at most once a day
  (`DECONFLICT_NO_UPDATE_CHECK=1` turns that off).
- `deconflict uninstall` removes only deconflict's own entries.

### Changed
- `deconflict install` no longer damages existing agent configuration. It
  keeps other tools' hooks, MCP servers and skills. It preserves JSON key order
  and numbers, and never widens a hook matcher shared with another tool. It
  writes every file atomically, and only if all of them can be written, keeping
  a `.deconflict.bak` of each file it changes. Files it cannot edit safely are
  left alone, with instructions to follow by hand.
- `--scope user` writes only under the home directory, including a user-level
  MCP entry in `~/.claude.json`.
- dnf/yum users install from a repository file shipped in `packaging/`, because
  the file Cloudsmith's setup script generates breaks TLS on Fedora 44.

## [0.1.9] - 2026-09-25

### Added
- Install without a Go toolchain: `install.sh`, `install.ps1`, the
  `cloudcons/tap` Homebrew tap, and apt and dnf/yum repositories on
  Cloudsmith. Every release also carries `.deb` and `.rpm` files.

## [0.1.8] - 2026-09-25

### Added
- Open needs are brought to the agents placed to take them.

## [0.1.7] - 2026-09-24

### Added
- The watercooler: rooms, questions, needs and heads-ups.
- The skill is installed for Codex too, and the session-start hook says when an
  installed skill is out of date.

## [0.1.6] - 2026-09-23

### Changed
- `--uses` is described in the always-loaded rule, not only in the skill.

## [0.1.5] - 2026-09-23

### Added
- A claim can say what it uses (`--uses`), and whoever changes that is told.

## [0.1.4] - 2026-09-23

### Added
- `deconflict login` names the organization a token binds to, and can be asked
  for a specific one.

## [0.1.3] - 2026-09-23

### Added
- Release archives and checksums are published on every version tag.

## [0.1.2] - 2026-09-23

### Added
- Advisory locks on shared resources (`deconflict lock` / `unlock`).

## [0.1.1] - 2026-08-28

### Fixed
- Builds on Windows.

## [0.1.0] - 2026-08-28

The client becomes a module of its own, with no dependencies, split out of the
registry.

[Unreleased]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.12...HEAD
[0.1.12]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.11...v0.1.12
[0.1.11]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.10...v0.1.11
[0.1.10]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.9...v0.1.10
[0.1.9]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.8...v0.1.9
[0.1.8]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.7...v0.1.8
[0.1.7]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.6...v0.1.7
[0.1.6]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.5...v0.1.6
[0.1.5]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/cloudcons/deconflict-cli/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/cloudcons/deconflict-cli/releases/tag/v0.1.0
