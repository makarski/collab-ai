package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

// predicate runs under the listener mutex; it never consumes the inbox.
func awaitListener(t *testing.T, l *Listener, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		l.mu.Lock()
		ok := predicate()
		l.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener condition timed out: %+v", l.Status())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestListenerSustainedDeliveryWithoutReceive(t *testing.T) {
	p := &testPublisher{messages: make(chan protocol.Message, 1)}
	l, peer := listenerFixture(t, p)
	ctx := context.Background()
	const total = 2*maxInboxMessages + 10
	for i := range total {
		id := fmt.Sprintf("sustained-%d", i)
		sendTracked(t, peer, SendRequest{To: "worker", Text: "review", MessageID: id})
		assertEqual(t, "published", awaitPublished(t, p).MessageID, id)
		assertStage(t, nextFrame(t, peer), id, protocol.StageAccepted)
		assertStage(t, nextFrame(t, peer), id, protocol.StageAdapterReceived)
		requireNoError(t, l.Acknowledge(ctx, id))
		assertStage(t, nextFrame(t, peer), id, protocol.StageAgentAcknowledged)
	}
	awaitListener(t, l, func() bool {
		for _, q := range l.queue {
			if q.message.Type == protocol.TypeMsg {
				return false
			}
		}
		return l.status.Submitted == total
	})
	status := l.Status()
	assertEqual(t, "still listening", status.State, "listening_delivery_unconfirmed")
	assertEqual(t, "bounded history", status.BufferedFrames, maxInboxMessages)
	assertEqual(t, "history evictions", status.ReceiptsDropped, uint64(total-maxInboxMessages))
	assertEqual(t, "all acknowledgments persisted", brokerStatus(t, peer.conn.RemoteAddr().String()).DurablePending.Total, 0)
	assertInboxAccounting(t, l)
}

func assertInboxAccounting(t *testing.T, l *Listener) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	bytes := 0
	for _, q := range l.queue {
		data, err := json.Marshal(q.message)
		requireNoError(t, err)
		assertEqual(t, "frame bytes", q.size, len(data))
		bytes += q.size
	}
	assertEqual(t, "total bytes", l.bytes, bytes)
}

func TestListenerOnlyOwnConfirmedAcknowledgmentReleasesMessage(t *testing.T) {
	l := NewListener(context.Background(), lazyAgent(t, "/unused", "worker"), nil)
	l.status.SessionID = "owner-session"
	msg := protocol.Message{Type: protocol.TypeMsg, MessageID: "reply", InReplyTo: "request", From: "peer"}
	requireNoError(t, l.retain(msg))
	for _, receipt := range []protocol.Message{
		{Type: protocol.TypeAck, MessageID: "reply", Stage: protocol.StageAdapterReceived, SessionID: "owner-session"},
		{Type: protocol.TypeAck, MessageID: "reply", Stage: protocol.StageAgentAcknowledged, SessionID: "other-session"},
		{Type: protocol.TypeAck, MessageID: "reply", Stage: protocol.StageAgentAcknowledged},
		{Type: protocol.TypeError, MessageID: "reply", Code: protocol.ErrInvalidAck},
	} {
		requireNoError(t, l.retain(receipt))
		assertEqual(t, "unconfirmed copy retained", l.queue[0].message.Type, protocol.TypeMsg)
	}
	requireNoError(t, l.retain(protocol.Message{Type: protocol.TypeAck, MessageID: "reply", Stage: protocol.StageAgentAcknowledged, SessionID: "owner-session"}))
	for _, q := range l.queue {
		if q.message.Type == protocol.TypeMsg {
			t.Fatal("confirmed message still retained")
		}
	}
	assertInboxAccounting(t, l)
}

func TestListenerReceiptPressurePreservesReplyAndError(t *testing.T) {
	l := NewListener(context.Background(), lazyAgent(t, "/unused", "worker"), nil)
	l.started = true // Pure queue fixture, without a pump.
	l.status.State = "listening_delivery_unconfirmed"
	wanted := protocol.Message{Type: protocol.TypeMsg, MessageID: "answer", InReplyTo: "request", From: "peer"}
	failure := protocol.Message{Type: protocol.TypeError, MessageID: "failed", Code: protocol.ErrUnknownRecipient}
	requireNoError(t, l.retain(wanted))
	requireNoError(t, l.retain(failure))
	for i := range 2 * maxInboxMessages {
		requireNoError(t, l.retain(protocol.Message{Type: protocol.TypeAck, MessageID: fmt.Sprint(i)}))
	}
	out, err := l.WaitReply(context.Background(), "request", "peer", time.Second)
	requireNoError(t, err)
	assertReply(t, out, wanted)
	assertEqual(t, "truncation visible to selective reader", out.Inbox.ReceiptsDropped, l.Status().ReceiptsDropped)
	assertEqual(t, "error retained", l.queue[0].message.Code, failure.Code)
	assertInboxAccounting(t, l)
}

func TestListenerReceiptCannotDisplaceFullMessageBacklog(t *testing.T) {
	l := NewListener(context.Background(), lazyAgent(t, "/unused", "worker"), nil)
	for range maxInboxMessages {
		requireNoError(t, l.retain(protocol.Message{Type: protocol.TypeMsg}))
	}
	requireNoError(t, l.retain(protocol.Message{Type: protocol.TypeAck}))
	assertEqual(t, "incoming receipt dropped", l.Status().ReceiptsDropped, uint64(1))
	assertEqual(t, "messages retained", len(l.queue), maxInboxMessages)
	assertErrorContains(t, l.retain(protocol.Message{Type: protocol.TypeError}), "overflow")
	assertInboxAccounting(t, l)
}

func TestListenerEvictsReceiptsAtByteLimit(t *testing.T) {
	l := NewListener(context.Background(), lazyAgent(t, "/unused", "worker"), nil)
	receipt := protocol.Message{Type: protocol.TypeAck, Detail: strings.Repeat("x", maxInboxBytes/3)}
	for range 4 {
		requireNoError(t, l.retain(receipt))
	}
	assertEqual(t, "byte pressure evictions", l.Status().ReceiptsDropped, uint64(2))
	assertInboxAccounting(t, l)
}
