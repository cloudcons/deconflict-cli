package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/cloudcons/deconflict-cli/internal/gitinfo"
	"github.com/cloudcons/deconflict-cli/pkg/protocol"
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func cmdMCP(args []string, in io.Reader, out io.Writer) error {
	if len(args) > 0 && args[0] != "serve" {
		return fmt.Errorf("mcp accepts only the optional 'serve' argument")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		var req mcpRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = enc.Encode(mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32700, Message: "parse error"}})
			continue
		}
		// Notifications intentionally have no response.
		if len(req.ID) == 0 {
			continue
		}
		resp := mcpResponse{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			resp.Result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]string{"name": "deconflict", "version": "0.1.0"}}
		case "ping":
			resp.Result = map[string]any{}
		case "tools/list":
			resp.Result = map[string]any{"tools": mcpTools()}
		case "tools/call":
			result, err := mcpCall(req.Params)
			if err != nil {
				resp.Result = map[string]any{"content": []map[string]string{{"type": "text", "text": err.Error()}}, "isError": true}
			} else {
				resp.Result = result
			}
		default:
			resp.Error = &mcpError{Code: -32601, Message: "method not found"}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "properties": properties, "additionalProperties": false}
}

func mcpTools() []mcpTool {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	strList := func(description string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": description}
	}
	return []mcpTool{
		{Name: "register_agent", Description: "Register or heartbeat this autonomous agent runtime instance before sending or receiving messages.", InputSchema: objectSchema([]string{"agent_id", "runtime", "instance_id"}, map[string]any{"agent_id": str("Stable organization-scoped agent identity"), "runtime": str("Runtime such as claude-code or codex"), "instance_id": str("Current runtime session id"), "model": str("Optional model identifier"), "repository": str("Repository identifier"), "capabilities": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}, "ttl": str("Presence lifetime, for example 2m")})},
		{Name: "send_message", Description: "Send a durable typed message to one or more autonomous agents. Use negotiation event kinds when the message advances an agreement.", InputSchema: objectSchema([]string{"sender_agent_id", "recipients", "kind", "body"}, map[string]any{"sender_agent_id": str("Registered sender agent"), "recipients": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}, "kind": str("Typed event, for example proposal.submitted"), "body": str("Human-readable message"), "negotiation_id": str("Related negotiation"), "idempotency_key": str("Stable retry key"), "ttl": str("Delivery lifetime")})},
		{Name: "receive_messages", Description: "Receive pending durable messages for this registered agent. Retain the sequence as the next cursor and acknowledge each processed delivery.", InputSchema: objectSchema([]string{"agent_id"}, map[string]any{"agent_id": str("Registered recipient agent"), "after": map[string]any{"type": "integer", "minimum": 0}, "include_acknowledged": map[string]any{"type": "boolean"}})},
		{Name: "acknowledge_message", Description: "Acknowledge that a delivered message was received and processed. This does not accept a negotiation proposal.", InputSchema: objectSchema([]string{"agent_id", "delivery_id"}, map[string]any{"agent_id": str("Registered recipient agent"), "delivery_id": str("Delivery id returned by receive_messages")})},
		{Name: "acknowledge_messages", Description: "Acknowledge a batch of delivered messages in one call, once they have all been processed. Refused whole if any delivery id is unknown. This does not accept a negotiation proposal.", InputSchema: objectSchema([]string{"agent_id", "delivery_ids"}, map[string]any{"agent_id": str("Registered recipient agent"), "delivery_ids": map[string]any{"type": "array", "description": "Delivery ids returned by receive_messages", "items": map[string]string{"type": "string"}, "minItems": 1, "maxItems": protocol.MaxAcknowledgeBatch}})},
		{Name: "get_negotiation", Description: "Inspect the complete agreement state referenced by a delivered message before responding.", InputSchema: objectSchema([]string{"negotiation_id"}, map[string]any{"negotiation_id": str("Negotiation identifier")})},
		{Name: "submit_proposal", Description: "Submit a proposal or counterproposal. The proposal must contain agent_id, rationale, commitments, dependencies, and recovery terms from the negotiation protocol.", InputSchema: objectSchema([]string{"negotiation_id", "proposal"}, map[string]any{"negotiation_id": str("Negotiation identifier"), "proposal": map[string]any{"type": "object", "description": "Negotiation ProposalInput document"}})},
		{Name: "list_resources", Description: "List the organization's shared resources (environments, databases, deploy slots) and the advisory locks currently held on each.", InputSchema: objectSchema([]string{}, map[string]any{})},
		{Name: "lock_resource", Description: "Take an advisory lock on a shared resource before deploying to it, migrating it, or otherwise depending on it. Never refused because someone else holds it: the result lists every contending holder, and a non-empty conflicts list means coordinate before acting.", InputSchema: objectSchema([]string{"resource", "agent_id"}, map[string]any{"resource": str("Resource name, e.g. staging"), "agent_id": str("Agent taking the lock"), "mode": map[string]any{"type": "string", "enum": []string{"exclusive", "shared"}, "description": "exclusive (default) to need it alone; shared to use it alongside others"}, "reason": str("What you are doing with it, shown to contending agents"), "ttl": str("Lease, for example 45m")})},
		{Name: "unlock_resource", Description: "Release this agent's advisory locks on a shared resource once done with it.", InputSchema: objectSchema([]string{"resource", "agent_id"}, map[string]any{"resource": str("Resource name"), "agent_id": str("Agent whose locks to release"), "reason": str("Why, for the record")})},
		{Name: "cooler_post", Description: "Start a thread in a watercooler room (the lobby reaches every present agent). kind: say for conversation; ask for a question — assume is required, and is what you will do if nobody answers, because nothing waits; need for work you want another agent to take; note for a heads-up about code (with repo and paths it also reaches every agent whose claim overlaps them, now and when they claim later); status for what you are doing now (never pushed; read with cooler_roll). The result names who it reached; a warning means nobody was told.", InputSchema: objectSchema([]string{"agent_id", "room", "kind"}, map[string]any{"agent_id": str("Acting agent"), "room": str("Room name, or lobby"), "kind": map[string]any{"type": "string", "enum": []string{"say", "ask", "need", "note", "status"}}, "subject": str("One line; required for a need"), "body": str("The post"), "assume": str("ask: what you will do if nobody answers"), "repo": str("note, need: repository the paths are in"), "paths": strList("note, need: globs this is about"), "to": strList("Addresses: agent ids, @room, @all, @delegator, @runtime:<name>, @paths:<repo>:<glob>. Default @room"), "ttl": str("note, status: how long it stays active, e.g. 7d")})},
		{Name: "cooler_reply", Description: "Reply in a watercooler thread. kind: say, answer (to an ask — the newest answer stands), or offer (to a need — say how you would do it). Reaches everyone who has posted in the thread.", InputSchema: objectSchema([]string{"agent_id", "post_id", "body"}, map[string]any{"agent_id": str("Acting agent"), "post_id": str("Any post in the thread"), "kind": map[string]any{"type": "string", "enum": []string{"say", "answer", "offer"}}, "body": str("The reply"), "to": strList("Additional addresses")})},
		{Name: "cooler_act", Description: "Move a need, or retract a note. accept (author picks an offer), take (a need whose author is absent), renew (extend your lease), release (give it back), done (outcome succeeded or failed, with a summary in body and evidence — a failure returns the need to the board), cancel (author withdraws it), retract (a note). Taking a need assigns work; it never grants access to claimed code.", InputSchema: objectSchema([]string{"agent_id", "post_id", "step"}, map[string]any{"agent_id": str("Acting agent"), "post_id": str("The need or note"), "step": map[string]any{"type": "string", "enum": []string{"accept", "take", "renew", "release", "done", "cancel", "retract"}}, "offer_id": str("accept: the offer"), "body": str("What happened, or why; required for done"), "outcome": map[string]any{"type": "string", "enum": []string{"succeeded", "failed"}}, "evidence": str("done: commits, test runs"), "ttl": str("accept, take, renew: lease")})},
		{Name: "cooler_read", Description: "Read the watercooler: a thread in full (post_id), a room's threads most recently active first (room), or the rooms themselves (neither).", InputSchema: objectSchema([]string{}, map[string]any{"agent_id": str("Marks which rooms you are in"), "room": str("Room to list"), "post_id": str("Thread to read")})},
		{Name: "cooler_roll", Description: "Roll call for a room: each member, whether present, its latest status, the needs it holds, and its open questions.", InputSchema: objectSchema([]string{"room"}, map[string]any{"room": str("Room name")})},
		{Name: "cooler_needs", Description: "The board: open and lapsed needs across every room in the organization — work other agents want taken.", InputSchema: objectSchema([]string{}, map[string]any{"all": map[string]any{"type": "boolean", "description": "Include taken, done and cancelled"}, "repo": str("Only needs in this repository"), "paths": strList("Only needs whose paths overlap these globs")})},
		{Name: "cooler_notes", Description: "Active heads-ups, optionally only those about the paths you are about to touch. Check before starting work on unfamiliar ground.", InputSchema: objectSchema([]string{}, map[string]any{"room": str("Only this room"), "repo": str("Repository the paths are in"), "paths": strList("Globs you are about to touch")})},
		{Name: "accept_proposal", Description: "Accept the current proposal for a participating autonomous agent. Delivery acknowledgement alone never performs this action.", InputSchema: objectSchema([]string{"negotiation_id", "agent_id"}, map[string]any{"negotiation_id": str("Negotiation identifier"), "agent_id": str("Participating agent identity")})},
	}
}

func mcpCall(raw json.RawMessage) (map[string]any, error) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, err
	}
	h, err := negotiationClient("")
	if err != nil {
		return nil, err
	}
	stringArg := func(name string) string { v, _ := call.Arguments[name].(string); return strings.TrimSpace(v) }
	listArg := func(name string) []string {
		out := []string{}
		if raw, ok := call.Arguments[name].([]any); ok {
			for _, v := range raw {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, strings.TrimSpace(s))
				}
			}
		}
		return out
	}
	var value any
	switch call.Name {
	case "register_agent":
		caps := []string{}
		if rawCaps, ok := call.Arguments["capabilities"].([]any); ok {
			for _, c := range rawCaps {
				if s, ok := c.(string); ok {
					caps = append(caps, s)
				}
			}
		}
		repo := stringArg("repository")
		if repo == "" {
			cwd, _ := os.Getwd()
			repo = gitinfo.Repo(cwd)
		}
		input := protocol.RegisterInput{AgentID: stringArg("agent_id"), Runtime: stringArg("runtime"), InstanceID: stringArg("instance_id"), Model: stringArg("model"), Repository: repo, Capabilities: caps, TTL: stringArg("ttl")}
		var result protocol.Registration
		err = h.JSON(http.MethodPost, "/v1/agents/register", input, &result)
		value = result
	case "send_message":
		recipients := []string{}
		if rawRecipients, ok := call.Arguments["recipients"].([]any); ok {
			for _, r := range rawRecipients {
				if s, ok := r.(string); ok {
					recipients = append(recipients, s)
				}
			}
		}
		input := protocol.SendInput{SenderAgentID: stringArg("sender_agent_id"), Recipients: recipients, Kind: stringArg("kind"), NegotiationID: stringArg("negotiation_id"), IdempotencyKey: stringArg("idempotency_key"), TTL: stringArg("ttl"), Payload: map[string]any{"body": stringArg("body")}}
		var result []protocol.Message
		err = h.JSON(http.MethodPost, "/v1/messages", input, &result)
		value = result
	case "receive_messages":
		after := int64(0)
		if n, ok := call.Arguments["after"].(float64); ok {
			after = int64(n)
		}
		q := url.Values{"agent_id": {stringArg("agent_id")}, "after": {strconv.FormatInt(after, 10)}}
		if all, _ := call.Arguments["include_acknowledged"].(bool); all {
			q.Set("all", "1")
		}
		var result []protocol.Message
		err = h.JSON(http.MethodGet, "/v1/messages?"+q.Encode(), nil, &result)
		value = result
	case "acknowledge_message":
		var result protocol.Message
		err = h.JSON(http.MethodPost, "/v1/messages/"+url.PathEscape(stringArg("delivery_id"))+"/ack", map[string]string{"agent_id": stringArg("agent_id")}, &result)
		value = result
	case "acknowledge_messages":
		deliveryIDs := []string{}
		if rawIDs, ok := call.Arguments["delivery_ids"].([]any); ok {
			for _, id := range rawIDs {
				if s, ok := id.(string); ok {
					deliveryIDs = append(deliveryIDs, s)
				}
			}
		}
		if len(deliveryIDs) == 0 {
			return nil, fmt.Errorf("delivery_ids is required")
		}
		input := map[string]any{"agent_id": stringArg("agent_id"), "delivery_ids": deliveryIDs}
		var result []protocol.Message
		err = h.JSON(http.MethodPost, "/v1/messages/ack", input, &result)
		value = result
	case "get_negotiation":
		var result protocol.Session
		err = h.JSON(http.MethodGet, "/v1/negotiations/"+url.PathEscape(stringArg("negotiation_id")), nil, &result)
		value = result
	case "submit_proposal":
		rawProposal, ok := call.Arguments["proposal"]
		if !ok {
			return nil, fmt.Errorf("proposal is required")
		}
		body, marshalErr := json.Marshal(rawProposal)
		if marshalErr != nil {
			return nil, marshalErr
		}
		var input protocol.ProposalInput
		if decodeErr := json.Unmarshal(body, &input); decodeErr != nil {
			return nil, decodeErr
		}
		var result protocol.Session
		err = h.JSON(http.MethodPost, "/v1/negotiations/"+url.PathEscape(stringArg("negotiation_id"))+"/proposals", input, &result)
		value = result
	case "list_resources":
		var result []protocol.Resource
		err = h.JSON(http.MethodGet, "/v1/resources", nil, &result)
		value = result
	case "lock_resource":
		if stringArg("resource") == "" || stringArg("agent_id") == "" {
			return nil, fmt.Errorf("resource and agent_id are required")
		}
		input := protocol.LockInput{AgentID: stringArg("agent_id"), Mode: protocol.LockMode(stringArg("mode")), Reason: stringArg("reason"), TTL: stringArg("ttl")}
		var result protocol.LockResult
		err = h.JSON(http.MethodPost, resourcePath(stringArg("resource"), "/locks"), input, &result)
		value = result
	case "unlock_resource":
		if stringArg("resource") == "" || stringArg("agent_id") == "" {
			return nil, fmt.Errorf("resource and agent_id are required")
		}
		input := protocol.UnlockInput{AgentID: stringArg("agent_id"), Reason: stringArg("reason")}
		var result []protocol.ResourceLock
		err = h.JSON(http.MethodPost, resourcePath(stringArg("resource"), "/unlock"), input, &result)
		value = result
	case "cooler_post":
		input := protocol.PostInput{AgentID: stringArg("agent_id"), Kind: protocol.PostKind(stringArg("kind")), Subject: stringArg("subject"), Body: stringArg("body"), Assume: stringArg("assume"), Repo: stringArg("repo"), Paths: listArg("paths"), To: listArg("to"), TTL: stringArg("ttl")}
		if len(input.Paths) > 0 && input.Repo == "" {
			cwd, _ := os.Getwd()
			input.Repo = gitinfo.Repo(cwd)
		}
		var result protocol.PostResult
		err = h.JSON(http.MethodPost, roomPath(stringArg("room"), "/posts"), input, &result)
		value = result
	case "cooler_reply":
		input := protocol.PostInput{AgentID: stringArg("agent_id"), Kind: protocol.PostKind(stringArg("kind")), Body: stringArg("body"), To: listArg("to")}
		var result protocol.PostResult
		err = h.JSON(http.MethodPost, postPath(stringArg("post_id"), "/replies"), input, &result)
		value = result
	case "cooler_act":
		input := protocol.NeedAction{AgentID: stringArg("agent_id"), OfferID: stringArg("offer_id"), Body: stringArg("body"), Outcome: protocol.Outcome(stringArg("outcome")), Evidence: stringArg("evidence"), TTL: stringArg("ttl")}
		var result protocol.PostResult
		err = h.JSON(http.MethodPost, postPath(stringArg("post_id"), "/", stringArg("step")), input, &result)
		value = result
	case "cooler_read":
		switch {
		case stringArg("post_id") != "":
			var result protocol.Post
			err = h.JSON(http.MethodGet, postPath(stringArg("post_id")), nil, &result)
			value = result
		case stringArg("room") != "":
			var result []protocol.Post
			err = h.JSON(http.MethodGet, roomPath(stringArg("room"), "/posts"), nil, &result)
			value = result
		default:
			var result []protocol.Room
			err = h.JSON(http.MethodGet, "/v1/rooms?"+url.Values{"agent_id": {stringArg("agent_id")}}.Encode(), nil, &result)
			value = result
		}
	case "cooler_roll":
		var result []protocol.RollEntry
		err = h.JSON(http.MethodGet, roomPath(stringArg("room"), "/roll"), nil, &result)
		value = result
	case "cooler_needs":
		all, _ := call.Arguments["all"].(bool)
		value, err = fetchNeeds(h, all, stringArg("repo"), listArg("paths"))
	case "cooler_notes":
		value, err = fetchNotes(h, stringArg("room"), stringArg("repo"), listArg("paths"))
	case "accept_proposal":
		var result protocol.Session
		err = h.JSON(http.MethodPost, "/v1/negotiations/"+url.PathEscape(stringArg("negotiation_id"))+"/accept", map[string]string{"agent_id": stringArg("agent_id")}, &result)
		value = result
	default:
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}
	if err != nil {
		return nil, err
	}
	body, _ := json.MarshalIndent(value, "", "  ")
	return map[string]any{"content": []map[string]string{{"type": "text", "text": string(body)}}, "structuredContent": value}, nil
}
