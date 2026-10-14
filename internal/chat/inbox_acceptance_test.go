package chat_test

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
)

func chatReceipt(ids ...string) protocol.Message {
	r := protocol.Message{Type: protocol.TypeAck, Stage: protocol.StageAdapterReceived}
	for _, id := range ids {
		r.Receipts = append(r.Receipts, protocol.ReceiptState{MessageID: id, AgentID: protocol.OperatorID, Stage: protocol.StageAdapterReceived})
	}
	return r
}

func TestChatAdmissionPrecedesReceiptAndExplicitAckKeepsItsIdentity(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	commands := h.holdResponse(inbox, chatInbox(chatIncoming("first", 1), chatIncoming("second", 2)))
	admitted := h.m.State()
	if len(admitted.Entries) != 2 || admitted.Entries[0].Body != "body first" || admitted.Entries[1].Body != "body second" {
		t.Fatalf("complete bodies not adopted before receipt command: %+v", admitted.Entries)
	}
	h.launch(commands)
	receipt := h.take(protocol.TypeOperatorReceived)
	if !reflect.DeepEqual(receipt.MessageIDs, []string{"first", "second"}) {
		t.Fatalf("receipt IDs=%v", receipt.MessageIDs)
	}
	state := h.m.State()
	if len(state.Entries) != 2 {
		t.Fatalf("receipt sent before complete bodies adopted: %+v", state)
	}
	for _, e := range state.Entries {
		if e.Receipt != chat.ReceiptPending {
			t.Fatalf("receipt prematurely confirmed: %+v", e)
		}
	}
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey('a', 0))
	if len(h.m.State().QueuedAcks) != 1 {
		t.Fatal("early explicit acknowledgment not queued")
	}
	h.input(chatKey(tea.KeyUp, 0))
	h.reply(receipt, chatReceipt("first", "second"))
	ack := h.take(protocol.TypeOperatorAck)
	if ack.MessageID != "second" {
		t.Fatalf("selection retargeted queued ack: %+v", ack)
	}
}

func TestChatHistoryIsNonConsumingButReceiveDedupStillConfirms(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	msg := chatIncoming("known", 1)
	h.reply(history, chatHistory(msg))
	h.await(func() bool { return len(h.m.State().Entries) == 1 })
	if h.m.State().Entries[0].Receipt != chat.ReceiptNone {
		t.Fatal("history confirmed receipt")
	}
	h.reply(inbox, chatInbox(msg))
	receipt := h.take(protocol.TypeOperatorReceived)
	if !reflect.DeepEqual(receipt.MessageIDs, []string{"known"}) {
		t.Fatalf("dedup skipped receipt: %+v", receipt)
	}
	if len(h.m.State().Entries) != 1 {
		t.Fatal("history/inbox duplicated timeline entry")
	}
}

func TestChatRejectsEntireMalformedInboxPageBeforeAdoption(t *testing.T) {
	for _, name := range []string{"missing body", "wrong recipient", "wrong type", "descending sequence", "duplicate ID", "cursor skips row"} {
		t.Run(name, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			inbox, history := h.baseline()
			h.reply(history, chatHistory())
			page := chatInbox(chatIncoming("first", 1), chatIncoming("second", 2))
			switch name {
			case "missing body":
				page.Page.Messages[1].Payload = nil
			case "wrong recipient":
				page.Page.Messages[1].To = "someone-else"
			case "wrong type":
				page.Page.Messages[1].Type = protocol.TypeAck
			case "descending sequence":
				page.Page.Messages[1].Seq = 1
			case "duplicate ID":
				page.Page.Messages[1].MessageID = "first"
			case "cursor skips row":
				page.Page.NextAfterSeq = 3
			}
			h.reply(inbox, page)
			h.await(func() bool { return h.m.State().Status != "" })
			if len(h.m.State().Entries) != 0 {
				t.Fatalf("partially adopted invalid page: %+v", h.m.State())
			}
		})
	}
}

func TestChatDrainFreezesUpperAndSchedulesArrivalAfterCurrentScan(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	pending := 3
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventInboxChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}, HeadSeq: 3, Pending: &pending}}
	first := chatInbox(chatIncoming("first", 1))
	first.Page.UpperSeq = 2
	first.Page.More = true
	h.reply(inbox, first)
	next := h.take(protocol.TypeOperatorReceive)
	if next.PageRequest.AfterSeq != 1 || next.PageRequest.UpperSeq != 2 {
		t.Fatalf("scan cursor moved: %+v", next.PageRequest)
	}
	second := chatInbox(chatIncoming("second", 2))
	h.reply(next, second)
	fresh := h.take(protocol.TypeOperatorReceive)
	if fresh.PageRequest.AfterSeq != 0 || fresh.PageRequest.UpperSeq != 0 {
		t.Fatalf("new arrivals not scanned from fresh bound: %+v", fresh.PageRequest)
	}
}

func TestChatCapacityPauseDoesNotReceiptOrSkipUnadoptedRows(t *testing.T) {
	bounds := chat.DefaultBounds()
	bounds.Entries = 1
	h := newChatHarness(t, bounds)
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("pinned", 1), chatIncoming("not-adopted", 2)))
	receipt := h.take(protocol.TypeOperatorReceived)
	if !reflect.DeepEqual(receipt.MessageIDs, []string{"pinned"}) {
		t.Fatalf("receipt for unadopted content: %v", receipt.MessageIDs)
	}
	state := h.m.State()
	if !state.Paused || len(state.Entries) != 1 {
		t.Fatalf("capacity bound broken: %+v", state)
	}
}

func TestChatFailedReceiptRetriesSameIDsWithoutHumanAck(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("retry-receipt", 1)))
	first := h.take(protocol.TypeOperatorReceived)
	h.reply(first, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "audit unavailable"})
	h.await(func() bool { return h.m.State().Entries[0].Receipt == chat.ReceiptFailed })
	second := h.take(protocol.TypeOperatorReceived)
	if !reflect.DeepEqual(first.MessageIDs, second.MessageIDs) {
		t.Fatalf("receipt retry changed IDs: %v -> %v", first.MessageIDs, second.MessageIDs)
	}
	h.reply(second, chatReceipt(second.MessageIDs...))
	h.await(func() bool { return h.m.State().Entries[0].Receipt == chat.ReceiptConfirmed })
	if h.m.State().Entries[0].OperatorStage != protocol.StageAdapterReceived || len(h.m.State().QueuedAcks) != 0 {
		t.Fatal("receipt retry became human acknowledgment")
	}
}
