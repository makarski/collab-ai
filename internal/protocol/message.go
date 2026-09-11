// Package protocol defines the NDJSON wire format between agents and the broker.
// Each frame is a single JSON object terminated by a newline.
package protocol

import (
	"encoding/json"
	"time"
)

// Frame types.
const (
	TypeHello   = "hello"   // client -> broker, must be the first frame
	TypeWelcome = "welcome" // broker -> client, confirms registration
	TypeMsg     = "msg"     // bidirectional, chat payload
	TypeError   = "error"   // broker -> client, protocol or routing error
)

// Broadcast is the reserved recipient for messages to all connected agents.
const Broadcast = "*"

// Error codes sent in TypeError frames.
const (
	ErrExpectedHello        = "expected_hello"
	ErrDuplicateID          = "duplicate_id"
	ErrUnknownRecipient     = "unknown_recipient"
	ErrRecipientUnavailable = "recipient_unavailable"
	ErrMalformedFrame       = "malformed_frame"
	ErrMissingRecipient     = "missing_recipient"
	ErrInternal             = "internal"
)

// Message is the single frame type used in both directions.
// Fields are populated depending on Type.
type Message struct {
	Type    string          `json:"type"`
	Seq     uint64          `json:"seq,omitempty"`      // broker-assigned global order (msg, welcome)
	AgentID string          `json:"agent_id,omitempty"` // hello, welcome
	Harness string          `json:"harness,omitempty"`  // hello: e.g. "claude-code 1.5"
	Model   string          `json:"model,omitempty"`    // hello: e.g. "claude-sonnet-4"
	From    string          `json:"from,omitempty"`     // msg (broker -> client)
	To      string          `json:"to,omitempty"`       // msg: agent id or "*"
	Payload json.RawMessage `json:"payload,omitempty"`  // msg: opaque JSON
	Code    string          `json:"code,omitempty"`     // error
	Detail  string          `json:"detail,omitempty"`   // error
	TS      *time.Time      `json:"ts,omitempty"`       // msg (broker -> client), UTC; pointer so zero is omitted
}
