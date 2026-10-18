//go:build linux

package server

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func chatExchange(t *testing.T, tr chat.Transport, req protocol.Message) protocol.Message {
	t.Helper()
	id, err := tr.Request(req)
	operatorNoError(t, err)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case frame, ok := <-tr.Incoming():
			if !ok {
				t.Fatal("chat connection closed before response")
			}
			if frame.Type == protocol.TypeEvent {
				continue
			}
			operatorEqual(t, "correlated response", frame.RequestID, id)
			return frame
		case <-deadline.C:
			t.Fatal("chat response timed out")
		}
	}
}

func TestOperatorChatTransportAgainstRealBroker(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	author := f.peer("author", protocol.Version)
	inboxSend(t, author, inboxMessage("chat-incoming"))
	// Wait for the operator listener without opening an owning session.
	operatorDial(t, f.operator).Close()
	dial := chat.SocketDialer(f.operator, time.Second)
	tr, info, err := dial(context.Background())
	operatorNoError(t, err)
	t.Cleanup(func() { tr.Close() })
	operatorEqual(t, "session identity", info.SessionID != "", true)
	other, _, err := dial(context.Background())
	if other != nil {
		other.Close()
		t.Fatal("second owner admitted")
	}
	var busy *chat.OwnerBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("expected typed owner_busy, got %v", err)
	}
	operatorEqual(t, "owner identity", busy.Owner.SessionID, info.SessionID)

	snapshot := chatExchange(t, tr, protocol.Message{Type: protocol.TypeOperatorSnapshot})
	if snapshot.Snapshot == nil {
		t.Fatal("missing snapshot")
	}
	operatorEqual(t, "pending inbox", snapshot.Snapshot.InboxPending, 1)
	history := chatExchange(t, tr, protocol.Message{Type: protocol.TypeOperatorHistory, PageRequest: &protocol.PageRequest{}})
	if history.History == nil || len(history.History.Entries) != 1 {
		t.Fatalf("history = %#v", history.History)
	}
	operatorEqual(t, "history remains non-consuming", inboxReceiptCount(f, "received_at"), 0)
	page := chatExchange(t, tr, protocol.Message{Type: protocol.TypeOperatorReceive, PageRequest: &protocol.PageRequest{}})
	if page.Page == nil || len(page.Page.Messages) != 1 {
		t.Fatalf("inbox = %#v", page.Page)
	}
	operatorEqual(t, "adopted identity", page.Page.Messages[0].MessageID, "chat-incoming")
	operatorEqual(t, "read remains non-consuming", inboxReceiptCount(f, "received_at"), 0)
	receipt := chatExchange(t, tr, protocol.Message{Type: protocol.TypeOperatorReceived, MessageIDs: []string{"chat-incoming"}})
	operatorEqual(t, "confirmed receipt", receipt.Stage, protocol.StageAdapterReceived)
	inboxStage(t, author, "chat-incoming", protocol.StageAdapterReceived)
	operatorEqual(t, "receipt is not human acknowledgment", inboxReceiptCount(f, "acknowledged_at"), 0)
	ack := chatExchange(t, tr, protocol.Message{Type: protocol.TypeOperatorAck, MessageID: "chat-incoming"})
	operatorEqual(t, "explicit acknowledgment", ack.Stage, protocol.StageAgentAcknowledged)
	inboxStage(t, author, "chat-incoming", protocol.StageAgentAcknowledged)
	request := operatorRequest("chat-reply", "author")
	request.InReplyTo = "chat-incoming"
	accepted := chatExchange(t, tr, request)
	requireOperatorAccepted(t, accepted, "author")
	delivered := author.read()
	operatorEqual(t, "reply correlation", delivered.InReplyTo, "chat-incoming")
	operatorEqual(t, "operator provenance", delivered.Origin, protocol.OriginOperator)
	retry := chatExchange(t, tr, request)
	operatorEqual(t, "same intent retained", retry.Repeated, true)
	operatorEqual(t, "same sequence", retry.Seq, accepted.Seq)
}
