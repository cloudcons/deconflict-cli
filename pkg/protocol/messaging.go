package protocol

import "time"

type Registration struct {
	ID           string         `json:"id"`
	AgentID      string         `json:"agent_id"`
	Name         string         `json:"name,omitempty"`
	Runtime      string         `json:"runtime"`
	Model        string         `json:"model,omitempty"`
	InstanceID   string         `json:"instance_id"`
	Repository   string         `json:"repository,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	DelegatedBy  string         `json:"delegated_by"`
	Delegation   string         `json:"delegation"`
	LastSeenAt   time.Time      `json:"last_seen_at"`
	ExpiresAt    time.Time      `json:"expires_at"`
}

type RegisterInput struct {
	AgentID      string         `json:"agent_id"`
	Name         string         `json:"name,omitempty"`
	Runtime      string         `json:"runtime"`
	Model        string         `json:"model,omitempty"`
	InstanceID   string         `json:"instance_id"`
	Repository   string         `json:"repository,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	TTL          string         `json:"ttl,omitempty"`
}

type Message struct {
	DeliveryID     string         `json:"delivery_id"`
	ID             string         `json:"id"`
	Sequence       int64          `json:"sequence"`
	SenderAgentID  string         `json:"sender_agent_id"`
	RecipientID    string         `json:"recipient_agent_id"`
	NegotiationID  string         `json:"negotiation_id,omitempty"`
	Kind           string         `json:"kind"`
	Payload        map[string]any `json:"payload,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	DeliveredAt    *time.Time     `json:"delivered_at,omitempty"`
	AcknowledgedAt *time.Time     `json:"acknowledged_at,omitempty"`
}

type SendInput struct {
	SenderAgentID  string         `json:"sender_agent_id"`
	Recipients     []string       `json:"recipients"`
	NegotiationID  string         `json:"negotiation_id,omitempty"`
	Kind           string         `json:"kind"`
	Payload        map[string]any `json:"payload,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	TTL            string         `json:"ttl,omitempty"`
}


// MaxAcknowledgeBatch bounds one bulk acknowledgement. It matches the inbox
// page size, so a batch can always clear exactly what a read handed over.
const MaxAcknowledgeBatch = 200
