package chat_test

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func bulkUsage(count int) protocol.InboxStatus {
	return protocol.InboxStatus{
		HeadSeq: 3, OldestUnacknowledgedSeq: 1, Unacknowledged: count,
		Limits:  protocol.InboxLimits{SenderMessages: 16, SenderBytes: 512 << 10, InboxMessages: 256, InboxBytes: 8 << 20},
		Senders: []protocol.SenderUsage{{AgentID: "author", Unacknowledged: count}},
	}
}

func beginBulkAck(h *chatHarness, command string) protocol.Message {
	h.t.Helper()
	v2Command(h, command)
	status := h.take(protocol.TypeOperatorInboxStatus)
	usage := bulkUsage(3)
	h.reply(status, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	return h.take(protocol.TypeOperatorAckMany)
}

func bulkAckResponse(next uint64, more bool, ids ...string) protocol.Message {
	resp := protocol.Message{Type: protocol.TypeAck, Stage: protocol.StageAgentAcknowledged, AgentID: protocol.OperatorID,
		AckMany: &protocol.AckManyResult{Acknowledged: len(ids), NextAfterSeq: next, More: more},
	}
	for _, id := range ids {
		resp.Receipts = append(resp.Receipts, protocol.ReceiptState{MessageID: id, AgentID: protocol.OperatorID, Stage: protocol.StageAgentAcknowledged})
	}
	return resp
}

func TestChatBulkAckUsesFrozenBoundsAndUpdatesOnlyConfirmedRows(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Type(h, "draft survives")
	first := beginBulkAck(h, "/ack from author")
	if first.AckSelection == nil {
		t.Fatal("missing bulk selection")
	}
	chatEqual(t, "sender scope", first.AckSelection.From, "author")
	chatEqual(t, "oldest pending included", first.AckSelection.AfterSeq, uint64(0))
	chatEqual(t, "upper captured from status", first.AckSelection.UpperSeq, uint64(3))
	chatEqual(t, "bounded page size", first.AckSelection.Limit, 64)
	h.reply(first, bulkAckResponse(1, true, "first"))
	second := h.take(protocol.TypeOperatorAckMany)
	chatEqual(t, "continuation keeps sender", second.AckSelection.From, "author")
	chatEqual(t, "continuation keeps upper", second.AckSelection.UpperSeq, uint64(3))
	chatEqual(t, "continuation uses returned cursor", second.AckSelection.AfterSeq, uint64(1))
	last := bulkAckResponse(3, false, "third")
	last.AckMany.Skipped = 1
	h.reply(second, last)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "acknowledged 2")
	v3Contains(t, h.m.State().Status, "1 not yet received")
	chatEqual(t, "draft untouched", h.m.State().Draft, "draft survives")
	for _, entry := range h.m.State().Entries {
		if entry.MessageID == "second" {
			chatEqual(t, "unconfirmed row remains unacknowledged", entry.OperatorStage == protocol.StageAgentAcknowledged, false)
		} else {
			chatEqual(t, "confirmed row updated", entry.OperatorStage, protocol.StageAgentAcknowledged)
		}
	}
}

func TestChatBulkAckEmptyStatusDoesNotSendMutation(t *testing.T) {
	h := ackPickerHarness(t, true)
	v2Command(h, "/ack all")
	req := h.take(protocol.TypeOperatorInboxStatus)
	usage := bulkUsage(0)
	usage.HeadSeq, usage.OldestUnacknowledgedSeq = 0, 0
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &usage})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Status, "nothing to acknowledge")
}

func TestChatBulkAckRejectsMalformedResultsBeforeApplyingReceipts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*protocol.Message)
	}{
		{"wrong stage", func(r *protocol.Message) { r.Stage = protocol.StageAdapterReceived }},
		{"wrong recipient", func(r *protocol.Message) { r.AgentID = "another" }},
		{"no cursor progress", func(r *protocol.Message) { r.AckMany.NextAfterSeq = 0 }},
		{"cursor beyond frozen upper", func(r *protocol.Message) { r.AckMany.NextAfterSeq = 4 }},
		{"negative count", func(r *protocol.Message) { r.AckMany.Acknowledged = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := ackPickerHarness(t, true)
			req := beginBulkAck(h, "/ack all")
			response := bulkAckResponse(1, true, "first")
			tc.alter(&response)
			h.reply(req, response)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			for _, entry := range h.m.State().Entries {
				chatEqual(t, "malformed response must not mark rows done", entry.OperatorStage == protocol.StageAgentAcknowledged, false)
			}
			if h.m.State().Status == "" {
				t.Fatal("malformed acknowledgment needs visible feedback")
			}
		})
	}
}

func TestChatBulkAckDisconnectAllowsExplicitRetry(t *testing.T) {
	h := ackPickerHarness(t, true)
	beginBulkAck(h, "/ack all")
	wire := newChatScript()
	h.reconnectTo(wire, "epoch")
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/ack all")
	retry := h.take(protocol.TypeOperatorInboxStatus)
	chatEqual(t, "a disconnected bulk run does not wedge future commands", retry.Type, protocol.TypeOperatorInboxStatus)
}
