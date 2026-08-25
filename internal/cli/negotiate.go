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
func printProtocol(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
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
	return printProtocol(out, v)
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
	return printProtocol(out, v)
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
	return printProtocol(out, v)
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
	return printProtocol(out, v)
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
	return printProtocol(out, v)
}
