// Package protocol defines the NDJSON wire format between agents and the broker.
// Each frame is a single JSON object terminated by a newline.
package protocol

import (
	"encoding/json"
	"errors"
	"time"
)

// Frame types.
const (
	TypeHello   = "hello"   // client -> broker, must be the first frame
	TypeWelcome = "welcome" // broker -> client, confirms registration
	TypeMsg     = "msg"     // bidirectional, chat payload
	TypeError   = "error"   // broker -> client, protocol or routing error
	TypeAck     = "ack"     // client -> broker receipt; broker -> client persisted acknowledgment

	TypeOperatorSend = "operator_send" // operator socket only: one request per connection
)

const (
	Version                = OriginVersion
	AcknowledgmentVersion  = 2
	DurableVersion         = 3
	OriginVersion          = 4
	StageAccepted          = "accepted"
	StageAdapterReceived   = "adapter_received"
	StageAgentAcknowledged = "agent_acknowledged"
)

// Origin is set by the broker from the connection, never accepted from a client.
const (
	OriginOperator = "operator"
	OriginAgent    = "agent"
	OriginUnknown  = "unknown"
)

// Broadcast is the reserved recipient for messages to all connected agents.
const Broadcast = "*"

// OperatorID is the reserved sender of operator messages; agents cannot claim it.
const OperatorID = "operator"

// MaxRecipients bounds broadcast and group membership.
const MaxRecipients = 256

// ValidateAgentID is shared by wire and MCP registration.
func ValidateAgentID(id string) error {
	if len(id) == 0 || len(id) > 128 {
		return errors.New("agent ID must be 1 to 128 bytes")
	}
	if id == Broadcast {
		return errors.New("agent ID cannot be *")
	}
	if id == OperatorID {
		return errors.New("agent ID operator is reserved")
	}
	return nil
}

// WithDisplayOrigin labels a delivered msg frame for a host; other frame types
// carry no origin.
func WithDisplayOrigin(msg Message) Message {
	if msg.Type == TypeMsg {
		msg.Origin = DisplayOrigin(msg.Origin)
	}
	return msg
}

// DisplayOrigin renders a missing origin as unknown, never as operator or agent.
func DisplayOrigin(origin string) string {
	switch origin {
	case OriginOperator, OriginAgent:
		return origin
	}
	return OriginUnknown
}

// Error codes sent in TypeError frames.
const (
	ErrExpectedHello         = "expected_hello"
	ErrDuplicateID           = "duplicate_id"
	ErrUnknownRecipient      = "unknown_recipient"
	ErrRecipientUnavailable  = "recipient_unavailable"
	ErrRecipientDisconnected = "recipient_disconnected"
	ErrMalformedFrame        = "malformed_frame"
	ErrMissingRecipient      = "missing_recipient"
	ErrInternal              = "internal"
	ErrDuplicateMessage      = "duplicate_message"
	ErrInvalidAck            = "invalid_ack"
	ErrInboxFull             = "inbox_full"
	ErrDurabilityUnavailable = "durability_unavailable"
	ErrForbidden             = "forbidden"
)

// Recipient freezes broadcast membership at acceptance, including inbox ownership.
type Recipient struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
}

// ReceiptState is retained per-recipient progress returned on an idempotent retry.
type ReceiptState struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id,omitempty"`
	Stage     string `json:"stage"`
}

// Message is the single frame type used in both directions.
// Fields are populated depending on Type.
type Message struct {
	Status          *StatusSnapshot `json:"status,omitempty"` // status response only
	Type            string          `json:"type"`
	ProtocolVersion int             `json:"protocol_version,omitempty"` // hello/welcome capability negotiation
	MessageID       string          `json:"message_id,omitempty"`
	InReplyTo       string          `json:"in_reply_to,omitempty"`
	AckRequested    bool            `json:"ack_requested,omitempty"`    // msg: request staged acknowledgments
	Durable         bool            `json:"durable,omitempty"`          // v3: retained until explicit agent acknowledgment
	Replayed        bool            `json:"replayed,omitempty"`         // delivery on a replacement connection
	Repeated        bool            `json:"repeated,omitempty"`         // accepted: idempotent send retry; no new delivery
	Stage           string          `json:"stage,omitempty"`            // ack
	Recipients      []Recipient     `json:"recipients,omitempty"`       // accepted: fixed membership
	Receipts        []ReceiptState  `json:"receipts,omitempty"`         // repeated acceptance: latest retained progress
	Seq             uint64          `json:"seq,omitempty"`              // broker-assigned global order (msg, welcome)
	AgentID         string          `json:"agent_id,omitempty"`         // hello, welcome
	SessionID       string          `json:"session_id,omitempty"`       // broker-assigned connection identity
	OwnerSessionID  string          `json:"owner_session_id,omitempty"` // duplicate_id: current inbox owner
	Harness         string          `json:"harness,omitempty"`          // hello: e.g. "claude-code 1.5"
	Model           string          `json:"model,omitempty"`            // hello: e.g. "claude-sonnet-4"
	From            string          `json:"from,omitempty"`             // msg (broker -> client)
	To              string          `json:"to,omitempty"`               // msg: agent id or "*"
	Origin          string          `json:"origin,omitempty"`           // msg (broker -> client): operator, agent or unknown
	Group           []string        `json:"group,omitempty"`            // operator_send: explicit recipient set
	Excluded        []string        `json:"excluded,omitempty"`         // operator accepted: connected agents left out of "*"
	Payload         json.RawMessage `json:"payload,omitempty"`          // msg: opaque JSON
	Code            string          `json:"code,omitempty"`             // error
	Detail          string          `json:"detail,omitempty"`           // error
	TS              *time.Time      `json:"ts,omitempty"`               // msg (broker -> client), UTC; pointer so zero is omitted
}
