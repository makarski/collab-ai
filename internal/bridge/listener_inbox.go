package bridge

import "collab-ai/internal/protocol"

func (l *Listener) statusLocked() ListenerStatus {
	out := l.status
	out.BufferedFrames, out.BufferedBytes = len(l.queue), l.bytes
	return out
}

func (l *Listener) inboxLocked() Inbox {
	return Inbox{Messages: []protocol.Message{}, Connected: l.status.State == "listening_delivery_unconfirmed",
		SessionID: l.status.SessionID, Error: l.status.Error,
		AcknowledgmentsSupported: l.status.AcknowledgmentsSupported, DurabilitySupported: l.status.DurabilitySupported,
		ReceiptsDropped: l.status.ReceiptsDropped}
}

// Only the broker's persistence confirmation for this receiving session releases
// a fallback message. Host writes and acknowledgments from peers cannot do so.
func (l *Listener) releaseAcknowledgedLocked(msg protocol.Message) {
	if msg.Type != protocol.TypeAck || msg.Stage != protocol.StageAgentAcknowledged {
		return
	}
	if msg.SessionID == "" || msg.SessionID != l.status.SessionID {
		return
	}
	for i := len(l.queue) - 1; i >= 0; i-- {
		queued := l.queue[i].message
		if queued.Type == protocol.TypeMsg && queued.MessageID == msg.MessageID {
			l.removeLocked(i)
		}
	}
}

// Receipts are bounded diagnostic history. Evict only receipts, preserving the
// order of all surviving frames, including unacknowledged messages and errors.
func (l *Listener) makeRoomLocked(size int) bool {
	for len(l.queue) >= maxInboxMessages || l.bytes+size > maxInboxBytes {
		index := -1
		for i, q := range l.queue {
			if q.message.Type == protocol.TypeAck {
				index = i
				break
			}
		}
		if index == -1 {
			return false
		}
		l.removeLocked(index)
		l.status.ReceiptsDropped++
	}
	return true
}

func (l *Listener) removeLocked(index int) {
	l.bytes -= l.queue[index].size
	copy(l.queue[index:], l.queue[index+1:])
	l.queue[len(l.queue)-1] = queuedMessage{}
	l.queue = l.queue[:len(l.queue)-1]
}
