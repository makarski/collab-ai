//go:build linux

package server

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestInboxManagementRefusalIsVisibleWithoutAcceptingOrLeakingPayload(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("author", 5)
	for i := range 16 {
		inboxSend(t, a, inboxMessage(fmt.Sprintf("full-%02d", i)))
	}
	owner := sessionOpen(f)
	mark := owner.snapshot().Mark
	a.write(inboxMessage("refused"))
	refusal := a.read()
	operatorEqual(t, "sender sees refusal", refusal.Code, "inbox_full")
	for _, want := range []string{"sender messages", "16/16"} {
		if !strings.Contains(refusal.Detail, want) {
			t.Fatalf("refusal lacks bound %q: %q", want, refusal.Detail)
		}
	}
	refusals := 0
	for _, frame := range owner.barrier() {
		event := sessionCheckEvent(t, frame, &mark)
		if event.Kind != "send_refused" {
			continue
		}
		refusals++
		operatorEqual(t, "operator sees blocked sender", event.AgentID, "author")
		detail := sessionDecode[struct {
			Detail string `json:"detail"`
		}](t, frame, "event").Detail
		if !strings.Contains(detail, "sender messages") || !strings.Contains(detail, "16/16") {
			t.Fatalf("operator refusal lacks quota explanation: %q", detail)
		}
		if strings.Contains(sessionJSON(frame), "PRIVATE_INBOX_PAYLOAD") {
			t.Fatal("refusal event leaked unaccepted payload")
		}
	}
	operatorEqual(t, "operator receives one refusal event", refusals, 1)
	var auditDetail, auditScope string
	operatorNoError(t, f.db.QueryRow("SELECT detail, scope FROM operator_audit WHERE action='send_refused' AND message_id='refused' AND result='rejected'").Scan(&auditDetail, &auditScope))
	if strings.Contains(auditDetail, "PRIVATE_INBOX_PAYLOAD") || !strings.Contains(auditDetail, "16/16") {
		t.Fatalf("refusal audit must record bound without payload: %q", auditDetail)
	}
	if !strings.Contains(auditScope, "author") {
		t.Fatalf("refusal audit missing sender: %q", auditScope)
	}
	operatorEqual(t, "refused message never enters history", f.count("messages"), 16)
	operatorEqual(t, "refused message never enters durable inbox", f.count("durable_inbox"), 16)
	operatorEqual(t, "refusal never receipts existing work", inboxReceiptCount(f, "received_at"), 0)
	operatorEqual(t, "refusal never acknowledges existing work", inboxReceiptCount(f, "acknowledged_at"), 0)
}

func TestInboxBulkAckRecoversSenderCapacityWithoutDeletingHistory(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("author", 5)
	var ids []string
	var upper uint64
	for i := range 16 {
		id := fmt.Sprintf("recover-%02d", i)
		ids = append(ids, id)
		upper = inboxSend(t, a, inboxMessage(id)).Seq
	}
	operatorEqual(t, "receipt setup", inboxReceived(f, ids...).Stage, "adapter_received")
	for _, id := range ids {
		inboxStage(t, a, id, "adapter_received")
	}
	a.write(inboxMessage("not-yet-free"))
	operatorEqual(t, "receipt alone does not free capacity", a.read().Code, "inbox_full")
	result, _ := managementAck(f, managementSelection("author", 0, upper, 256))
	operatorEqual(t, "all received rows acknowledged", result.Acknowledged, 16)
	for _, id := range ids {
		inboxStage(t, a, id, "agent_acknowledged")
	}
	inboxSend(t, a, inboxMessage("capacity-recovered"))
	status := managementReadStatus(f)
	operatorEqual(t, "only new message is pending", status.Unacknowledged, 1)
	operatorEqual(t, "ack does not delete durable history", status.Retained.Messages, 17)
	page := inboxReadPage(f, 0, 0, 32)
	operatorEqual(t, "only unacknowledged message replays", len(page.Messages), 1)
	operatorEqual(t, "new message remains replayable", page.Messages[0].MessageID, "capacity-recovered")
}
