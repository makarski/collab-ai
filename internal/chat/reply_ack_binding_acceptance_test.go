package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func replyAckSuspended(t *testing.T, h *chatHarness, reason string) {
	t.Helper()
	queued := h.m.State().QueuedAcks
	chatEqual(t, "one suspended acknowledgment", len(queued), 1)
	chatEqual(t, "suspended target", queued[0].MessageID, "target")
	if !strings.Contains(queued[0].Suspended, reason) {
		t.Fatalf("suspension reason = %q, want %q", queued[0].Suspended, reason)
	}
	row := ackDisplayRow(t, h, "target", "ack queued")
	v3Contains(t, row, "suspended")
}

func TestChatReplyAckConflictAfterSendSuspendsInsteadOfAcknowledging(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	sent := replyAckSend(h)
	h.event(protocol.EventConversationChanged, "epoch", 2)
	history := h.take(protocol.TypeOperatorHistory)
	changed := chatIncoming("target", 1)
	changed.Payload = json.RawMessage(`{"text":"replacement body"}`)
	page := chatHistory(changed)
	page.History.Watermark.Revision = 2
	h.reply(history, page)
	h.await(func() bool { return h.m.State().Entries[0].Conflict })
	replyAckAccept(h, sent)
	replyAckSuspended(t, h, "content changed")
	replyAckFinishHistory(h)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "original body retained", h.m.State().Entries[0].Body, "body target")
}

func TestChatReplyAckEpochChangeSuspendsLateAcceptance(t *testing.T) {
	h, _ := replyAckHarness(t, true)
	sent := replyAckSend(h)
	h.event(protocol.EventResyncRequired, "next-epoch", 1)
	h.take(protocol.TypeOperatorSnapshot)
	replyAckAccept(h, sent)
	replyAckSuspended(t, h, "broker restarted")
	chatEqual(t, "old target is not acknowledged", h.m.State().Entries[0].OperatorStage == protocol.StageAgentAcknowledged, false)
}

func TestChatReplyAckPreservesExistingSuspendedExplicitQueue(t *testing.T) {
	h, _ := replyAckHarness(t, false)
	v2Command(h, "/ack #1")
	sent := replyAckSend(h)
	h.event(protocol.EventResyncRequired, "next-epoch", 1)
	h.take(protocol.TypeOperatorSnapshot)
	replyAckSuspended(t, h, "broker restarted")
	replyAckAccept(h, sent)
	replyAckSuspended(t, h, "broker restarted")
}

func TestChatReplyAckEvictedTargetReportsIncompleteAcknowledgment(t *testing.T) {
	bounds := chat.DefaultBounds()
	bounds.Entries = 1
	h := newChatHarness(t, bounds)
	v2History(h, chatIncoming("target", 1))
	sent := replyAckSend(h)
	h.event(protocol.EventConversationChanged, "epoch", 2)
	history := h.take(protocol.TypeOperatorHistory)
	page := chatHistory(chatIncoming("newer", 2))
	page.History.Watermark.Revision = 2
	h.reply(history, page)
	h.await(func() bool { return h.m.State().Entries[0].MessageID == "newer" })
	replyAckAccept(h, sent)
	chatEqual(t, "missing target has no manufactured queue", len(h.m.State().QueuedAcks), 0)
	v3Contains(t, h.m.State().Status, "automatic acknowledgment of #1 could not be completed")
	replyAckFinishHistory(h)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}
