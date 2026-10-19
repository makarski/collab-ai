package bridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRenamedChannelMessagingAcceptance(t *testing.T) {
	path, _ := startBroker(t)
	publisher := &testPublisher{messages: make(chan protocol.Message, 4)}
	listener := NewListener(context.Background(), lazyAgent(t, path, "renamed-worker"), publisher)
	t.Cleanup(listener.Close)
	session := connectAutoChannel(t, listener)
	if session.InitializeResult().ServerInfo.Name != "pairtask" {
		t.Fatalf("old MCP implementation name: %q", session.InitializeResult().ServerInfo.Name)
	}
	if session.InitializeResult().Capabilities.Experimental["claude/channel"] == nil {
		t.Fatal("rename lost the Claude channel capability")
	}
	awaitListener(t, listener, func() bool { return listener.started })
	owner := listener.Status().SessionID
	peer := connectAgent(t, path, "peer")

	call(t, session, "send", map[string]any{"to": "peer", "text": "review", "message_id": "renamed-outbound"})
	message := nextFrame(t, peer)
	assertEqual(t, "outbound ID", message.MessageID, "renamed-outbound")
	assertEqual(t, "outbound sender", message.From, "renamed-worker")
	requireNoError(t, peer.Acknowledge(context.Background(), message.MessageID))
	_, err := peer.SendMessage(context.Background(), SendRequest{To: "renamed-worker", Text: "reviewed",
		MessageID: "renamed-inbound", InReplyTo: "renamed-outbound"})
	requireNoError(t, err)
	delivered := awaitPublished(t, publisher)
	assertEqual(t, "inbound ID", delivered.MessageID, "renamed-inbound")
	assertEqual(t, "inbound correlation", delivered.InReplyTo, "renamed-outbound")
	assertEqual(t, "explicit acknowledgment remains required", delivered.AckRequested, true)
	assertEqual(t, "submission did not acknowledge", brokerStatus(t, path).DurablePending.Total, 1)

	result := call(t, session, "receive", map[string]any{"limit": 100})
	data, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	var inbox Inbox
	requireNoError(t, json.Unmarshal(data, &inbox))
	found := false
	for _, item := range inbox.Messages {
		if item.MessageID == "renamed-inbound" && item.Type == protocol.TypeMsg {
			found = true
		}
	}
	if !found {
		t.Fatal("renamed channel lost its fallback inbox copy")
	}
	call(t, session, "acknowledge", map[string]any{"message_id": "renamed-inbound"})
	awaitListener(t, listener, func() bool {
		for _, item := range listener.queue {
			if item.message.MessageID == "renamed-inbound" && item.message.Stage == protocol.StageAgentAcknowledged {
				return true
			}
		}
		return false
	})
	assertEqual(t, "explicit acknowledgment committed", brokerStatus(t, path).DurablePending.Total, 0)
	assertEqual(t, "one inbox owner throughout", listener.Status().SessionID, owner)
}
