package protocol

import "time"

// Live operator session limits.
const (
	SessionEventQueue   = 256
	SessionInFlight     = 8
	SessionControlSlots = 2
	SnapshotAgents      = StatusLimit
	SnapshotRecentSent  = 16
	StagesDefault       = 64
	StagesMax           = 256
)

// Session event kinds.
const (
	EventConversationChanged = "conversation_changed"
	EventInboxChanged        = "inbox_changed"
	EventStageChanged        = "stage_changed"
	EventPresenceChanged     = "presence_changed"
	EventResyncRequired      = "resync_required"
)

// SessionCapabilities are advertised in the operator_session response.
var SessionCapabilities = []string{"send", "receive", "received", "ack", "snapshot", "message_stages", "events"}

// Watermark orders session state: Revision increases by one per event within
// an Epoch, which is new on every broker start.
type Watermark struct {
	Epoch    string `json:"epoch"`
	Revision uint64 `json:"revision"`
}

type SessionInfo struct {
	SessionID    string    `json:"session_id"`
	Epoch        string    `json:"epoch,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"`
	OpenedAt     time.Time `json:"opened_at"`
}

// RecipientStage is a current delivery state; it can move back to accepted
// when a reconnect moves the delivery to a new session.
type RecipientStage struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Stage     string `json:"stage"`
}

// SentSummary describes an operator-sent message; it never carries the payload.
type SentSummary struct {
	MessageID         string           `json:"message_id"`
	Seq               uint64           `json:"seq"`
	TS                time.Time        `json:"ts"`
	To                string           `json:"to"`
	Recipients        []RecipientStage `json:"recipients"`
	ReceiptsTruncated bool             `json:"receipts_truncated"`
}

type AgentPresence struct {
	AgentID         string `json:"agent_id"`
	SessionID       string `json:"session_id"`
	ProtocolVersion int    `json:"protocol_version"`
}

// OperatorSnapshot is a bounded view; omitted entities are unknown, not absent.
type OperatorSnapshot struct {
	Watermark           Watermark       `json:"watermark"`
	Agents              []AgentPresence `json:"agents"`
	AgentsTotal         int             `json:"agents_total"`
	AgentsTruncated     bool            `json:"agents_truncated"`
	InboxHeadSeq        uint64          `json:"inbox_head_seq"`
	InboxPending        int             `json:"inbox_pending"`
	RecentSent          []SentSummary   `json:"recent_sent"`
	RecentSentTruncated bool            `json:"recent_sent_truncated"`
}

type StagePage struct {
	Watermark      Watermark        `json:"watermark"`
	MessageID      string           `json:"message_id"`
	Recipients     []RecipientStage `json:"recipients"`
	NextAfterAgent string           `json:"next_after_agent"`
	More           bool             `json:"more"`
}

// SessionEvent is a notification, never a payload.
type SessionEvent struct {
	Kind            string    `json:"kind"`
	Watermark       Watermark `json:"watermark"`
	Seq             uint64    `json:"seq,omitempty"`
	MessageID       string    `json:"message_id,omitempty"`
	Direction       string    `json:"direction,omitempty"`
	HeadSeq         uint64    `json:"head_seq,omitempty"`
	Pending         *int      `json:"pending,omitempty"`
	AgentID         string    `json:"agent_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	Stage           string    `json:"stage,omitempty"`
	State           string    `json:"state,omitempty"`
	ProtocolVersion int       `json:"protocol_version,omitempty"`
}
