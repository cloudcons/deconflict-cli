package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPAdvertisesInteroperableMailboxTools(t *testing.T) {
	in := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	var out bytes.Buffer
	if err := cmdMCP(nil, in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("responses = %q", out.String())
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &response); err != nil {
		t.Fatal(err)
	}
	result := response["result"].(map[string]any)
	tools := result["tools"].([]any)
	names := map[string]bool{}
	for _, item := range tools {
		names[item.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"register_agent", "send_message", "receive_messages", "acknowledge_message", "acknowledge_messages", "get_negotiation", "submit_proposal", "accept_proposal"} {
		if !names[want] {
			t.Errorf("missing %s", want)
		}
	}
}
