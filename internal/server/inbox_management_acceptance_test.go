//go:build linux

package server

import (
	"encoding/json"
	"os"
	"testing"
)

type managementUsage struct {
	AgentID        string `json:"agent_id"`
	Unacknowledged int    `json:"unacknowledged"`
	Unreceived     int    `json:"unreceived"`
	Bytes          int    `json:"bytes"`
}

type managementStatus struct {
	HeadSeq        uint64            `json:"head_seq"`
	Oldest         uint64            `json:"oldest_unacknowledged_seq"`
	Unacknowledged int               `json:"unacknowledged"`
	Unreceived     int               `json:"unreceived"`
	Bytes          int               `json:"bytes"`
	Senders        []managementUsage `json:"senders"`
	Limits         struct {
		SenderMessages int `json:"sender_messages"`
		SenderBytes    int `json:"sender_bytes"`
		InboxMessages  int `json:"inbox_messages"`
		InboxBytes     int `json:"inbox_bytes"`
	} `json:"limits"`
	Retained struct {
		Messages      int `json:"messages"`
		Bytes         int `json:"bytes"`
		LimitMessages int `json:"limit_messages"`
		LimitBytes    int `json:"limit_bytes"`
	} `json:"retained"`
}

func managementCall(f *operatorFixture, kind string, fields map[string]any) sessionWire {
	f.t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = kind
	p := &sessionProbe{operatorPeer: operatorDial(f.t, f.operator)}
	defer p.Close()
	p.send(fields)
	return p.frame()
}

func managementReadStatus(f *operatorFixture) managementStatus {
	f.t.Helper()
	r := managementCall(f, "operator_inbox_status", nil)
	operatorEqual(f.t, "status response", r.text("type"), "operator_inbox_status")
	var status managementStatus
	operatorNoError(f.t, json.Unmarshal(r["inbox"], &status))
	return status
}

func managementSender(t *testing.T, status managementStatus, id string) managementUsage {
	t.Helper()
	for _, sender := range status.Senders {
		if sender.AgentID == id {
			return sender
		}
	}
	t.Fatalf("sender %q missing from usage: %+v", id, status.Senders)
	return managementUsage{}
}

func TestInboxManagementStatusSeparatesDeliveryFromAcknowledgment(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b := f.peer("author", 5), f.peer("other", 5)
	first := inboxSend(t, a, inboxMessage("first"))
	inboxSend(t, a, inboxMessage("second"))
	last := inboxSend(t, b, inboxMessage("third"))
	operatorEqual(t, "receipt setup", inboxReceived(f, "first").Stage, "adapter_received")
	inboxStage(t, a, "first", "adapter_received")
	status := managementReadStatus(f)
	operatorEqual(t, "head is latest inbox sequence", status.HeadSeq, last.Seq)
	operatorEqual(t, "oldest pending is inclusive", status.Oldest, first.Seq)
	operatorEqual(t, "all three still need acknowledgment", status.Unacknowledged, 3)
	operatorEqual(t, "receipt distinguishes delivery backlog", status.Unreceived, 2)
	usage := managementSender(t, status, "author")
	operatorEqual(t, "sender pending", usage.Unacknowledged, 2)
	operatorEqual(t, "sender delivery backlog", usage.Unreceived, 1)
	operatorEqual(t, "sender limit", status.Limits.SenderMessages, 16)
	operatorEqual(t, "sender byte limit", status.Limits.SenderBytes, 512<<10)
	operatorEqual(t, "inbox limit", status.Limits.InboxMessages, 256)
	operatorEqual(t, "inbox byte limit", status.Limits.InboxBytes, 8<<20)
	operatorEqual(t, "retained count", status.Retained.Messages, 3)
	operatorEqual(t, "retained count limit", status.Retained.LimitMessages, 100000)
	operatorEqual(t, "retained byte limit", status.Retained.LimitBytes, 256<<20)
	var charged int
	operatorNoError(t, f.db.QueryRow("SELECT SUM(wire_bytes) FROM durable_messages").Scan(&charged))
	operatorEqual(t, "status reports actual quota byte charge", status.Bytes, charged)
	operatorEqual(t, "status does not receipt unseen entries", inboxReceiptCount(f, "received_at"), 1)
	operatorEqual(t, "status never acknowledges", inboxReceiptCount(f, "acknowledged_at"), 0)
	operatorEqual(t, "explicit ack", inboxAck(f, "first").Stage, "agent_acknowledged")
	inboxStage(t, a, "first", "agent_acknowledged")
	after := managementReadStatus(f)
	operatorEqual(t, "ack frees pending count", after.Unacknowledged, 2)
	operatorEqual(t, "ack preserves retained history count", after.Retained.Messages, 3)
	if after.Bytes >= status.Bytes {
		t.Fatal("ack did not free pending bytes")
	}
}

func TestInboxManagementStatusAuditFailureReturnsNoUsage(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxSend(t, p, inboxMessage("kept"))
	inboxExec(f, `CREATE TRIGGER fail_management_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	r := managementCall(f, "operator_inbox_status", nil)
	operatorEqual(t, "status fails closed without audit", r.text("code"), "internal")
	operatorEqual(t, "no unaudited usage returned", len(r["inbox"]), 0)
	operatorEqual(t, "audit failure does not receipt", inboxReceiptCount(f, "received_at"), 0)
}
