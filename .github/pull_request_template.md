## What and why

<!-- What changes, and why this way. -->

## Checklist

- [ ] `go vet ./...` and `go test -race ./...` pass
- [ ] No new dependencies (no `go.sum`)
- [ ] `pkg/` changes only add; nothing shipped is removed or repurposed
- [ ] Installer changes keep other tools' config intact, with a test for the new case
- [ ] A line under **Unreleased** in `CHANGELOG.md`, if a user would notice
