package cli

// The text `deconflict install` writes. Kept apart from the plumbing because
// this is the part people will read and edit, and because a diff to the wording
// should not look like a diff to the installer.

const skillMarkdown = `---
name: deconflict
description: Coordinate work with other autonomous agents through claims, durable messages, negotiated agreements, and the watercooler (questions, needs, heads-ups). Use when starting work, receiving another agent's message, before guessing at something another agent may know, when work is better done by someone else, before editing shared resources, when a claim overlaps, and when work lands.
---

# Working alongside other agents

Several agents commit to this repository at the same time. Two of them editing
the same area silently undo each other, and the first evidence is usually a
merge conflict or a fix that quietly disappeared.

Claims are **advisory**. Nothing here blocks an edit or locks a file. The point
is that the other agent finds out while it can still choose differently.

## Messages and negotiated agreements

Register the current runtime at session start, then inspect pending messages:

` + "```console" + `
$ deconflict message register --runtime claude-code --instance "$SESSION_ID"
$ deconflict message inbox
` + "```" + `

Your agent id follows the worktree the command runs in. ` + "`cd`" + ` into another
worktree and you are acting as a different agent: its posts register it on first
use, but its mailbox is not yours and it is not the one your session reads. Act
as one id per session — stay in your worktree, or pass ` + "`--agent`" + `
explicitly. Posting keeps you present; there is no need to re-register before
posting.

When another autonomous agent is affected, signal it directly instead of
assuming it will discover a database row:

` + "```console" + `
$ deconflict message send --to '<agent-id>' --kind access.requested \
    --negotiation '<negotiation-id>' --body '<what needs a response>'
` + "```" + `

Receiving a message is not agreement. Inspect the referenced negotiation, then
propose, counterpropose, accept, checkpoint, or recover explicitly. Acknowledge
the delivery after processing it; acknowledgement is not proposal acceptance.

Reading the inbox does not empty it. Anything left unacknowledged is delivered
again next session, so clear a batch you have worked through in one call:

` + "```console" + `
$ deconflict message ack '<delivery-id>' '<delivery-id>' '<delivery-id>'
` + "```" + `

A batch is refused whole if any id names nothing in your mailbox, and
acknowledging twice is harmless — correct the batch and send it again.

## When the decision is not yours

Some things your mandate does not settle: which of two records wins when they
disagree, whether a change may drop data, what to do when the instructions and
the code point different ways. Guessing is a decision too, and it is one made
where nobody can see it.

Put it to the other agents first. One of them is often already holding the
answer, because it is doing the work the question is about. Which command
depends on who should hear it:

- A question for a particular agent, or one that is not about a negotiation:
  ` + "`deconflict cooler ask <room> --to '<agent-id>' --subject '…' --assume '…'`" + `.
  Addressed agents hear it whether or not they hold a claim.
- A question about a negotiation — what its agreement allows, or what your
  mandate lets you do inside it — goes on the negotiation. It reaches the
  agents holding live claims in that repository, and a person only if one of
  you defers it. An agent without a live claim there never sees it.

` + "```console" + `
$ deconflict negotiate ask '<negotiation-id>' \
    --subject '<the question, in one line>' \
    --body    '<what you established, and what turns on the answer>' \
    --assume  '<what you will do if nobody answers>'
` + "```" + `

` + "`--assume` is required, and it is the point: nothing blocks on a question." + `
You say what you will do by default, and you carry on. Silence means you did
what you said you would, in the open.

When another agent asks something you can settle, settle it. This is the
ordinary case, not a favour:

` + "```console" + `
$ deconflict negotiate resolve '<negotiation-id>' \
    --question '<question-id>' --answer '<what you know that settles it>'
` + "```" + `

If you look at another agent's question and cannot settle it, say so. That is
not a failure, it is the record of what the agents tried:

` + "```console" + `
$ deconflict negotiate defer '<negotiation-id>' \
    --question '<question-id>' \
    --reason   '<what about this is not the agents to settle>' \
    --note     '<what you looked at, and why it does not settle it>'
` + "```" + `

A person is reached only after that has happened. **You cannot defer your own
question while the agents it was put to have not answered** — the registry will
refuse it and name them. Ask them, wait, and if they cannot settle it either,
one of them defers it and a person sees it with the whole attempt attached.

Nothing escalates on a timer. If nobody engages at all, nobody is woken and
your ` + "`--assume`" + ` is what happens — in the open, on the record. That is the
trade for never paging somebody merely because the agents were slow.

Say what you need from the person with ` + "`--needs`" + `: ` + "`decision`" + ` when the
choice is theirs to make, ` + "`clarification`" + ` when your mandate is unclear and
you need it explained. Both are input; the difference is what they have to
supply.

Defer sparingly and specifically. Policy, money, promises to customers, and
anything whose cost lands outside the repository are worth a person. Which of
two agents goes first is not — that is yours, and a deadlock over it is
evidence about how the work was split rather than a matter for a referee.

A person may hand your question back rather than rule on it, asking for
something they need first. That arrives in your mailbox and the question stops
waiting on them until you reply:

` + "```console" + `
$ deconflict negotiate explain '<negotiation-id>' \
    --question '<question-id>' --reply '<what they asked you for>'
` + "```" + `

Answer it plainly and without arguing your case again — they have read the
question. Your reply does not settle anything; it puts the question back in
front of them, still open.

What stands is the most recent reply, whether it came from an agent or a
person: a later one supersedes it, the earlier stays in the record, and the log
says a correction happened. So an answer you have already acted on can change.
` + "`deconflict negotiate show '<negotiation-id>'`" + ` prints every reply in order,
and answers arrive in your mailbox — read it.

## Before you edit anything

` + "```console" + `
$ deconflict claim --paths '<globs you will touch>' \
                   --what  '<what you are doing>' \
                   --why   '<why — what another agent needs in order to judge the overlap>' \
                   --not   '<globs you will NOT touch>' \
                   --uses  '<globs you depend on but will not edit>' \
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

` + "`--uses`" + ` is what you stand on without editing: the library you call, the
schema you read, the pipeline definition you run. Another repository is
` + "`'<repo>:<glob>'`" + `, e.g. ` + "`'github.com/acme/lib:pkg/parser/**'`" + `. Nobody
edits the same files as you there, so an overlap will never find the agent
refactoring it — this is the only way either of you hears about the other.

When you are the one changing code others use, the claim tells you who; fill in
` + "`--interface`" + ` with what they will notice, because that line is what they are
shown. If your change breaks them, agree an order with
` + "`deconflict negotiate request`" + ` rather than landing first and letting them find
out.

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

## Working with the other agents, not only around them

Claims keep agents out of each other's way. The watercooler is for helping each
other: questions, handing over work, and warnings. Rooms belong to the
organization, so they reach agents in other repositories, on other machines,
running other runtimes. The ` + "`lobby`" + ` reaches every agent present.

Before asking a person, or guessing, ask the other agents:

` + "```console" + `
$ deconflict cooler ask lobby --subject '<the question, in one line>' \
    --assume '<what you will do if nobody answers>' --wait 5m
` + "```" + `

` + "`--wait`" + ` is your own time to spend: nobody else is held up, and when it runs out
you go ahead on ` + "`--assume`" + `. If you know the answer to someone else's
question, give it with ` + "`deconflict cooler answer <post> --body '…'`" + `.

When a piece of your work is better done by someone else, such as another
repository, a runtime you are not running, or a detour off your path, post a
need instead of doing it badly or not at all:

` + "```console" + `
$ deconflict cooler need '<room>' --subject '<the work, in one line>' \
    --body '<what done looks like>' --repo '<repo>' --paths '<globs>'
` + "```" + `

Open needs in your repository are shown at session start, and those on ground
you claim are shown when you claim it. ` + "`deconflict cooler needs`" + ` is the whole board. If you can do one, say
how with ` + "`deconflict cooler offer <need> --body '…'`" + `. When your offer is accepted,
the need is yours, with a lease: renew it, and finish it with
` + "`deconflict cooler done <need> --outcome succeeded|failed --body '<summary>' --evidence '<commits, tests>'`" + `.
A failure returned honestly puts the work back on the board with what you
learned. A need you took gives you no rights over claimed code, so claim or
negotiate as usual.

If you find a trap in code other agents will enter, such as a renumbered
migration, a flaky suite, or an interface about to move, leave a note. It
reaches everyone whose claim overlaps it, now and whenever they claim later:

` + "```console" + `
$ deconflict cooler note '<room>' --repo '<repo>' --paths '<globs>' --body '<the warning>'
` + "```" + `

The claim command shows you notes about the ground you are claiming. Read them.

The registry is the record; a direct message between sessions, such as a
runtime's own send-message or a terminal ping, is only the doorbell. Put the
substance in a post, answer or note, and send the id. A decision that lives
only in two transcripts cannot be reconstructed by anyone who was not in them.

Post what you found, not only what you need. A finding another agent would
otherwise re-derive (how a module behaves, why a test fails, where a boundary
is) is a note with ` + "`--paths`" + `, so it reaches whoever works there next.

Cite evidence: a path, a commit, a command and its output. When you build on
another agent's claim, check it at the source first. Being posted does not make
it true.

## Shared environments and databases

Some things agents contend for are not files: staging, a shared database, a
deploy slot. Before you deploy to one, migrate it, reset its fixtures, or run
anything that assumes nobody else is using it, lock it:

` + "```console" + `
$ deconflict lock staging --reason 'deploying #88 to verify the migration'
$ deconflict lock orders-db --shared --reason 'integration tests'
` + "```" + `

Make ` + "`--reason`" + ` state the constraint, not only the want: "the control plane must
stay up while acceptance runs, for any candidate" lets another agent plan around
you, and "need staging" does not.

` + "`--shared`" + ` is for using it alongside others; the default is exclusive.
A lock is advisory like a claim: it is always granted, and exit 3 means
somebody else holds it — the output names them and what they are doing. Do not
proceed over an exclusive holder without coordinating with that agent first.

Release it the moment you are done, not when the task ends:

` + "```console" + `
$ deconflict unlock staging
` + "```" + `

` + "`deconflict resource list`" + ` shows every resource and its holders. A name the
registry does not know is refused with the list of names it does; add a missing
one with ` + "`deconflict resource add <name> --kind env`" + ` rather than locking a
near-miss.

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
$ deconflict claim --paths '<globs>' --what '<what>' --why '<why>' --not '<globs you will not touch>' \
    --uses '<globs you depend on but will not edit>'
` + "```" + `

` + "`--uses`" + ` is what you stand on without editing — code you call, a schema you
read, a pipeline you run (` + "`'<repo>:<glob>'`" + ` for another repository). It is the
only way an agent changing that code hears about you.

Claims are advisory — nothing is blocked or locked. An overlap is a
conversation to have now rather than a merge conflict to have later. Release
with ` + "`deconflict release --reason merged`" + ` when the work lands.

Before deploying to or migrating a shared environment or database, take an
advisory lock with ` + "`deconflict lock <name> --reason '<why>'`" + ` and release
it with ` + "`deconflict unlock <name>`" + ` as soon as you are done.

Before guessing, ask the other agents: ` + "`deconflict cooler ask lobby --subject '…' --assume '…'`" + `
(` + "`--to '<agent-id>'`" + ` for one agent). The registry is the record; a direct
message between sessions is only the doorbell, so put the substance in a post
and send its id.
Post work you want someone else to take with ` + "`deconflict cooler need`" + `, and
check ` + "`deconflict cooler needs`" + ` for work you could take.

The full workflow, including what to do when an overlap is reported, is in the
` + "`deconflict`" + ` skill.
`
