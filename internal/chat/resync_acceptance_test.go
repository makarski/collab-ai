package chat_test

import (
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func (h *chatHarness) event(kind, epoch string, revision uint64) {
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: kind, Watermark: protocol.Watermark{Epoch: epoch, Revision: revision}}}
}

func TestChatResyncRetainsBodiesAndReloadsPendingInbox(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory(chatIncoming("retained", 1)))
	h.reply(inbox, chatInbox())
	h.await(func() bool { return len(h.m.State().Entries) == 1 && h.m.State().InFlight == 0 })
	h.event(protocol.EventInboxChanged, "epoch", 3)
	snap := h.take(protocol.TypeOperatorSnapshot)
	if len(h.m.State().Entries) != 1 {
		t.Fatal("resync erased rendered bodies")
	}
	h.reply(snap, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 3}, InboxPending: 1, InboxHeadSeq: 2}})
	nextInbox := h.take(protocol.TypeOperatorReceive)
	if nextInbox.PageRequest.AfterSeq != 0 || nextInbox.PageRequest.UpperSeq != 0 {
		t.Fatalf("resync reused old inbox cursor: %+v", nextInbox.PageRequest)
	}
	h.take(protocol.TypeOperatorHistory)
}

func TestChatEpochChangeSuspendsQueuedAcknowledgment(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("queued", 1)))
	receipt := h.take(protocol.TypeOperatorReceived)
	v2Command(h, "/ack #1")
	h.event(protocol.EventResyncRequired, "new-epoch", 1)
	h.take(protocol.TypeOperatorSnapshot)
	state := h.m.State()
	if len(state.QueuedAcks) != 1 || state.QueuedAcks[0].Suspended == "" {
		t.Fatalf("epoch did not suspend intent: %+v", state.QueuedAcks)
	}
	h.reply(receipt, chatReceipt("queued"))
	h.await(func() bool { return h.m.State().Entries[0].Receipt == chat.ReceiptConfirmed })
	if len(h.m.State().QueuedAcks) != 1 {
		t.Fatal("late receipt dispatched suspended ack")
	}
}

func TestChatConflictingBodyPreservesRenderedContentAndSuspendsAck(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	msg := chatIncoming("same-id", 1)
	h.reply(history, chatHistory(msg))
	h.await(func() bool { return len(h.m.State().Entries) == 1 })
	v2Command(h, "/ack #1")
	msg.Payload = json.RawMessage(`{"text":"replacement body"}`)
	h.reply(inbox, chatInbox(msg))
	h.await(func() bool { return h.m.State().Entries[0].Conflict })
	state := h.m.State()
	if state.Entries[0].Body != "body same-id" {
		t.Fatal("content silently rewritten")
	}
	if len(state.QueuedAcks) != 1 || state.QueuedAcks[0].Suspended == "" {
		t.Fatal("conflicting content did not suspend queued ack immediately")
	}
}

func chatSentHistory(id string, seq uint64, revision uint64, stage protocol.RecipientStage) protocol.Message {
	msg := chatIncoming(id, seq)
	msg.From = protocol.OperatorID
	msg.To = "author"
	msg.Origin = protocol.OriginOperator
	return protocol.Message{Type: protocol.TypeOperatorHistory, History: &protocol.HistoryPage{Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}, UpperSeq: seq, NextBeforeSeq: seq, Entries: []protocol.HistoryEntry{{Message: msg, Direction: "sent", Recipients: []protocol.RecipientStage{stage}}}}}
}

func TestChatLateHistoryCannotUndoNewerRecipientReclaim(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatSentHistory("sent", 1, 1, protocol.RecipientStage{AgentID: "author", Stage: protocol.StageAdapterReceived, SessionID: "old-session"}))
	h.await(func() bool { return len(h.m.State().Entries) == 1 && h.m.State().InFlight == 0 })
	h.event(protocol.EventConversationChanged, "epoch", 2)
	catchup := h.take(protocol.TypeOperatorHistory)
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventStageChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 3}, MessageID: "sent", AgentID: "author", SessionID: "new-session", Stage: protocol.StageAccepted}}
	h.await(func() bool { return h.m.State().Revision == 3 })
	h.reply(catchup, chatSentHistory("sent", 1, 2, protocol.RecipientStage{AgentID: "author", Stage: protocol.StageAdapterReceived, SessionID: "old-session"}))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	stage := h.m.State().Entries[0].Stages[0]
	if stage.SessionID != "new-session" || stage.Stage != protocol.StageAccepted {
		t.Fatalf("late history undid reclaim: %+v", stage)
	}
}

func TestChatCatchupCapLeavesPageableGap(t *testing.T) {
	b := chat.DefaultBounds()
	b.CatchupPages = 1
	h := newChatHarness(t, b)
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory(chatIncoming("known", 1)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.event(protocol.EventConversationChanged, "epoch", 2)
	catchup := h.take(protocol.TypeOperatorHistory)
	page := chatHistory(chatIncoming("newest", 100))
	page.History.More = true
	page.History.Watermark.Revision = 2
	h.reply(catchup, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	state := h.m.State()
	if len(state.Entries) != 2 || !state.Entries[1].GapBelow {
		t.Fatalf("catchup limit hid gap: %+v", state.Entries)
	}
	v2Command(h, "/history")
	older := h.take(protocol.TypeOperatorHistory)
	if older.PageRequest.BeforeSeq != 100 || older.PageRequest.UpperSeq < 100 {
		t.Fatalf("gap paging used old scroll bounds: %+v", older.PageRequest)
	}
}

func TestChatArrivalDoesNotMoveScrolledView(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 80, Height: 8})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory(chatIncoming("second", 2), chatIncoming("first", 1)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyPgUp, 0))
	before := h.m.State()
	if before.ScrollOffset == 0 {
		t.Fatalf("view did not scroll up: %+v", before)
	}
	h.event(protocol.EventConversationChanged, "epoch", 2)
	req := h.take(protocol.TypeOperatorHistory)
	page := chatHistory(chatIncoming("third", 3), chatIncoming("second", 2))
	page.History.Watermark.Revision = 2
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	after := h.m.State()
	if after.ScrollOffset <= before.ScrollOffset || after.Unread != before.Unread+1 {
		t.Fatalf("arrival moved the view or lost unread count: before %d after %+v", before.ScrollOffset, after)
	}
}

func TestChatLatePageFromPreviousEpochCannotOverwriteNewBaseline(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatSentHistory("sent", 1, 1, protocol.RecipientStage{AgentID: "author", Stage: protocol.StageAdapterReceived, SessionID: "old-session"}))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.event(protocol.EventConversationChanged, "epoch", 2)
	oldRequest := h.take(protocol.TypeOperatorHistory)
	h.event(protocol.EventResyncRequired, "new-epoch", 1)
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark:  protocol.Watermark{Epoch: "new-epoch", Revision: 1},
		RecentSent: []protocol.SentSummary{{MessageID: "sent", Seq: 1, Recipients: []protocol.RecipientStage{{AgentID: "author", SessionID: "new-session", Stage: protocol.StageAccepted}}}},
	}})
	h.await(func() bool { return h.m.State().Baseline && h.m.State().Epoch == "new-epoch" })
	h.reply(oldRequest, chatSentHistory("sent", 1, 999, protocol.RecipientStage{AgentID: "author", Stage: protocol.StageAgentAcknowledged, SessionID: "old-session"}))
	// Completing the stale scan must schedule the fresh catch-up requested by resync.
	h.take(protocol.TypeOperatorHistory)
	stage := h.m.State().Entries[0].Stages[0]
	if stage.SessionID != "new-session" || stage.Stage != protocol.StageAccepted {
		t.Fatalf("old epoch page overwrote baseline: %+v", stage)
	}
}
