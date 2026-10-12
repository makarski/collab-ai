//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

func TestOperatorInboxSenderCountQuotaAndRetry(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	for i := 0; i < 16; i++ {
		inboxSend(t, p, inboxMessage(fmt.Sprintf("count-%d", i)))
	}
	retry := inboxSend(t, p, inboxMessage("count-0"))
	operatorEqual(t, "retry at capacity", retry.Repeated, true)
	p.write(inboxMessage("overflow"))
	operatorEqual(t, "sender count cap", p.read().Code, "inbox_full")
	operatorEqual(t, "no partial overflow", f.count("messages"), 16)
	operatorEqual(t, "receipt", inboxReceived(f, "count-0").Type, "ack")
	inboxStage(t, p, "count-0", "adapter_received")
	p.write(inboxMessage("still-full"))
	operatorEqual(t, "received doesn't release quota", p.read().Code, "inbox_full")
	operatorEqual(t, "human ack", inboxAck(f, "count-0").Type, "ack")
	inboxStage(t, p, "count-0", "agent_acknowledged")
	inboxSend(t, p, inboxMessage("after-ack"))
}
func TestOperatorInboxSenderBytesQuota(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	first := inboxMessage("bytes-first")
	first.Payload, _ = json.Marshal(strings.Repeat("x", 300<<10))
	inboxSend(t, p, first)
	second := first
	second.MessageID = "bytes-second"
	p.write(second)
	operatorEqual(t, "sender byte cap", p.read().Code, "inbox_full")
	large := inboxMessage("oversized")
	large.Payload, _ = json.Marshal(strings.Repeat("x", 512<<10))
	p.write(large)
	operatorEqual(t, "single wire exceeds cap", p.read().Code, "inbox_full")
	operatorEqual(t, "failed byte quota atomic", f.count("messages"), 1)
}
func TestOperatorInboxAggregateCountQuota(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	seedInboxMessages(t, f, 256)
	p := f.peer("fresh", 5)
	p.write(inboxMessage("aggregate-overflow"))
	operatorEqual(t, "aggregate count cap", p.read().Code, "inbox_full")
	operatorEqual(t, "aggregate history atomic", f.count("messages"), 256)
	operatorEqual(t, "aggregate inbox atomic", f.count("durable_inbox"), 256)
}
func TestOperatorInboxAggregateBytesQuota(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	// Twenty independent 400 KiB payloads fit under 8 MiB, while the next cannot.
	for i := 0; i < 20; i++ {
		p := f.peer(fmt.Sprintf("bytes-%d", i), 5)
		m := inboxMessage(fmt.Sprintf("bytes-%d", i))
		m.Payload, _ = json.Marshal(strings.Repeat("x", 400<<10))
		inboxSend(t, p, m)
	}
	p := f.peer("next", 5)
	m := inboxMessage("bytes-overflow")
	m.Payload, _ = json.Marshal(strings.Repeat("x", 400<<10))
	p.write(m)
	operatorEqual(t, "aggregate byte cap", p.read().Code, "inbox_full")
	operatorEqual(t, "aggregate byte rollback", f.count("messages"), 20)
}
func TestOperatorInboxGlobalCapacityStillApplies(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxExec(f, `INSERT INTO durable_messages(message_id,wire_bytes,storage_bytes) VALUES('global-fixture',1,1);
 WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<4096)
 INSERT INTO durable_inbox(message_id,agent_id,session_id) SELECT 'global-fixture','unrelated-'||x,'' FROM n;`)
	p.write(inboxMessage("global-overflow"))
	operatorEqual(t, "global pending cap", p.read().Code, "inbox_full")
	operatorEqual(t, "no partial history", f.count("messages"), 0)
	operatorEqual(t, "global count unchanged", f.count("durable_inbox"), store.MaxGlobalPending)
}
func TestOperatorInboxStatusHasNoPseudoPresence(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxSend(t, p, inboxMessage("status"))
	c := operatorDial(t, f.agent)
	c.write(protocol.Message{Type: "status"})
	r := c.read()
	data, err := json.Marshal(r.Status)
	operatorNoError(t, err)
	var status struct {
		OperatorPending *int `json:"operator_pending"`
	}
	operatorNoError(t, json.Unmarshal(data, &status))
	if status.OperatorPending == nil {
		t.Fatal("operator pending count unavailable")
	}
	operatorEqual(t, "operator pending", *status.OperatorPending, 1)
	if r.Status.ConnectedSessions == nil {
		t.Fatal("sessions unavailable")
	}
	operatorEqual(t, "real session only", *r.Status.ConnectedSessions, 1)
	operatorEqual(t, "total includes operator once", r.Status.DurablePending.Total, 1)
}
