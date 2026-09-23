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
