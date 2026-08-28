package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/cloudcons/deconflict/internal/gitinfo"
	"github.com/cloudcons/deconflict/internal/negotiation"
	"github.com/cloudcons/deconflict/internal/store"
)

const negotiateUsage = `deconflict negotiate — coordination protocol for autonomous agents

  request     request scoped access for an objective
  list        list active negotiations
  show        inspect one negotiation
  propose     submit a proposal or counterproposal JSON document
  accept      accept the current proposal under an agent delegation
  checkpoint  publish checkpoint evidence
  recover     enter the negotiated recovery procedure
  deviate     report that the agreement no longer matches the work, and reopen it
  ask         put a question to the other agents that is not yours to decide
  resolve     settle another agent's question from what you already know
  defer       hand a question up to the humans, when it is not the agents' to settle
  explain     answer what a person put back to you before they would rule
  answer      answer a question that was deferred to you
  lease       renew the agreement lease
  complete    complete an agreement after commitments are satisfied
  plans       show execution plans derived from current agreements
`

func cmdNegotiate(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, negotiateUsage)
		return nil
	}
	switch args[0] {
	case "request":
		return negotiateRequest(args[1:], out)
	case "list":
		return negotiateList(args[1:], out)
	case "show":
		return negotiateShow(args[1:], out)
	case "propose":
		return negotiatePropose(args[1:], out)
	case "accept":
		return negotiateAccept(args[1:], out)
	case "checkpoint":
		return negotiateCheckpoint(args[1:], out)
	case "recover":
		return negotiateRecover(args[1:], out)
	case "deviate":
		return negotiateDeviate(args[1:], out)
	case "ask":
		return negotiateAsk(args[1:], out)
	case "answer":
		return negotiateAnswer(args[1:], out)
	case "resolve":
		return negotiateResolve(args[1:], out)
	case "defer":
		return negotiateDefer(args[1:], out)
	case "explain":
		return negotiateExplain(args[1:], out)
	case "lease":
		return negotiateLease(args[1:], out)
	case "complete":
		return negotiateComplete(args[1:], out)
	case "plans":
		return negotiatePlans(args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, negotiateUsage)
		return nil
	default:
		return fmt.Errorf("unknown negotiate command %q", args[0])
	}
}

func negotiationClient(dsn string) (*store.HTTPStore, error) {
	st, err := openStore(dsn)
	if err != nil {
		return nil, err
	}
	h, ok := st.(*store.HTTPStore)
	if !ok {
		return nil, fmt.Errorf("negotiation requires an http(s) registry; got %s", st.Describe())
	}
	return h, nil
}
func negotiateRequest(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("negotiate request", flag.ContinueOnError)
	paths := fs.String("paths", "", "comma-separated paths or globs")
	objective := fs.String("objective", "", "objective summary")
	success := fs.String("success", "", "comma-separated success criteria")
	access := fs.String("access", "modify", "inspect, modify, or exclusive_modify")
	scope := fs.String("scope", "", "what the access is for, e.g. 'tests only'")
	lease := fs.String("lease", "45m", "requested coordination lease")
	runtime := fs.String("runtime", "", "agent runtime")
	model := fs.String("model", "", "agent model")
	instance := fs.String("instance", "", "agent instance id")
	priority := fs.String("priority", "", "objective priority")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *paths == "" || *objective == "" {
		return fmt.Errorf("--paths and --objective are required")
	}
	cwd, _ := os.Getwd()
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	in := negotiation.AccessRequest{Repository: gitinfo.Repo(cwd), Agent: negotiation.AgentIdentity{ID: agentID(), Name: agentID(), Runtime: *runtime, Model: *model, Instance: *instance}, Objective: negotiation.Objective{ID: negotiation.NewID("obj_"), Summary: strings.TrimSpace(*objective), SuccessCriteria: splitList(*success), Priority: *priority}, Resources: []negotiation.ResourceRequest{{Paths: splitList(*paths), Access: *access, Scope: strings.TrimSpace(*scope)}}, Lease: negotiation.Lease{Duration: *lease}}
	var v negotiation.Session
	if err = h.JSON(http.MethodPost, "/v1/negotiations", in, &v); err != nil {
		return err
	}
	return encodeJSON(out, v)
}

func negotiateList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("negotiate list", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository filter")
	all := fs.Bool("all", false, "include completed negotiations")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	q := url.Values{}
	if *repo != "" {
		q.Set("repo", *repo)
	}
	if *all {
		q.Set("all", "1")
	}
	path := "/v1/negotiations"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var v []negotiation.Session
	if err = h.JSON(http.MethodGet, path, nil, &v); err != nil {
		return err
	}
	return encodeJSON(out, v)
}
func negotiateShow(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate show", flag.ContinueOnError)
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("negotiation id is required")
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	var v negotiation.Session
	if err = h.JSON(http.MethodGet, "/v1/negotiations/"+url.PathEscape(id), nil, &v); err != nil {
		return err
	}
	return encodeJSON(out, v)
}
func readProtocolFile(path string, out any) error {
	if path == "" {
		return fmt.Errorf("--file is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func negotiatePropose(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate propose", flag.ContinueOnError)
	file := fs.String("file", "", "proposal JSON file")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("negotiation id is required")
	}
	var in negotiation.ProposalInput
	if err := readProtocolFile(*file, &in); err != nil {
		return err
	}
	return protocolMutation(*dsn, id, "proposals", in, out)
}
func negotiateAccept(args []string, out io.Writer) error {
	return simpleAgentMutation("accept", args, out)
}
func negotiateComplete(args []string, out io.Writer) error {
	return simpleAgentMutation("complete", args, out)
}
func simpleAgentMutation(action string, args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate "+action, flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("negotiation id is required")
	}
	return protocolMutation(*dsn, id, action, map[string]string{"agent_id": *agent}, out)
}
func negotiateCheckpoint(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate checkpoint", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	commitment := fs.String("commitment", "", "commitment id")
	checkpoint := fs.String("checkpoint", "", "checkpoint id")
	evidence := fs.String("evidence", "", "artifact, commit, or test evidence")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" || *commitment == "" || *checkpoint == "" {
		return fmt.Errorf("negotiation id, --commitment, and --checkpoint are required")
	}
	return protocolMutation(*dsn, id, "checkpoints", negotiation.CheckpointInput{AgentID: *agent, CommitmentID: *commitment, CheckpointID: *checkpoint, Evidence: *evidence}, out)
}
func negotiateRecover(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate recover", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	reason := fs.String("reason", "", "recovery reason")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" || strings.TrimSpace(*reason) == "" {
		return fmt.Errorf("negotiation id and --reason are required")
	}
	return protocolMutation(*dsn, id, "recovery", negotiation.RecoveryInput{AgentID: *agent, Reason: *reason}, out)
}

// negotiateDeviate is the way to amend an agreement that is already running.
// Proposals are refused during execution so that nobody can rewrite the terms
// underneath an agent acting on them; saying plainly that the terms are wrong
// pauses the work and reopens them.
func negotiateDeviate(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate deviate", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	commitment := fs.String("commitment", "", "commitment the deviation was found in")
	reason := fs.String("reason", "", "what the agreement says, and what is actually true")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" || strings.TrimSpace(*reason) == "" {
		return fmt.Errorf("negotiation id and --reason are required")
	}
	return protocolMutation(*dsn, id, "deviation", negotiation.DeviationInput{AgentID: *agent, CommitmentID: *commitment, Reason: *reason}, out)
}

// negotiateAsk is for the questions that were never the agents' to settle.
//
// Not for coordination: which of you goes first is yours, and a human called in
// to referee it knows less about the code than either of you by then. This is
// for authority — may I break this interface, may I commit — where an answer
// invented by an agent is worth nothing.
//
// Nothing waits. --assume says what you will do if nobody replies, and silence
// means exactly that.
func negotiateAsk(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate ask", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	scope := fs.String("scope", "mandate", "mandate (your own delegator) or agreement (every participant's)")
	subject := fs.String("subject", "", "the question, in one line")
	body := fs.String("body", "", "what you have established, and what turns on the answer")
	assume := fs.String("assume", "", "what you will do if nobody answers")
	commitment := fs.String("commitment", "", "the commitment this bears on")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" || strings.TrimSpace(*subject) == "" || strings.TrimSpace(*assume) == "" {
		return fmt.Errorf("negotiation id, --subject and --assume are required")
	}
	return protocolMutation(*dsn, id, "questions", negotiation.QuestionInput{
		AgentID: *agent, Scope: *scope, Subject: *subject, Body: *body,
		Assume: *assume, CommitmentID: *commitment,
	}, out)
}

func negotiateAnswer(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate answer", flag.ContinueOnError)
	question := fs.String("question", "", "question id")
	answer := fs.String("answer", "", "the answer")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" || *question == "" || strings.TrimSpace(*answer) == "" {
		return fmt.Errorf("negotiation id, --question and --answer are required")
	}
	return protocolMutation(*dsn, id, "answers", negotiation.AnswerInput{QuestionID: *question, Answer: *answer}, out)
}

// negotiateResolve is one agent settling another's question, which is the rung
// below a human and where most questions should end.
func negotiateResolve(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate resolve", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	question := fs.String("question", "", "question id")
	answer := fs.String("answer", "", "what you know that settles it")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" || *question == "" || strings.TrimSpace(*answer) == "" {
		return fmt.Errorf("negotiation id, --question and --answer are required")
	}
	return protocolMutation(*dsn, id, "answers", negotiation.AnswerInput{
		QuestionID: *question, Answer: *answer, AgentID: *agent,
	}, out)
}

// negotiateDefer hands a question up to the humans, and is the only thing that
// does. Nothing else escalates — no timer, no unanswered-for-long-enough.
func negotiateDefer(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate defer", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	question := fs.String("question", "", "question id")
	reason := fs.String("reason", "", "what about this is not the agents' to settle")
	needs := fs.String("needs", "decision", "what the person must supply: decision or clarification")
	note := fs.String("note", "", "when deferring somebody else's question: what you looked at and why you cannot settle it")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" || *question == "" || strings.TrimSpace(*reason) == "" {
		return fmt.Errorf("negotiation id, --question and --reason are required")
	}
	return protocolMutation(*dsn, id, "deferrals", negotiation.DeferInput{
		QuestionID: *question, AgentID: *agent, Reason: *reason, Needs: *needs, Note: *note,
	}, out)
}

// negotiateExplain answers what a person put back to you before they would
// rule. It does not settle your question — it returns it to them.
func negotiateExplain(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate explain", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	question := fs.String("question", "", "question id")
	reply := fs.String("reply", "", "what they asked you for")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" || *question == "" || strings.TrimSpace(*reply) == "" {
		return fmt.Errorf("negotiation id, --question and --reply are required")
	}
	return protocolMutation(*dsn, id, "explanations", negotiation.ExplainInput{
		QuestionID: *question, AgentID: *agent, Reply: *reply,
	}, out)
}

func negotiateLease(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("negotiate lease", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "delegated agent id")
	duration := fs.String("duration", "45m", "new lease duration")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("negotiation id is required")
	}
	return protocolMutation(*dsn, id, "lease", negotiation.LeaseInput{AgentID: *agent, Duration: *duration}, out)
}
func protocolMutation(dsn, id, action string, in any, out io.Writer) error {
	h, err := negotiationClient(dsn)
	if err != nil {
		return err
	}
	var v negotiation.Session
	if err = h.JSON(http.MethodPost, "/v1/negotiations/"+url.PathEscape(id)+"/"+action, in, &v); err != nil {
		return err
	}
	return encodeJSON(out, v)
}
func negotiatePlans(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("negotiate plans", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository filter")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	path := "/v1/execution-plans"
	if *repo != "" {
		path += "?repo=" + url.QueryEscape(*repo)
	}
	var v []negotiation.ExecutionPlan
	if err = h.JSON(http.MethodGet, path, nil, &v); err != nil {
		return err
	}
	return encodeJSON(out, v)
}
