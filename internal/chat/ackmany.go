package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Request kinds for inbox management.
const (
	kindInboxStatus = "inbox_status"
	kindAckMany     = "ack_many"
)

// PickerAck lists received messages that still need the operator's
// acknowledgment; Enter acknowledges the selected one, nothing before that.
const PickerAck = "ack"

// bulkAck is one /ack all or /ack from run: the inbox status read first, then
// stable pages under a frozen upper bound.
type bulkAck struct {
	from    string
	sel     protocol.AckSelection
	running bool
	result  protocol.AckManyResult
}

// BulkAckView is the state of a bulk acknowledgment, for tests and the CLI.
type BulkAckView struct {
	Running                                    bool
	Acknowledged, AlreadyAcknowledged, Skipped int
}

// ackCommand: /ack opens the picker, /ack all and /ack from <id> acknowledge
// in bulk, /ack #n targets one message as before.
func (m *Model) ackCommand(arg argument) tea.Cmd {
	switch {
	case arg.empty():
		m.openAckPicker()
		return nil
	case arg == "all":
		return m.startBulkAck("")
	case strings.HasPrefix(string(arg), "from "):
		return m.startBulkAck(strings.TrimSpace(strings.TrimPrefix(string(arg), "from ")))
	}
	e, reason := m.target(arg, noEntry)
	if e == nil {
		m.status = reason
		return nil
	}
	return m.acknowledge(e)
}

// ackArg accepts nothing, "all", "from <id>" or "#n".
func ackArg(a argument) error {
	s := string(a)
	switch {
	case a.empty(), s == "all":
		return nil
	case strings.HasPrefix(s, "from "):
		if err := protocol.ValidateAgentID(strings.TrimSpace(strings.TrimPrefix(s, "from "))); err != nil {
			return err
		}
		return nil
	}
	return targetArg(a)
}

// openAckPicker lists rows that need acknowledgment, newest first.
func (m *Model) openAckPicker() {
	var ids []string
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		if e := m.timeline.order[i]; m.targetable(e) && m.unacknowledged(e) {
			ids = append(ids, e.msg.MessageID)
		}
	}
	if len(ids) == 0 {
		m.status = "nothing to acknowledge"
		return
	}
	m.compose.picker = &picker{kind: PickerAck, items: ids}
}

// pickAck acknowledges the picked row through the normal /ack path.
func (m *Model) pickAck(id string) tea.Cmd {
	m.compose.picker = nil
	e := m.timeline.entries[id]
	if e == nil {
		m.status = "that message is no longer loaded"
		return nil
	}
	return m.acknowledge(e)
}

// ackLabel renders one picker row: "#seq sender · first line".
func (m *Model) ackLabel(id string) string {
	e := m.timeline.entries[id]
	if e == nil {
		return "  " + safe(id)
	}
	line, _, _ := strings.Cut(payloadText(e.msg.Payload), "\n")
	if e.omitted {
		line = "[text not available]"
	}
	return fmt.Sprintf("  #%d %s · %s", e.msg.Seq, agentStyle(e.msg.From).Render(safe(e.msg.From)), safeText(line))
}

// startBulkAck reads the inbox status first, so the pages run under a frozen
// upper bound and start just before the oldest unacknowledged row.
func (m *Model) startBulkAck(from string) tea.Cmd {
	if !m.ready() {
		m.status = "not connected"
		return nil
	}
	if m.bulk != nil && m.bulk.running {
		m.status = "a bulk acknowledgment is already running"
		return nil
	}
	m.bulk = &bulkAck{from: from, running: true}
	return m.request(kindInboxStatus, protocol.Message{Type: protocol.TypeOperatorInboxStatus})
}

// inboxStatusResponse keeps the usage for the header and, when a bulk run
// is waiting for it, starts the first page.
func (m *Model) inboxStatusResponse(_ pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorInboxStatus || resp.Inbox == nil {
		return m.bulkFailed("inbox status not read: " + brokerReason(resp))
	}
	m.usage = resp.Inbox
	b := m.bulk
	if b == nil || !b.running || b.sel.UpperSeq != 0 {
		return nil
	}
	if resp.Inbox.Unacknowledged == 0 || resp.Inbox.HeadSeq == 0 {
		m.bulk = nil
		m.status = "nothing to acknowledge"
		return nil
	}
	b.sel = protocol.AckSelection{From: b.from, UpperSeq: resp.Inbox.HeadSeq, Limit: protocol.AckManyDefault}
	if resp.Inbox.OldestUnacknowledgedSeq > 0 {
		b.sel.AfterSeq = resp.Inbox.OldestUnacknowledgedSeq - 1
	}
	return m.ackManyPage()
}

func (m *Model) ackManyPage() tea.Cmd {
	sel := m.bulk.sel
	return m.request(kindAckMany, protocol.Message{Type: protocol.TypeOperatorAckMany, AckSelection: &sel})
}

// ackManyResponse applies one page: receipts mark rows acknowledged, then the
// next page continues under the same upper bound, or the run ends.
func (m *Model) ackManyResponse(_ pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeAck || resp.AckMany == nil {
		return m.bulkFailed("bulk acknowledgment failed: " + brokerReason(resp))
	}
	for _, r := range resp.Receipts {
		if e := m.timeline.entries[r.MessageID]; e != nil && r.Stage == protocol.StageAgentAcknowledged {
			e.operatorStage = protocol.StageAgentAcknowledged
		}
	}
	b := m.bulk
	if b == nil {
		return nil
	}
	b.result.Acknowledged += resp.AckMany.Acknowledged
	b.result.AlreadyAcknowledged += resp.AckMany.AlreadyAcknowledged
	b.result.Skipped += resp.AckMany.Skipped
	if resp.AckMany.More {
		b.sel.AfterSeq = resp.AckMany.NextAfterSeq
		return m.ackManyPage()
	}
	b.running = false
	m.status = fmt.Sprintf("acknowledged %d (%d already, %d not yet received)", b.result.Acknowledged, b.result.AlreadyAcknowledged, b.result.Skipped)
	return nil
}

func (m *Model) bulkFailed(reason string) tea.Cmd {
	if m.bulk != nil {
		m.bulk.running = false
	}
	m.status = reason
	return nil
}

// sendRefused tells the operator which bound refused an agent's message,
// since only the operator can free it, and refreshes the usage.
func (m *Model) sendRefused(ev *protocol.SessionEvent) tea.Cmd {
	m.status = safe(ev.AgentID) + "'s message was refused: " + safe(ev.Detail)
	if !m.ready() || m.requested(kindInboxStatus, "") {
		return nil
	}
	return m.request(kindInboxStatus, protocol.Message{Type: protocol.TypeOperatorInboxStatus})
}

// ackReminder is the header's call to action while anything needs
// acknowledgment, and the warning when a sender is near its bound.
func (m *Model) ackReminder() string {
	if m.inbox.pending == 0 {
		return ""
	}
	text := fmt.Sprintf("%d need ack · /ack all", m.inbox.pending)
	if m.usage == nil {
		return text
	}
	for _, u := range m.usage.Senders {
		if u.Unacknowledged*4 >= m.usage.Limits.SenderMessages*3 || u.Bytes*4 >= m.usage.Limits.SenderBytes*3 {
			state := "near bound"
			if u.Unacknowledged >= m.usage.Limits.SenderMessages || u.Bytes >= m.usage.Limits.SenderBytes {
				state = "AT BOUND"
			}
			text += fmt.Sprintf(" · %s %d/%d %s", safe(u.AgentID), u.Unacknowledged, m.usage.Limits.SenderMessages, state)
		}
	}
	return text
}

func (m *Model) bulkView() *BulkAckView {
	if m.bulk == nil {
		return nil
	}
	return &BulkAckView{Running: m.bulk.running, Acknowledged: m.bulk.result.Acknowledged, AlreadyAcknowledged: m.bulk.result.AlreadyAcknowledged, Skipped: m.bulk.result.Skipped}
}
