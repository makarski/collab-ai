package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Check the status directly under a particular message body so a global
// status notification cannot satisfy a per-message acknowledgment assertion.
func ackDisplayRow(t *testing.T, h *chatHarness, id, want string) string {
	t.Helper()
	status := ackStatusBelowMessage(t, h, id)
	marks := 0
	for _, label := range []string{"needs ack", "ack queued", "acknowledged"} {
		marks += strings.Count(status, label)
	}
	if marks != 1 {
		t.Fatalf("message %s status = %q; want exactly one acknowledgment mark", id, status)
	}
	if !strings.Contains(status, want) {
		t.Fatalf("message %s status = %q; want %q", id, status, want)
	}
	for _, legacy := range []string{"you:", "receipt:"} {
		if strings.Contains(status, legacy) {
			t.Fatalf("message %s status = %q; unexpected legacy label %q", id, status, legacy)
		}
	}
	return status
}

func ackStatusBelowMessage(t *testing.T, h *chatHarness, id string) string {
	t.Helper()
	rows := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	for i, row := range rows {
		if strings.TrimSpace(row) != "body "+id {
			continue
		}
		if i+1 >= len(rows) {
			t.Fatalf("missing status for %s", id)
		}
		return rows[i+1]
	}
	t.Fatalf("message %s not visible: %q", id, rows)
	return ""
}

func TestChatAckDisplayTracksQueuedAndInFlightUntilConfirmation(t *testing.T) {
	for _, receiptFirst := range []bool{false, true} {
		name := "ack-before-receipt"
		if receiptFirst {
			name = "ack-after-receipt"
		}
		t.Run(name, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
			inbox, history := h.baseline()
			h.reply(history, chatHistory())
			h.reply(inbox, chatInbox(chatIncoming("target", 1), chatIncoming("untouched", 2)))
			receipt := h.take(protocol.TypeOperatorReceived)
			ackDisplayRow(t, h, "target", "needs ack")
			if receiptFirst {
				h.reply(receipt, chatReceipt("target", "untouched"))
				h.await(func() bool { return h.m.State().InFlight == 0 })
				ackDisplayRow(t, h, "target", "needs ack")
			}
			v2Command(h, "/ack #1")
			ackDisplayRow(t, h, "target", "ack queued")
			ackDisplayRow(t, h, "untouched", "needs ack")
			if !receiptFirst {
				h.reply(receipt, chatReceipt("target", "untouched"))
			}
			ack := h.take(protocol.TypeOperatorAck)
			chatEqual(t, "ack bound to selected message", ack.MessageID, "target")
			ackDisplayRow(t, h, "target", "ack queued")
			h.reply(ack, protocol.Message{Type: protocol.TypeAck, MessageID: "target", Stage: protocol.StageAgentAcknowledged})
			h.await(func() bool { return h.m.State().Entries[0].OperatorStage == protocol.StageAgentAcknowledged })
			ackDisplayRow(t, h, "target", "acknowledged")
			ackDisplayRow(t, h, "untouched", "needs ack")
		})
	}
}

func TestChatAckDisplayFailedAckReturnsToNeeded(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("target", 1)))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt("target"))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack #1")
	ack := h.take(protocol.TypeOperatorAck)
	h.reply(ack, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "ack unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	ackDisplayRow(t, h, "target", "needs ack")
	chatEqual(t, "failed ack is not confirmation", h.m.State().Entries[0].OperatorStage, protocol.StageAdapterReceived)
}

func TestChatAckDisplayHistoryAndCancelledQueue(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	page := chatHistory(chatIncoming("done", 2), chatIncoming("target", 1))
	page.History.Entries[0].OperatorStage = protocol.StageAgentAcknowledged
	h.reply(history, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	ackDisplayRow(t, h, "done", "acknowledged")
	ackDisplayRow(t, h, "target", "needs ack")
	v2Command(h, "/ack #1")
	ackDisplayRow(t, h, "target", "ack queued")
	v2Command(h, "/ack #1")
	ackDisplayRow(t, h, "target", "needs ack")
	ackDisplayRow(t, h, "done", "acknowledged")
}

func TestChatAckDisplayRetainsConflictAndSuspendedReason(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 180, Height: 24})
	inbox, history := h.baseline()
	msg := chatIncoming("target", 1)
	h.reply(history, chatHistory(msg))
	h.await(func() bool { return len(h.m.State().Entries) == 1 })
	v2Command(h, "/ack #1")
	msg.Payload = []byte(`{"text":"changed body"}`)
	h.reply(inbox, chatInbox(msg))
	h.await(func() bool { return h.m.State().Entries[0].Conflict })
	row := ackDisplayRow(t, h, "target", "ack queued")
	for _, want := range []string{"CONFLICT", "suspended", "content changed"} {
		if !strings.Contains(row, want) {
			t.Fatalf("missing %q from message status %q", want, row)
		}
	}
}

func TestChatAckDisplayRetainsReceiptFailure(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 24})
	inbox, history := h.baseline()
	h.reply(history, chatHistory())
	h.reply(inbox, chatInbox(chatIncoming("target", 1)))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "receipt unavailable"})
	h.await(func() bool { return h.m.State().Entries[0].Receipt == chat.ReceiptFailed })
	row := ackDisplayRow(t, h, "target", "needs ack")
	if !strings.Contains(row, "receipt failed") {
		t.Fatalf("receipt failure hidden: %q", row)
	}
}
