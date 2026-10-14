package chat_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
)

func TestChatOfflineSubmitKeepsDraftUntilConnected(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.draft("author", "keep until connected")
	h.wire.Close()
	h.await(func() bool { return h.m.State().Connection == chat.Reconnecting })
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "offline draft preserved", h.m.State().Draft, "keep until connected")
	chatEqual(t, "no unsent frozen intent", h.m.State().Intent, nil)
}

func TestChatCapacityPauseResumesAtFirstUnadoptedRow(t *testing.T) {
	b := chat.DefaultBounds()
	b.Entries = 1
	h := newChatHarness(t, b)
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("first", 1), chatIncoming("second", 2)))
	receipt := h.take(protocol.TypeOperatorReceived)
	chatEqual(t, "capacity pause", h.m.State().Paused, true)
	h.reply(receipt, chatReceipt("first"))
	resume := h.take(protocol.TypeOperatorReceive)
	chatEqual(t, "resume after adopted row", resume.PageRequest.AfterSeq, uint64(1))
	chatEqual(t, "resume frozen upper", resume.PageRequest.UpperSeq, uint64(2))
	h.reply(resume, chatInbox(chatIncoming("second", 2)))
	second := h.take(protocol.TypeOperatorReceived)
	chatEqual(t, "receipt for resumed row count", len(second.MessageIDs), 1)
	chatEqual(t, "receipt for resumed row", second.MessageIDs[0], "second")
}
