package chat

import (
	"sort"

	"collab-ai/internal/protocol"
)

// StateView is a read-only snapshot of the chat for tests and the CLI.
type StateView struct {
	Connection        string
	Session           protocol.SessionInfo
	Epoch             string
	Revision          uint64
	Baseline          bool
	Entries           []EntryView
	Selected          int
	Agents            []protocol.AgentPresence
	AgentsTotal       int
	AgentsPartial     bool
	InboxPending      int
	InboxHeadSeq      uint64
	Unread            int
	Paused            bool
	Intent            *IntentView
	Confirm           *ConfirmView
	QueuedAcks        []QueuedAckView
	InFlight          int
	Queued            int
	Focus             string
	Recipients        string
	Draft             string
	InReplyTo         string
	Status            string
	Help              bool
	KeyDisambiguation bool
	ExitCode          int
	Fatal             string
}

// EntryView is one timeline message.
type EntryView struct {
	MessageID       string
	Seq             uint64
	Direction       string
	From            string
	To              string
	Origin          string
	InReplyTo       string
	Body            string
	BodyOmitted     bool
	Receipt         string
	OperatorStage   string
	Stages          []protocol.RecipientStage
	StagesTruncated bool
	Conflict        bool
	GapBelow        bool
}

type IntentView struct {
	MessageID  string
	Scope      protocol.Message
	InReplyTo  string
	State      string
	Recipients []protocol.Recipient
	Excluded   []string
	Detail     string
}

type ConfirmView struct {
	Preview int
}

type QueuedAckView struct {
	MessageID string
	Suspended string
}

// State returns a copy; changing it doesn't affect the model.
func (m *Model) State() StateView {
	v := StateView{
		Connection: m.conn, Session: m.session, Epoch: m.epoch, Revision: m.revision, Baseline: m.baseline,
		Selected: m.timeline.selected, Agents: append([]protocol.AgentPresence(nil), m.agents.list...),
		AgentsTotal: m.agents.total, AgentsPartial: m.agents.partial, InboxPending: m.inbox.pending,
		InboxHeadSeq: m.inbox.headSeq, Unread: m.timeline.unread, Paused: m.timeline.paused,
		InFlight: m.inFlight(), Queued: len(m.outbox), Focus: m.focus, Recipients: m.recipients.Value(),
		Draft: m.editor.Value(), InReplyTo: m.send.inReplyTo, Status: m.status, Help: m.help,
		KeyDisambiguation: m.keyboard, ExitCode: m.exitCode, Fatal: m.fatal,
	}
	for _, e := range m.timeline.order {
		v.Entries = append(v.Entries, e.view())
	}
	if in := m.send.intent; in != nil {
		v.Intent = &IntentView{MessageID: in.ID, Scope: in.Scope, InReplyTo: in.InReplyTo, State: in.State, Recipients: in.Recipients, Excluded: in.Excluded, Detail: in.Detail}
	}
	if c := m.send.confirm; c != nil {
		v.Confirm = &ConfirmView{Preview: c.preview}
	}
	for id, q := range m.inbox.queuedAcks {
		v.QueuedAcks = append(v.QueuedAcks, QueuedAckView{MessageID: id, Suspended: q.suspended})
	}
	sort.Slice(v.QueuedAcks, func(i, j int) bool { return v.QueuedAcks[i].MessageID < v.QueuedAcks[j].MessageID })
	return v
}

func (e *entry) view() EntryView {
	v := EntryView{MessageID: e.msg.MessageID, Seq: e.msg.Seq, Direction: e.direction, From: e.msg.From, To: e.msg.To,
		Origin: protocol.DisplayOrigin(e.msg.Origin), InReplyTo: e.msg.InReplyTo, BodyOmitted: e.omitted,
		Receipt: e.receipt, OperatorStage: e.operatorStage, StagesTruncated: e.stagesTruncated, Conflict: e.conflict, GapBelow: e.gapBelow}
	if !e.omitted {
		v.Body = payloadText(e.msg.Payload)
	}
	for _, st := range e.stages {
		v.Stages = append(v.Stages, st.RecipientStage)
	}
	sort.Slice(v.Stages, func(i, j int) bool { return v.Stages[i].AgentID < v.Stages[j].AgentID })
	return v
}
