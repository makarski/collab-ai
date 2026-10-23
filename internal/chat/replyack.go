package chat

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A reply acknowledges the message it answers once the broker accepts the
// reply. The acknowledgment is bound to that message as it was when the
// reply was sent, and it is an ensure-ack: never a cancel, never a second
// send, and quiet on the happy path.

// bindAck captures the reply target as it is now, so the acknowledgment the
// reply makes later is for exactly this message and content.
func (m *Model) bindAck() *ackBinding {
	r := m.compose.reply
	if r == nil {
		return nil
	}
	b := &ackBinding{id: r.messageID, seq: r.seq}
	if e := m.timeline.entries[r.messageID]; e != nil {
		b.hash, b.epoch = e.hash, e.epoch
	}
	return b
}

// autoAck runs a reply's bound acknowledgment once, on the broker's
// acceptance of the reply. It is an ensure-ack: an acknowledgment already
// done, queued or on the wire is left alone, and a target that changed is
// queued suspended with the reason on its row rather than acknowledged.
func (m *Model) autoAck(in *intent) tea.Cmd {
	b := in.Ack
	if b == nil {
		return nil
	}
	in.Ack = nil
	e := m.timeline.entries[b.id]
	if e == nil || b.hash == "" {
		m.status = fmt.Sprintf("automatic acknowledgment of #%d could not be completed", b.seq)
		return nil
	}
	if e.direction != "received" || m.ackState(e) != AckNeeded {
		return nil
	}
	m.inbox.quiet[b.id] = true
	if reason := m.ackMismatch(b, e); reason != "" {
		m.inbox.queuedAcks[b.id] = &queuedAck{hash: b.hash, suspended: reason}
		return nil
	}
	return m.ensureAck(e)
}

// ackMismatch is why the bound message can't be acknowledged as it stands:
// its content changed, or the broker restarted since the reply was sent.
func (m *Model) ackMismatch(b *ackBinding, e *entry) string {
	switch {
	case e.conflict || e.hash != b.hash:
		return "content changed"
	case b.epoch != m.epoch:
		return "broker restarted"
	}
	return ""
}

// ensureAck sends the acknowledgment now, or queues it until the receipt is
// confirmed, without touching the status line.
func (m *Model) ensureAck(e *entry) tea.Cmd {
	id := e.msg.MessageID
	if e.receipt == ReceiptConfirmed {
		return m.request(kindAck, protocol.Message{Type: protocol.TypeOperatorAck, MessageID: id}, id)
	}
	m.inbox.queuedAcks[id] = &queuedAck{hash: e.hash}
	return nil
}
