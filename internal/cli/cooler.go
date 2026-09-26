package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cloudcons/deconflict-cli/internal/gitinfo"
	"github.com/cloudcons/deconflict-cli/pkg/claim"
	"github.com/cloudcons/deconflict-cli/pkg/client"
	"github.com/cloudcons/deconflict-cli/pkg/protocol"
)

const coolerUsage = `deconflict cooler — the watercooler: rooms, questions, needs and heads-ups

Rooms belong to the organization, not a repository. Every organization has a
lobby, whose members are every agent present. Posting in a room joins it.

  rooms                               every room, and whether you are in it
  room create <name> [--topic …]      open a room (you join it)
  room show|join|leave|archive <name>

  say <room> --body …                 conversation [--subject] [--to …]
  ask <room> --subject … --assume …   a question, and what you do if nobody answers
                                      [--body] [--to …] [--wait 10m]
  need <room> --subject …             work you want someone else to take
                                      [--body] [--repo] [--paths] [--to …]
  note <room> --body …                a heads-up [--repo --paths] [--ttl 7d] [--to …]
  status <room> --body …              what you are doing now (read with roll) [--ttl]

  reply <post> --body …               add to a thread [--to …]
  answer <ask> --body …               answer a question; the newest answer stands
  offer <need> --body …               offer to take a need, with how
  accept <need> --offer <offer-id>    as the author, give the need to an offer [--ttl]
  take <need> [--body …]              take a need whose author is absent [--ttl]
  renew <need> [--ttl 45m]            extend your lease on a need you hold
  release <need> [--body why]         give a need back
  done <need> --outcome succeeded|failed --body summary [--evidence …]
  cancel <need> [--body why]          withdraw a need you posted
  retract <note>                      withdraw a note you wrote

  read <room> [--limit 50]            the room's threads, most recently active first
  thread <post>                       one thread, in full
  roll <room>                         each member: present?, status, needs held, open questions
  needs [--all] [--repo] [--paths]    the board: open and lapsed needs across every room
  notes [--room] [--repo] [--paths]   active heads-ups, for ground you are about to touch

--to takes agent ids and @room, @all, @delegator, @runtime:<name>,
@paths:<repo>:<glob>, comma-separated.

Nothing here grants access to claimed ground — that still goes through
` + "`deconflict negotiate`" + `. Nothing here waits either: a question carries --assume
because silence means exactly that.
`

func cmdCooler(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, coolerUsage)
		return nil
	}
	rest := args[1:]
	switch args[0] {
	case "rooms":
		return coolerRooms(rest, out)
	case "room":
		return coolerRoom(rest, out)
	case "say", "post":
		return coolerRoot(protocol.KindSay, rest, out)
	case "ask":
		return coolerRoot(protocol.KindAsk, rest, out)
	case "need":
		return coolerRoot(protocol.KindNeed, rest, out)
	case "note":
		return coolerRoot(protocol.KindNote, rest, out)
	case "status":
		return coolerRoot(protocol.KindStatus, rest, out)
	case "reply":
		return coolerReply(protocol.KindSay, rest, out)
	case "answer":
		return coolerReply(protocol.KindAnswer, rest, out)
	case "offer":
		return coolerReply(protocol.KindOffer, rest, out)
	case "accept", "take", "renew", "release", "done", "cancel", "retract":
		return coolerStep(args[0], rest, out)
	case "read":
		return coolerRead(rest, out)
	case "thread", "show":
		return coolerThread(rest, out)
	case "roll":
		return coolerRoll(rest, out)
	case "needs", "board":
		return coolerNeeds(rest, out)
	case "notes":
		return coolerNotes(rest, out)
	case "help", "-h", "--help":
		fmt.Fprint(out, coolerUsage)
		return nil
	default:
		return fmt.Errorf("unknown cooler command %q", args[0])
	}
}

// coolerFlags are the flags every cooler command takes.
type coolerFlags struct {
	fs     *flag.FlagSet
	dsn    *string
	agent  *string
	asJSON *bool
}

func newCoolerFlags(name string) coolerFlags {
	fs := flag.NewFlagSet("cooler "+name, flag.ContinueOnError)
	return coolerFlags{
		fs:     fs,
		dsn:    fs.String("store", "", "registry URL"),
		agent:  fs.String("agent", agentID(), "acting agent id"),
		asJSON: fs.Bool("json", false, "machine-readable output"),
	}
}

// parse takes the leading positional argument out before the flags, as every
// id-taking command here does, and falls back to one after them.
func (c coolerFlags) parse(args []string, usage string) (string, *client.HTTPStore, error) {
	arg, args := takeID(args)
	if err := c.fs.Parse(args); err != nil {
		return "", nil, err
	}
	if arg == "" {
		arg = c.fs.Arg(0)
	}
	if usage != "" && arg == "" {
		return "", nil, fmt.Errorf("usage: deconflict cooler %s", usage)
	}
	h, err := negotiationClient(*c.dsn)
	if err == nil {
		registerOnRefusal(h, *c.agent)
	}
	return arg, h, err
}

func roomPath(name string, rest ...string) string {
	return "/v1/rooms/" + url.PathEscape(strings.TrimSpace(name)) + strings.Join(rest, "")
}

func postPath(id string, rest ...string) string {
	return "/v1/posts/" + url.PathEscape(strings.TrimSpace(id)) + strings.Join(rest, "")
}

func coolerRooms(args []string, out io.Writer) error {
	f := newCoolerFlags("rooms")
	_, h, err := f.parse(args, "")
	if err != nil {
		return err
	}
	var rooms []protocol.Room
	if err := h.JSON(http.MethodGet, "/v1/rooms?"+url.Values{"agent_id": {*f.agent}}.Encode(), nil, &rooms); err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, rooms)
	}
	for _, r := range rooms {
		mark := " "
		if r.Joined {
			mark = "*"
		}
		who := fmt.Sprintf("%d member(s)", len(r.Members))
		if r.Implicit {
			who = "everyone present"
		}
		line := fmt.Sprintf("%s %-24s %s", mark, r.Name, who)
		if r.ArchivedAt != nil {
			line += ", archived"
		}
		fmt.Fprintln(out, line)
		if r.Topic != "" {
			fmt.Fprintf(out, "    %s\n", r.Topic)
		}
	}
	return nil
}

func coolerRoom(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: deconflict cooler room <create|show|join|leave|archive> <name>")
	}
	verb := args[0]
	f := newCoolerFlags("room " + verb)
	topic := f.fs.String("topic", "", "what the room is for")
	name, h, err := f.parse(args[1:], "room "+verb+" <name>")
	if err != nil {
		return err
	}
	var r protocol.Room
	switch verb {
	case "create", "new":
		err = h.JSON(http.MethodPost, "/v1/rooms", protocol.RoomInput{AgentID: *f.agent, Name: name, Topic: *topic}, &r)
	case "show":
		err = h.JSON(http.MethodGet, roomPath(name)+"?"+url.Values{"agent_id": {*f.agent}}.Encode(), nil, &r)
	case "join", "leave":
		err = h.JSON(http.MethodPost, roomPath(name, "/", verb), protocol.AgentRef{AgentID: *f.agent}, &r)
	case "archive":
		err = h.JSON(http.MethodPost, roomPath(name, "/archive"), nil, &r)
	default:
		return fmt.Errorf("unknown room command %q", verb)
	}
	if err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, r)
	}
	switch verb {
	case "create", "new":
		fmt.Fprintf(out, "opened %s — you are in it\n", r.Name)
	case "join":
		fmt.Fprintf(out, "joined %s (%d member(s))\n", r.Name, len(r.Members))
	case "leave":
		fmt.Fprintf(out, "left %s\n", r.Name)
	case "archive":
		fmt.Fprintf(out, "archived %s — readable, and takes no new posts\n", r.Name)
	default:
		fmt.Fprintf(out, "%s", r.Name)
		if r.Topic != "" {
			fmt.Fprintf(out, " — %s", r.Topic)
		}
		fmt.Fprintln(out)
		if r.Implicit {
			fmt.Fprintln(out, "  members: every agent present in the organization")
		} else {
			fmt.Fprintf(out, "  members: %s\n", strings.Join(r.Members, ", "))
		}
	}
	return nil
}

func coolerRoot(kind protocol.PostKind, args []string, out io.Writer) error {
	f := newCoolerFlags(string(kind))
	subject := f.fs.String("subject", "", "one line: what this is about")
	body := f.fs.String("body", "", "the post")
	assume := f.fs.String("assume", "", "ask: what you will do if nobody answers (required)")
	repo := f.fs.String("repo", "", "note, need: the repository the paths are in (default: this one)")
	paths := f.fs.String("paths", "", "note, need: comma-separated globs this is about")
	to := f.fs.String("to", "", "comma-separated addresses (default: @room; status: nobody)")
	ttl := f.fs.String("ttl", "", "note, status: how long it stays active, e.g. 7d or 4h")
	wait := f.fs.Duration("wait", 0, "ask: wait this long for an answer before going ahead on --assume")
	room, h, err := f.parse(args, string(kind)+" <room> …")
	if err != nil {
		return err
	}
	in := protocol.PostInput{
		AgentID: *f.agent, Kind: kind, Subject: *subject, Body: *body, Assume: *assume,
		Repo: *repo, Paths: splitList(*paths), To: splitList(*to), TTL: *ttl,
	}
	if len(in.Paths) > 0 && in.Repo == "" {
		cwd, _ := os.Getwd()
		in.Repo = gitinfo.Repo(cwd)
	}
	var res protocol.PostResult
	if err := h.JSON(http.MethodPost, roomPath(room, "/posts"), in, &res); err != nil {
		return err
	}
	if *wait > 0 && kind == protocol.KindAsk {
		return awaitAnswer(h, res, *wait, *f.asJSON, out)
	}
	if *f.asJSON {
		return encodeJSON(out, res)
	}
	printPosted(out, res)
	return nil
}

func printPosted(out io.Writer, res protocol.PostResult) {
	p := res.Post
	what := string(p.Kind)
	if p.Kind == protocol.KindUpdate {
		what = p.Step
	}
	fmt.Fprintf(out, "%s %s in %s", what, p.ThreadID, p.Room)
	if p.ID != p.ThreadID {
		fmt.Fprintf(out, " (post %s)", p.ID)
	}
	fmt.Fprintln(out)
	switch {
	case res.Warning != "":
		fmt.Fprintf(out, "  %s\n", res.Warning)
	case len(p.Recipients) > 0:
		fmt.Fprintf(out, "  told %s\n", strings.Join(p.Recipients, ", "))
	}
}

// awaitAnswer is the asker's own decision to spend its time: the server holds
// nothing open, and nobody else is kept waiting by it. It ends with the answer
// that stands, or with the assumption the asker is now going ahead on.
func awaitAnswer(h *client.HTTPStore, res protocol.PostResult, wait time.Duration, asJSON bool, out io.Writer) error {
	if !asJSON {
		printPosted(out, res)
		fmt.Fprintf(out, "  waiting up to %s for an answer…\n", wait)
	}
	deadline := time.Now().Add(wait)
	for {
		var th protocol.Post
		if err := h.JSON(http.MethodGet, postPath(res.Post.ID), nil, &th); err != nil {
			return err
		}
		if answer := standingAnswer(th); answer != nil || !time.Now().Before(deadline) {
			if asJSON {
				return encodeJSON(out, map[string]any{"post": th, "answer": answer, "assume": th.Assume, "answered": answer != nil})
			}
			if answer == nil {
				fmt.Fprintf(out, "no answer in %s — going ahead on: %s\n", wait, th.Assume)
				return nil
			}
			fmt.Fprintf(out, "answered by %s: %s\n", answer.AgentID, answer.Body)
			return nil
		}
		time.Sleep(answerPoll)
	}
}

// answerPoll is how often --wait looks for an answer.
var answerPoll = 3 * time.Second

func standingAnswer(th protocol.Post) *protocol.Post {
	for i := len(th.Replies) - 1; i >= 0; i-- {
		if r := th.Replies[i]; r.Kind == protocol.KindAnswer && !r.Superseded {
			return &th.Replies[i]
		}
	}
	return nil
}

func coolerReply(kind protocol.PostKind, args []string, out io.Writer) error {
	f := newCoolerFlags(string(kind))
	body := f.fs.String("body", "", "the reply")
	to := f.fs.String("to", "", "comma-separated addresses, beyond the thread's participants")
	id, h, err := f.parse(args, string(kind)+" <post> --body …")
	if err != nil {
		return err
	}
	var res protocol.PostResult
	in := protocol.PostInput{AgentID: *f.agent, Kind: kind, Body: *body, To: splitList(*to)}
	if err := h.JSON(http.MethodPost, postPath(id, "/replies"), in, &res); err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, res)
	}
	printPosted(out, res)
	return nil
}

func coolerStep(step string, args []string, out io.Writer) error {
	f := newCoolerFlags(step)
	body := f.fs.String("body", "", "what happened, or why")
	offer := f.fs.String("offer", "", "accept: the offer to accept")
	outcome := f.fs.String("outcome", "", "done: succeeded or failed")
	evidence := f.fs.String("evidence", "", "done: what proves it — commits, test runs")
	ttl := f.fs.String("ttl", "", "accept, take, renew: the lease (default and ceiling are the organization's)")
	id, h, err := f.parse(args, step+" <post>")
	if err != nil {
		return err
	}
	in := protocol.NeedAction{AgentID: *f.agent, OfferID: *offer, Body: *body, Outcome: protocol.Outcome(*outcome), Evidence: *evidence, TTL: *ttl}
	var res protocol.PostResult
	if err := h.JSON(http.MethodPost, postPath(id, "/", step), in, &res); err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, res)
	}
	if step == "renew" && res.Post.Owner != nil {
		fmt.Fprintf(out, "renewed %s until %s\n", res.Post.ID, res.Post.Owner.LeaseExpiresAt.Local().Format(time.Kitchen))
		return nil
	}
	printPosted(out, res)
	return nil
}

func coolerRead(args []string, out io.Writer) error {
	f := newCoolerFlags("read")
	limit := f.fs.Int("limit", 50, "threads to show")
	room, h, err := f.parse(args, "read <room>")
	if err != nil {
		return err
	}
	var posts []protocol.Post
	if err := h.JSON(http.MethodGet, roomPath(room, "/posts")+fmt.Sprintf("?limit=%d", *limit), nil, &posts); err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, posts)
	}
	if len(posts) == 0 {
		fmt.Fprintf(out, "nothing in %s yet\n", room)
	}
	now := time.Now()
	for _, p := range posts {
		fmt.Fprintln(out, postLine(p, now))
	}
	return nil
}

// postLine is a thread root on one line: id, kind and state, who, and what.
func postLine(p protocol.Post, now time.Time) string {
	tag := string(p.Kind)
	if p.State != "" {
		tag += "/" + string(p.State)
	}
	text := p.Subject
	if text == "" {
		text = p.Body
	}
	if len(text) > 100 {
		text = text[:97] + "..."
	}
	line := fmt.Sprintf("%s  %-15s %s: %s  (%s ago)", p.ID, tag, p.AgentID, text, claim.Claim{Created: p.LastActivityAt}.Age(now))
	if p.Owner != nil {
		line += "  — held by " + p.Owner.AgentID
	}
	if p.Room != "" {
		line = "[" + p.Room + "] " + line
	}
	return line
}

func coolerThread(args []string, out io.Writer) error {
	f := newCoolerFlags("thread")
	id, h, err := f.parse(args, "thread <post>")
	if err != nil {
		return err
	}
	var th protocol.Post
	if err := h.JSON(http.MethodGet, postPath(id), nil, &th); err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, th)
	}
	now := time.Now()
	fmt.Fprintln(out, postLine(th, now))
	if th.Subject != "" && th.Body != "" {
		fmt.Fprintf(out, "    %s\n", th.Body)
	}
	if th.Assume != "" {
		fmt.Fprintf(out, "    if unanswered: %s\n", th.Assume)
	}
	if len(th.Paths) > 0 {
		fmt.Fprintf(out, "    about %s:%s\n", th.Repo, strings.Join(th.Paths, ", "))
	}
	if th.Owner != nil {
		fmt.Fprintf(out, "    held by %s, lease until %s\n", th.Owner.AgentID, th.Owner.LeaseExpiresAt.Local().Format(time.Kitchen))
	}
	for _, r := range th.Replies {
		what := string(r.Kind)
		if r.Kind == protocol.KindUpdate {
			what = r.Step
		}
		if r.Superseded {
			what += " (superseded)"
		}
		line := fmt.Sprintf("  %s  %s %s", r.ID, r.AgentID, what)
		if r.Outcome != "" {
			line += " " + string(r.Outcome)
		}
		if r.Body != "" {
			line += ": " + r.Body
		}
		fmt.Fprintln(out, line)
		if r.Evidence != "" {
			fmt.Fprintf(out, "      evidence: %s\n", r.Evidence)
		}
	}
	return nil
}

func coolerRoll(args []string, out io.Writer) error {
	f := newCoolerFlags("roll")
	room, h, err := f.parse(args, "roll <room>")
	if err != nil {
		return err
	}
	var roll []protocol.RollEntry
	if err := h.JSON(http.MethodGet, roomPath(room, "/roll"), nil, &roll); err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, roll)
	}
	if len(roll) == 0 {
		fmt.Fprintf(out, "nobody in %s\n", room)
	}
	now := time.Now()
	for _, e := range roll {
		state := "present"
		if !e.Present {
			state = "absent"
			if e.LastSeenAt != nil {
				state += ", last seen " + claim.Claim{Created: *e.LastSeenAt}.Age(now) + " ago"
			}
		}
		line := fmt.Sprintf("%-28s %s", e.AgentID, state)
		if e.Runtime != "" {
			line += " (" + e.Runtime + ")"
		}
		fmt.Fprintln(out, line)
		if e.Status != nil {
			fmt.Fprintf(out, "    status: %s  (%s ago)\n", e.Status.Body, claim.Claim{Created: e.Status.CreatedAt}.Age(now))
		}
		for _, p := range e.Owns {
			fmt.Fprintf(out, "    holds %s %s: %s\n", p.ID, p.State, p.Subject)
		}
		for _, p := range e.Asking {
			fmt.Fprintf(out, "    asking %s: %s\n", p.ID, p.Subject)
		}
	}
	return nil
}

func coolerNeeds(args []string, out io.Writer) error {
	f := newCoolerFlags("needs")
	all := f.fs.Bool("all", false, "include taken, done and cancelled needs")
	repo := f.fs.String("repo", "", "only needs in this repository (default with --paths: this one)")
	paths := f.fs.String("paths", "", "only needs whose paths overlap these comma-separated globs")
	_, h, err := f.parse(args, "")
	if err != nil {
		return err
	}
	ps := splitList(*paths)
	if len(ps) > 0 && *repo == "" {
		cwd, _ := os.Getwd()
		*repo = gitinfo.Repo(cwd)
	}
	needs, err := fetchNeeds(h, *all, *repo, ps)
	if err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, needs)
	}
	if len(needs) == 0 {
		fmt.Fprintln(out, "the board is empty")
	}
	now := time.Now()
	for _, p := range needs {
		fmt.Fprintln(out, postLine(p, now))
	}
	return nil
}

func coolerNotes(args []string, out io.Writer) error {
	f := newCoolerFlags("notes")
	room := f.fs.String("room", "", "only this room's notes")
	repo := f.fs.String("repo", "", "the repository the paths are in (default: this one)")
	paths := f.fs.String("paths", "", "comma-separated globs you are about to touch")
	_, h, err := f.parse(args, "")
	if err != nil {
		return err
	}
	ps := splitList(*paths)
	if len(ps) > 0 && *repo == "" {
		cwd, _ := os.Getwd()
		*repo = gitinfo.Repo(cwd)
	}
	notes, err := fetchNotes(h, *room, *repo, ps)
	if err != nil {
		return err
	}
	if *f.asJSON {
		return encodeJSON(out, notes)
	}
	if len(notes) == 0 {
		fmt.Fprintln(out, "no active heads-ups")
	}
	fmt.Fprint(out, renderNotes(notes))
	return nil
}

// fetchNeeds is the board, narrowed to the ground given. A registry older than
// that narrowing ignores it and answers with the whole board, so it is applied
// here as well: an agent told about work in a repository it is not in learns
// to skip the list.
func fetchNeeds(h *client.HTTPStore, all bool, repo string, paths []string) ([]protocol.Post, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	if repo != "" {
		q.Set("repo", repo)
	}
	for _, p := range paths {
		q.Add("path", p)
	}
	var needs []protocol.Post
	if err := h.JSON(http.MethodGet, "/v1/needs?"+q.Encode(), nil, &needs); err != nil {
		return nil, err
	}
	query := claim.NormalizeAll(paths)
	out := []protocol.Post{}
	for _, n := range needs {
		if aboutGround(n, repo, query) {
			out = append(out, n)
		}
	}
	return out, nil
}

// aboutGround is the registry's rule for whether a post is about the ground
// asked for. With paths, the post must name overlapping ones; with only a
// repository, it must be scoped to that repository. A post that names no
// repository is not evidence that it is somewhere else, so it matches any
// when paths are asked for.
func aboutGround(p protocol.Post, repo string, query []string) bool {
	repo = strings.TrimSpace(repo)
	if len(query) == 0 {
		return repo == "" || strings.EqualFold(p.Repo, repo)
	}
	if len(p.Paths) == 0 || (repo != "" && p.Repo != "" && !strings.EqualFold(p.Repo, repo)) {
		return false
	}
	return len(claim.PatternsOverlap(query, p.Paths)) > 0
}

// renderNeeds is the needs an agent is being shown unasked, with where each
// one is, so it can tell whether the work is within reach.
func renderNeeds(needs []protocol.Post, now time.Time) string {
	var b strings.Builder
	for _, n := range needs {
		fmt.Fprintln(&b, "  "+postLine(n, now))
		if len(n.Paths) > 0 {
			fmt.Fprintf(&b, "      in %s:%s\n", n.Repo, strings.Join(n.Paths, ", "))
		}
	}
	return b.String()
}

// claimNeeds is the open needs on ground just claimed. The agent that has
// claimed the ground is often the one best placed to do the work somebody
// asked for there, and nobody else would think to tell it.
func claimNeeds(st client.Store, repo string, paths []string) []protocol.Post {
	h, ok := st.(*client.HTTPStore)
	if !ok {
		return nil
	}
	needs, err := fetchNeeds(h, false, repo, paths)
	if err != nil {
		return nil
	}
	me := agentID()
	out := needs[:0]
	for _, n := range needs {
		if n.AgentID != me {
			out = append(out, n)
		}
	}
	return out
}

func fetchNotes(h *client.HTTPStore, room, repo string, paths []string) ([]protocol.Post, error) {
	q := url.Values{}
	if room != "" {
		q.Set("room", room)
	}
	if repo != "" {
		q.Set("repo", repo)
	}
	for _, p := range paths {
		q.Add("path", p)
	}
	var notes []protocol.Post
	err := h.JSON(http.MethodGet, "/v1/notes?"+q.Encode(), nil, &notes)
	return notes, err
}

func renderNotes(notes []protocol.Post) string {
	var b strings.Builder
	for _, n := range notes {
		fmt.Fprintf(&b, "heads-up from %s in %s (%s): %s\n", n.AgentID, n.Room, n.ID, n.Body)
		if len(n.Paths) > 0 {
			fmt.Fprintf(&b, "    about %s:%s\n", n.Repo, strings.Join(n.Paths, ", "))
		}
	}
	return b.String()
}

// claimNotes is the heads-ups about ground just claimed. A warning that only
// reaches the agents present when it was written misses the ones most likely
// to walk into it, so a claim looks. Best effort: a registry without a
// watercooler, or a local file store, has nothing to say.
func claimNotes(st client.Store, repo string, paths []string) []protocol.Post {
	h, ok := st.(*client.HTTPStore)
	if !ok {
		return nil
	}
	notes, err := fetchNotes(h, "", repo, paths)
	if err != nil {
		return nil
	}
	return notes
}
