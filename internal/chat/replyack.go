package chat

import (
	"fmt"
	"slices"

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
	return m.ensureBound(b)
}

// ensureBound runs one binding: an ensure-ack for the bound message as it
// stands, suspended when it changed, reported when it is gone.
func (m *Model) ensureBound(b *ackBinding) tea.Cmd {
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

// bindAudience captures, for a plain direct or group send, the loaded,
// adopted and unacknowledged messages from each intended recipient: a message
// to X says the operator has seen what X sent so far. Replies bind only
// their target and broadcasts bind nothing.
func (m *Model) bindAudience(scope protocol.Message, inReplyTo string) []ackBinding {
	if inReplyTo != "" || scope.To == protocol.Broadcast {
		return nil
	}
	ids := scope.Group
	if scope.To != "" {
		ids = []string{scope.To}
	}
	var out []ackBinding
	for _, e := range m.timeline.order {
		if e.direction != "received" || !slices.Contains(ids, e.msg.From) || !m.adoptedForAck(e) {
			continue
		}
		out = append(out, ackBinding{id: e.msg.MessageID, seq: e.msg.Seq, hash: e.hash, epoch: e.epoch, sender: e.msg.From})
	}
	return out
}

// adoptedForAck reports a message the audience acknowledgment may bind: read
// in the current epoch, adopted through the inbox (history and snapshot
// reads are non-consuming and leave no receipt), with its body, and not yet
// acknowledged, queued or on the wire.
func (m *Model) adoptedForAck(e *entry) bool {
	return m.targetable(e) && e.receipt != ReceiptNone && !e.omitted && m.ackState(e) == AckNeeded
}

// autoAckBatch consumes the audience bindings once, for the recipients the
// broker actually accepted.
func (m *Model) autoAckBatch(in *intent) tea.Cmd {
	batch := in.AckBatch
	in.AckBatch = nil
	accepted := map[string]bool{}
	for _, r := range in.Recipients {
		accepted[r.AgentID] = true
	}
	var cmds []tea.Cmd
	for i := range batch {
		if accepted[batch[i].sender] {
			cmds = append(cmds, m.ensureBound(&batch[i]))
		}
	}
	return tea.Batch(cmds...)
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
