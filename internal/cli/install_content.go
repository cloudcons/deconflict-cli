package cli

// The text `deconflict install` writes. Kept apart from the plumbing because
// this is the part people will read and edit, and because a diff to the wording
// should not look like a diff to the installer.

const skillMarkdown = `---
name: deconflict
description: Claim an area of a repository before editing it, and check what other agents already claimed. Use when starting work in a repo where several people or agents commit in parallel, before editing files, when a claim reports an overlap, and when the work lands.
---

# Working alongside other agents

Several agents commit to this repository at the same time. Two of them editing
the same area silently undo each other, and the first evidence is usually a
merge conflict or a fix that quietly disappeared.

Claims are **advisory**. Nothing here blocks an edit or locks a file. The point
is that the other agent finds out while it can still choose differently.

## Before you edit anything

` + "```console" + `
$ deconflict claim --paths '<globs you will touch>' \
                   --what  '<what you are doing>' \
                   --why   '<why — what another agent needs in order to judge the overlap>' \
                   --not   '<globs you will NOT touch>' \
                   --interface '<interface or schema changes others would notice>' \
                   --task  '<the ticket this is, if there is one>'
` + "```" + `

Write ` + "`--not`" + ` carefully. It is what lets another agent work alongside you
instead of backing off from the whole module. A claim on ` + "`src/**`" + ` with no
` + "`--not`" + ` reads as "stay out of the repository".

` + "`--task`" + ` is not decoration where the registry is connected to a work tracker:
the reference resolves to a real item, and picking it up is announced on the
ticket. Find the reference with ` + "`deconflict task search '<words>'`" + ` rather than
typing one from memory — a ` + "`--task`" + ` that points at nothing is worse than none,
because it reads as a link and is not one.

## When a claim reports an overlap

It has not blocked you. Read the other claim's ` + "`NOT touching`" + ` line and decide
whether the areas genuinely collide. If they do, say so to your operator before
proceeding rather than after — that is the entire value of finding out early.

Check an area without claiming it:

` + "```console" + `
$ deconflict check --paths 'src/auth/**'
$ deconflict list --all
` + "```" + `

## While you work

Stay inside the claim. Touch only what the task needs — no opportunistic
cleanup, reformatting or renames outside the area you claimed. If the work
genuinely requires editing something outside it, widen the claim or say so.

Start from a fresh base. Most "another agent broke my fix" is a branch cut from
yesterday.

Run ` + "`deconflict status`" + ` before opening a PR. It diffs the branch against the
claim; files changed outside your claimed paths are either a claim to widen or
scope creep to drop.

Claims expire, so a long task needs ` + "`deconflict renew`" + `. An expired claim is
not a held one — it stops warning anybody.

## When the work lands

` + "```console" + `
$ deconflict release --reason merged --pr <url>
` + "```" + `

If you forget, ` + "`deconflict reconcile --apply`" + ` closes claims whose branch git
shows as merged. A claim nobody released is the failure mode that makes a
registry untrustworthy: it reports ground as held that nobody is standing on.
`

const guidanceMarkdown = `## Working alongside other agents

Several agents commit to this repository in parallel. **Claim the area you are
about to edit before you edit it**, and read what comes back:

` + "```console" + `
$ deconflict claim --paths '<globs>' --what '<what>' --why '<why>' --not '<globs you will not touch>'
` + "```" + `

Claims are advisory — nothing is blocked or locked. An overlap is a
conversation to have now rather than a merge conflict to have later. Release
with ` + "`deconflict release --reason merged`" + ` when the work lands.

The full workflow, including what to do when an overlap is reported, is in the
` + "`deconflict`" + ` skill.
`
