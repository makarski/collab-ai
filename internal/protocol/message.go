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
)

const (
	Version                = DurableVersion
	AcknowledgmentVersion  = 2
	DurableVersion         = 3
	StageAccepted          = "accepted"
	StageAdapterReceived   = "adapter_received"
	StageAgentAcknowledged = "agent_acknowledged"
)

// Broadcast is the reserved recipient for messages to all connected agents.
const Broadcast = "*"

// ValidateAgentID is shared by wire and MCP registration.
func ValidateAgentID(id string) error {
	if len(id) == 0 || len(id) > 128 {
		return errors.New("agent ID must be 1 to 128 bytes")
	}
	if id == Broadcast {
		return errors.New("agent ID cannot be *")
	}
	return nil
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
	Payload         json.RawMessage `json:"payload,omitempty"`          // msg: opaque JSON
	Code            string          `json:"code,omitempty"`             // error
	Detail          string          `json:"detail,omitempty"`           // error
	TS              *time.Time      `json:"ts,omitempty"`               // msg (broker -> client), UTC; pointer so zero is omitted
}
