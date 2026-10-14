package chat_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
)

func TestChatReceiptBatchesRespectRequestBudget(t *testing.T) {
	b := chat.DefaultBounds()
	b.ReceiptBatch = 1
	h := newChatHarness(t, b)
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	var messages []protocol.Message
	for i := 1; i <= 32; i++ {
		messages = append(messages, chatIncoming(fmt.Sprint(i), uint64(i)))
	}
	h.reply(inbox, chatInbox(messages...))
	seen := map[string]bool{}
	for range messages {
		req := h.take(protocol.TypeOperatorReceived)
		if len(req.MessageIDs) != 1 || seen[req.MessageIDs[0]] {
			t.Fatalf("invalid receipt batch: %v", req.MessageIDs)
		}
		seen[req.MessageIDs[0]] = true
		if h.m.State().InFlight > 4 {
			t.Fatalf("request budget exceeded: %+v", h.m.State())
		}
		h.reply(req, chatReceipt(req.MessageIDs...))
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func TestChatOmittedBodyPromotionCannotBypassMemoryBound(t *testing.T) {
	b := chat.DefaultBounds()
	b.Bytes = 1500
	h := newChatHarness(t, b)
	inbox, history := h.baseline()
	msg := chatIncoming("omitted", 1)
	page := chatHistory(msg)
	page.History.Entries[0].Message.Payload = nil
	page.History.Entries[0].BodyOmitted = true
	h.reply(history, page)
	h.await(func() bool { return len(h.m.State().Entries) == 1 })
	msg.Payload = json.RawMessage(`{"text":"` + strings.Repeat("x", 2000) + `"}`)
	h.reply(inbox, chatInbox(msg))
	h.await(func() bool { return h.m.State().Paused || !h.m.State().Entries[0].BodyOmitted })
	state := h.m.State()
	chatEqual(t, "capacity pause", state.Paused, true)
	chatEqual(t, "body stays omitted", state.Entries[0].BodyOmitted, true)
	chatEqual(t, "no premature receipt", state.Entries[0].Receipt, chat.ReceiptNone)
}

func TestChatDuplicateHistoryDoesNotEvictExistingBody(t *testing.T) {
	b := chat.DefaultBounds()
	b.Entries = 1
	h := newChatHarness(t, b)
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory(chatIncoming("known", 1)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.event(protocol.EventConversationChanged, "epoch", 2)
	req := h.take(protocol.TypeOperatorHistory)
	changed := chatIncoming("known", 1)
	changed.Payload = json.RawMessage(`{"text":"different"}`)
	page := chatHistory(changed)
	page.History.Watermark.Revision = 2
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	state := h.m.State()
	chatEqual(t, "retained count", len(state.Entries), 1)
	chatEqual(t, "original body", state.Entries[0].Body, "body known")
	chatEqual(t, "content conflict", state.Entries[0].Conflict, true)
}

func TestChatRepeatedStageFetchIsCoalesced(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatSentHistory("sent", 1, 1, protocol.RecipientStage{AgentID: "author", Stage: protocol.StageAccepted, SessionID: "session"}))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyDown, 0))
	for range 100 {
		h.input(chatKey('s', 0))
	}
	state := h.m.State()
	if state.InFlight != 1 || state.Queued != 0 {
		t.Fatalf("identical fetches accumulated: in flight=%d queued=%d", state.InFlight, state.Queued)
	}
}
