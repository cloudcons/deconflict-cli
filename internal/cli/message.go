package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudcons/deconflict/internal/gitinfo"
	"github.com/cloudcons/deconflict/internal/messaging"
)

const messageUsage = `deconflict message — interoperable mailbox for autonomous agents

  register   register or heartbeat this runtime instance
  send       send a typed message to one or more agents
  inbox      receive durable pending messages
  watch      wait for and print messages as they arrive
  ack        acknowledge one or more delivered messages
  presence   show live instances for an agent
`

func cmdMessage(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, messageUsage)
		return nil
	}
	switch args[0] {
	case "register":
		return messageRegister(args[1:], out)
	case "send":
		return messageSend(args[1:], out)
	case "inbox":
		return messageInbox(args[1:], out)
	case "watch":
		return messageWatch(args[1:], out)
	case "ack":
		return messageAck(args[1:], out)
	case "presence":
		return messagePresence(args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, messageUsage)
		return nil
	default:
		return fmt.Errorf("unknown message command %q", args[0])
	}
}

func runtimeName() string {
	if v := strings.TrimSpace(os.Getenv("DECONFLICT_RUNTIME")); v != "" {
		return v
	}
	if os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" || os.Getenv("CLAUDE_CODE_SSE_PORT") != "" {
		return "claude-code"
	}
	if os.Getenv("CODEX_HOME") != "" {
		return "codex"
	}
	return "agent"
}

func defaultInstance() string {
	for _, key := range []string{"DECONFLICT_INSTANCE", "CLAUDE_CODE_SESSION_ID", "CODEX_SESSION_ID"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	host, _ := os.Hostname()
	return agentID() + "@" + host
}

func messageRegister(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("message register", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "stable autonomous agent id")
	runtime := fs.String("runtime", runtimeName(), "claude-code, codex, or another runtime")
	instance := fs.String("instance", defaultInstance(), "runtime session or instance id")
	model := fs.String("model", "", "model identifier")
	capabilities := fs.String("capabilities", "messaging,negotiation", "comma-separated capabilities")
	ttl := fs.String("ttl", "2m", "presence lifetime")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	in := messaging.RegisterInput{AgentID: *agent, Name: *agent, Runtime: *runtime, Model: *model, InstanceID: *instance, Repository: gitinfo.Repo(cwd), Capabilities: splitList(*capabilities), TTL: *ttl}
	var r messaging.Registration
	if err = h.JSON(http.MethodPost, "/v1/agents/register", in, &r); err != nil {
		return err
	}
	return printProtocol(out, r)
}

func messageSend(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("message send", flag.ContinueOnError)
	from := fs.String("from", agentID(), "sender agent id")
	to := fs.String("to", "", "comma-separated recipient agent ids")
	kind := fs.String("kind", "message", "message or negotiation event kind")
	negotiationID := fs.String("negotiation", "", "related negotiation id")
	body := fs.String("body", "", "message body")
	idempotency := fs.String("idempotency-key", "", "retry-safe caller key")
	ttl := fs.String("ttl", "24h", "message lifetime")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" || *body == "" {
		return fmt.Errorf("--to and --body are required")
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	in := messaging.SendInput{SenderAgentID: *from, Recipients: splitList(*to), Kind: *kind, NegotiationID: *negotiationID, Payload: map[string]any{"body": *body}, IdempotencyKey: *idempotency, TTL: *ttl}
	var items []messaging.Message
	if err = h.JSON(http.MethodPost, "/v1/messages", in, &items); err != nil {
		return err
	}
	return printProtocol(out, items)
}

func messageInbox(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("message inbox", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "recipient agent id")
	after := fs.Int64("after", 0, "only messages after this sequence")
	all := fs.Bool("all", false, "include acknowledged messages")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	q := url.Values{"agent_id": {*agent}, "after": {strconv.FormatInt(*after, 10)}}
	if *all {
		q.Set("all", "1")
	}
	var items []messaging.Message
	if err = h.JSON(http.MethodGet, "/v1/messages?"+q.Encode(), nil, &items); err != nil {
		return err
	}
	return printProtocol(out, items)
}

func messageWatch(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("message watch", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "recipient agent id")
	after := fs.Int64("after", 0, "initial sequence cursor")
	wait := fs.Duration("wait", 10*time.Minute, "maximum wait time")
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		var buf strings.Builder
		if err := messageInbox([]string{"--agent", *agent, "--after", strconv.FormatInt(*after, 10), "--store", *dsn}, &buf); err != nil {
			return err
		}
		if strings.TrimSpace(buf.String()) != "[]" {
			_, err := io.WriteString(out, buf.String())
			return err
		}
		time.Sleep(2 * time.Second)
	}
	return nil
}

// takeIDs is takeID for a batch: every leading argument up to the first flag.
func takeIDs(args []string) ([]string, []string) {
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return args[:i:i], args[i:]
		}
	}
	return args, nil
}

// parseIDs parses flags that have ids mixed in among them. flag stops at the
// first argument that is not a flag, so `dlv_a --agent bo dlv_b --store …`
// would otherwise silently drop --store and send the batch wherever the
// environment happened to point — which is a bad way to find out that an
// acknowledgement went to the wrong registry. Each pass consumes at least the
// id that stopped the last one, so this terminates.
func parseIDs(fs *flag.FlagSet, args []string) ([]string, error) {
	ids := []string{}
	for {
		leading, rest := takeIDs(args)
		ids = append(ids, leading...)
		if len(rest) == 0 {
			return ids, nil
		}
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		args = fs.Args()
	}
}

// messageAck acknowledges a whole processed batch, because that is the unit an
// agent works in: a session hook hands over everything pending, the agent acts
// on it, and one call says so. Ids may be given as arguments or through
// --ids; either side of the flags is accepted, since `ack dlv_a --agent x
// dlv_b` is what a shell makes easy to type.
func messageAck(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("message ack", flag.ContinueOnError)
	agent := fs.String("agent", agentID(), "recipient agent id")
	list := fs.String("ids", "", "comma-separated delivery ids")
	dsn := fs.String("store", "", "registry URL")
	ids, err := parseIDs(fs, args)
	if err != nil {
		return err
	}
	ids = append(ids, splitList(*list)...)
	if len(ids) == 0 {
		return fmt.Errorf("delivery id is required")
	}
	if len(ids) > messaging.MaxAcknowledgeBatch {
		return fmt.Errorf("a batch acknowledges at most %d deliveries, got %d", messaging.MaxAcknowledgeBatch, len(ids))
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	// One id keeps the single-delivery response it has always printed; asking
	// for a batch is what produces a batch.
	if len(ids) == 1 {
		var item messaging.Message
		if err = h.JSON(http.MethodPost, "/v1/messages/"+url.PathEscape(ids[0])+"/ack", map[string]string{"agent_id": *agent}, &item); err != nil {
			return err
		}
		return printProtocol(out, item)
	}
	in := map[string]any{"agent_id": *agent, "delivery_ids": ids}
	var items []messaging.Message
	if err = h.JSON(http.MethodPost, "/v1/messages/ack", in, &items); err != nil {
		return err
	}
	return printProtocol(out, items)
}

func messagePresence(args []string, out io.Writer) error {
	id, rest := takeID(args)
	fs := flag.NewFlagSet("message presence", flag.ContinueOnError)
	dsn := fs.String("store", "", "registry URL")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if id == "" {
		id = agentID()
	}
	h, err := negotiationClient(*dsn)
	if err != nil {
		return err
	}
	var items []messaging.Registration
	if err = h.JSON(http.MethodGet, "/v1/agents/"+url.PathEscape(id)+"/presence", nil, &items); err != nil {
		return err
	}
	return printProtocol(out, items)
}
