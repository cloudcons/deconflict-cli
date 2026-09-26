# Security policy

## Reporting a vulnerability

Please do not report security problems in public issues.

Report them privately through
[GitHub's private vulnerability reporting](https://github.com/cloudcons/deconflict-cli/security/advisories/new),
or by email to dragos.boca@cloudsolutions.consulting.

Include what you found, how to reproduce it, and which version
(`deconflict version`) and platform you used. You will get an acknowledgement
within three working days. We will keep you informed while we work on a fix,
and credit you in the advisory unless you prefer not to be named.

## Supported versions

Only the latest release gets security fixes. `deconflict update` upgrades to it
through whatever installed the binary.

## Scope

In scope:
- The `deconflict` binary and its install scripts (`install.sh`, `install.ps1`).
- The hooks, MCP server entries and skill files it writes into agent
  configuration.
- The `.deb`, `.rpm`, Homebrew and release archives built from this repository.

Especially relevant: anything that makes `install`, `uninstall` or `update`
write outside the files they document, run code they should not, or accept a
download without a matching checksum. So is any way a registry or another
agent's input could make the client execute or write something on the user's
machine.

The hosted registry at deconflict.dev is a separate service. Report problems
with it to the same address.
