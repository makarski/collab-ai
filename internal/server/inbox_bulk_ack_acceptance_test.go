//go:build linux

package server

import (
	"encoding/json"
	"os"
	"testing"
)

type managementAckResult struct {
	Acknowledged int    `json:"acknowledged"`
	Already      int    `json:"already_acknowledged"`
	Skipped      int    `json:"skipped"`
	Next         uint64 `json:"next_after_seq"`
	More         bool   `json:"more"`
}

func managementSelection(from string, after, upper uint64, limit int) map[string]any {
	return map[string]any{"ack_selection": map[string]any{"from": from, "after_seq": after, "upper_seq": upper, "limit": limit}}
}

func managementAck(f *operatorFixture, fields map[string]any) (managementAckResult, []inboxReceipt) {
	f.t.Helper()
	r := managementCall(f, "operator_ack_many", fields)
	operatorEqual(f.t, "bulk ack response", r.text("type"), "ack")
	operatorEqual(f.t, "bulk ack stage", r.text("stage"), "agent_acknowledged")
	operatorEqual(f.t, "bulk ack recipient", r.text("agent_id"), "operator")
	var result managementAckResult
	var receipts []inboxReceipt
	operatorNoError(f.t, json.Unmarshal(r["ack_many"], &result))
	if len(r["receipts"]) > 0 {
		operatorNoError(f.t, json.Unmarshal(r["receipts"], &receipts))
	}
	return result, receipts
}

func managementAcknowledged(f *operatorFixture, id string) bool {
	f.t.Helper()
	var acked bool
	operatorNoError(f.t, f.db.QueryRow("SELECT acknowledged_at IS NOT NULL FROM durable_inbox WHERE message_id=? AND agent_id='operator'", id).Scan(&acked))
	return acked
}

func TestInboxBulkAckFreezesScopeSkipsUnreceivedAndRetriesSamePage(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b := f.peer("author", 5), f.peer("other", 5)
	inboxSend(t, a, inboxMessage("first"))
	second := inboxSend(t, a, inboxMessage("unreceived"))
	inboxSend(t, b, inboxMessage("other-sender"))
	third := inboxSend(t, a, inboxMessage("third"))
	inboxSend(t, a, inboxMessage("late"))
	operatorEqual(t, "receipt setup", inboxReceived(f, "first", "third", "other-sender", "late").Stage, "adapter_received")
	page := managementSelection("author", 0, third.Seq, 2)
	r, receipts := managementAck(f, page)
	operatorEqual(t, "one ready row acknowledged", r.Acknowledged, 1)
	operatorEqual(t, "one unreceived row skipped", r.Skipped, 1)
	operatorEqual(t, "cursor passes last examined, including skipped", r.Next, second.Seq)
	operatorEqual(t, "later selected row remains", r.More, true)
	operatorEqual(t, "only new transition reported", len(receipts), 1)
	operatorEqual(t, "first transition ID", receipts[0].MessageID, "first")
	operatorEqual(t, "skipped row remains pending", managementAcknowledged(f, "unreceived"), false)
	operatorEqual(t, "from filter excludes other sender", managementAcknowledged(f, "other-sender"), false)
	operatorEqual(t, "upper bound excludes late arrival", managementAcknowledged(f, "late"), false)
	retry, receipts := managementAck(f, page)
	operatorEqual(t, "retry makes no new transition", retry.Acknowledged, 0)
	operatorEqual(t, "retry reports earlier committed acknowledgment", retry.Already, 1)
	operatorEqual(t, "retry keeps skipped count", retry.Skipped, 1)
	operatorEqual(t, "retry keeps page cursor", retry.Next, r.Next)
	operatorEqual(t, "retry emits no new transition receipt", len(receipts), 0)
	operatorEqual(t, "retry cannot consume next page", managementAcknowledged(f, "third"), false)
	next, receipts := managementAck(f, managementSelection("author", r.Next, third.Seq, 2))
	operatorEqual(t, "continuation acknowledges remaining row", next.Acknowledged, 1)
	operatorEqual(t, "continuation ends frozen scan", next.More, false)
	operatorEqual(t, "continuation cursor", next.Next, third.Seq)
	operatorEqual(t, "continuation transition", receipts[0].MessageID, "third")
	operatorEqual(t, "bulk never fabricates receipts", inboxReceiptCount(f, "received_at"), 4)
}

func TestInboxBulkAckAuditFailureRollsBackWholeBatch(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxSend(t, p, inboxMessage("first"))
	last := inboxSend(t, p, inboxMessage("second"))
	operatorEqual(t, "receipt setup", inboxReceived(f, "first", "second").Stage, "adapter_received")
	inboxExec(f, `CREATE TRIGGER fail_second_bulk_audit BEFORE INSERT ON operator_audit WHEN NEW.action='ack' AND NEW.message_id='second' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	r := managementCall(f, "operator_ack_many", managementSelection("author", 0, last.Seq, 256))
	operatorEqual(t, "audit failure reported", r.text("code"), "internal")
	operatorEqual(t, "durable ack transaction rolled back", inboxReceiptCount(f, "acknowledged_at"), 0)
	var historyAcks, bulkAudits int
	operatorNoError(t, f.db.QueryRow("SELECT COUNT(*) FROM message_receipts WHERE agent_acknowledged_at IS NOT NULL").Scan(&historyAcks))
	operatorNoError(t, f.db.QueryRow("SELECT COUNT(*) FROM operator_audit WHERE action='ack' AND detail='bulk'").Scan(&bulkAudits))
	operatorEqual(t, "history receipts rolled back with durable rows", historyAcks, 0)
	operatorEqual(t, "earlier per-message audit also rolled back", bulkAudits, 0)
	inboxExec(f, `DROP TRIGGER fail_second_bulk_audit`)
	result, _ := managementAck(f, managementSelection("author", 0, last.Seq, 256))
	operatorEqual(t, "retry after failed transaction handles full batch", result.Acknowledged, 2)
}

func TestInboxBulkAckRequiresCurrentOperatorOwner(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	last := inboxSend(t, p, inboxMessage("owned"))
	operatorEqual(t, "receipt setup", inboxReceived(f, "owned").Stage, "adapter_received")
	owner := sessionOpen(f)
	selection := managementSelection("author", 0, last.Seq, 256)
	r := managementCall(f, "operator_ack_many", selection)
	operatorEqual(t, "one-shot cannot bypass live owner", r.text("code"), "owner_busy")
	operatorEqual(t, "rejected writer preserves inbox", managementAcknowledged(f, "owned"), false)
	status := managementReadStatus(f)
	operatorEqual(t, "read remains available while owned", status.Unacknowledged, 1)
	r, _ = owner.request("operator_ack_many", selection)
	operatorEqual(t, "owner can acknowledge in session", r.text("stage"), "agent_acknowledged")
	operatorEqual(t, "owner mutation committed", managementAcknowledged(f, "owned"), true)
}

func TestInboxBulkAckRejectsInvalidBoundsWithoutMutation(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	last := inboxSend(t, p, inboxMessage("kept"))
	operatorEqual(t, "receipt setup", inboxReceived(f, "kept").Stage, "adapter_received")
	for _, tc := range []struct {
		name         string
		after, upper uint64
		limit        int
	}{
		{"missing upper", 0, 0, 16},
		{"after exceeds upper", last.Seq + 1, last.Seq, 16},
		{"upper beyond committed history", 0, last.Seq + 1, 16},
		{"negative limit", 0, last.Seq, -1},
		{"overlarge limit", 0, last.Seq, 257},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := managementCall(f, "operator_ack_many", managementSelection("author", tc.after, tc.upper, tc.limit))
			operatorEqual(t, "malformed selection", r.text("code"), "malformed_frame")
			operatorEqual(t, "invalid selection never acknowledges", managementAcknowledged(f, "kept"), false)
		})
	}
}
