package chat_test

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func ackPickerHarness(t *testing.T, confirmed bool) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 24})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("first", 1), chatIncoming("second", 2), chatIncoming("third", 3)))
	receipt := h.take(protocol.TypeOperatorReceived)
	if confirmed {
		h.reply(receipt, chatReceipt("first", "second", "third"))
		h.await(func() bool { return h.m.State().InFlight == 0 })
	}
	return h
}

func ackPickerItems(t *testing.T, h *chatHarness, want ...string) {
	t.Helper()
	p := h.m.State().Picker
	if p == nil || p.Kind != "ack" {
		t.Fatalf("ack picker missing: %+v", p)
	}
	if !reflect.DeepEqual(p.Items, want) {
		t.Fatalf("ack picker choices = %v, want %v", p.Items, want)
	}
}

func TestChatAckPickerOpeningAndEscapePreserveDraftAndReceipts(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Type(h, "unsent draft")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "third", "second", "first")
	chatEqual(t, "newest initially selected", h.m.State().Picker.Selected, 0)
	chatEqual(t, "opening picker sends no acknowledgment", h.m.State().InFlight, 0)
	chatEqual(t, "opening picker queues no acknowledgment", len(h.m.State().QueuedAcks), 0)
	h.input(chatKey(tea.KeyTab, 0))
	ackPickerItems(t, h, "third", "second", "first")
	chatEqual(t, "Tab cannot acknowledge", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyEscape, 0))
	chatEqual(t, "Escape closes picker", h.m.State().Picker, nil)
	chatEqual(t, "Escape preserves draft", h.m.State().Draft, "unsent draft")
}

func TestChatAckPickerEnterAcknowledgesOnlySelectedMessage(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Type(h, "kept draft")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "third", "second", "first")
	h.input(chatKey(tea.KeyDown, 0))
	chatEqual(t, "down selects older message", h.m.State().Picker.Selected, 1)
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorAck)
	chatEqual(t, "selected message acknowledged", req.MessageID, "second")
	h.reply(req, protocol.Message{Type: protocol.TypeAck, MessageID: "second", Stage: protocol.StageAgentAcknowledged})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "picker closes on selection", h.m.State().Picker, nil)
	chatEqual(t, "selection preserves draft", h.m.State().Draft, "kept draft")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "third", "first")
}

func TestChatAckPickerExcludesAcknowledgedAndInFlightMessages(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Command(h, "/ack #1")
	first := h.take(protocol.TypeOperatorAck)
	h.reply(first, protocol.Message{Type: protocol.TypeAck, MessageID: "first", Stage: protocol.StageAgentAcknowledged})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack #2")
	h.take(protocol.TypeOperatorAck) // The second acknowledgment stays in flight.
	v2Command(h, "/ack")
	ackPickerItems(t, h, "third")
	chatEqual(t, "picker does not duplicate pending acknowledgment", h.m.State().InFlight, 1)
}

func TestChatAckPickerKeepsReceiptBeforeAckAndExcludesQueuedRows(t *testing.T) {
	h := ackPickerHarness(t, false)
	v2Command(h, "/ack")
	ackPickerItems(t, h, "third", "second", "first")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "only receipt request in flight", h.m.State().InFlight, 1)
	chatEqual(t, "selected acknowledgment queued", len(h.m.State().QueuedAcks), 1)
	chatEqual(t, "queued selected row", h.m.State().QueuedAcks[0].MessageID, "third")
	v2Command(h, "/ack")
	ackPickerItems(t, h, "second", "first")
}

func TestChatAckPickerEmptyInboxMakesNoRequest(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	v2Command(h, "/ack")
	chatEqual(t, "empty picker is not opened", h.m.State().Picker, nil)
	v3Contains(t, h.m.State().Status, "nothing to acknowledge")
	chatEqual(t, "empty picker makes no requests", h.m.State().InFlight, 0)
}
