package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type ackRequest struct {
	Path        string
	AgentID     string   `json:"agent_id"`
	DeliveryIDs []string `json:"delivery_ids"`
}

func ackServer(t *testing.T, got *[]ackRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in ackRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		in.Path = r.URL.Path
		*got = append(*got, in)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages/ack") {
			_ = json.NewEncoder(w).Encode([]map[string]string{{"delivery_id": "dlv_a"}, {"delivery_id": "dlv_b"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": "dlv_a"})
	}))
}

// A session's mail arrives as a batch and is worked through as a batch, so
// saying so must cost one call rather than one per message. Before bulk
// acknowledgement existed, a mailbox that the session hook no longer consumes
// could only be cleared a message at a time.
func TestMessageAckClearsAProcessedBatchInOneCall(t *testing.T) {
	var got []ackRequest
	server := ackServer(t, &got)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")

	var out bytes.Buffer
	if err := cmdMessage([]string{"ack", "dlv_a", "dlv_b", "--agent", "bo/claude", "dlv_c", "--ids", "dlv_d,dlv_e", "--store", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("acknowledging five deliveries made %d requests, want 1", len(got))
	}
	if got[0].Path != "/v1/messages/ack" {
		t.Errorf("path = %q, want the batch endpoint", got[0].Path)
	}
	if got[0].AgentID != "bo/claude" {
		t.Errorf("agent_id = %q", got[0].AgentID)
	}
	// Ids typed before the flags, after them, and through --ids are all ids.
	want := []string{"dlv_a", "dlv_b", "dlv_c", "dlv_d", "dlv_e"}
	if strings.Join(got[0].DeliveryIDs, ",") != strings.Join(want, ",") {
		t.Errorf("delivery_ids = %v, want %v", got[0].DeliveryIDs, want)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"delivery_id": "dlv_b"`)) {
		t.Errorf("output = %s", out.String())
	}
}

// One id keeps the request and the single-object output it has always had.
// Every installed hook and skill in the wild types this form.
func TestMessageAckOfOneDeliveryIsUnchanged(t *testing.T) {
	var got []ackRequest
	server := ackServer(t, &got)
	defer server.Close()
	t.Setenv("DECONFLICT_TOKEN", "test-agent-token")

	var out bytes.Buffer
	if err := cmdMessage([]string{"ack", "dlv_a", "--agent", "bo/claude", "--store", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "/v1/messages/dlv_a/ack" {
		t.Fatalf("requests = %+v", got)
	}
	if strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Errorf("single acknowledgement printed a list: %s", out.String())
	}
}

func TestMessageAckWithoutADeliveryIDIsRefused(t *testing.T) {
	var out bytes.Buffer
	if err := cmdMessage([]string{"ack", "--agent", "bo/claude"}, &out); err == nil {
		t.Fatal("acknowledged nothing without complaining")
	}
}
