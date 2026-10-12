//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

type inboxPage struct {
	Messages []protocol.Message `json:"messages"`
	Next     uint64             `json:"next_after_seq"`
	Upper    uint64             `json:"upper_seq"`
	More     bool               `json:"more"`
}
type inboxReceipt struct {
	MessageID string `json:"message_id"`
	Stage     string `json:"stage"`
}
type inboxResponse struct {
	protocol.Message
	Page     *inboxPage     `json:"page"`
	Receipts []inboxReceipt `json:"receipts"`
}

func inboxCall(f *operatorFixture, request any) inboxResponse {
	f.t.Helper()
	p := operatorDial(f.t, f.operator)
	data, err := json.Marshal(request)
	operatorNoError(f.t, err)
	p.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = p.Write(append(data, '\n'))
	operatorNoError(f.t, err)
	line, err := p.r.ReadBytes('\n')
	operatorNoError(f.t, err)
	operatorEqual(f.t, "response frame within limit", len(line) <= protocol.MaxFrameBytes, true)
	var out inboxResponse
	operatorNoError(f.t, json.Unmarshal(line, &out))
	_, err = p.r.ReadByte()
	operatorEqual(f.t, "one response per connection", err, io.EOF)
	return out
}
func inboxReceive(f *operatorFixture, after, upper uint64, limit int) inboxResponse {
	return inboxCall(f, map[string]any{"type": "operator_receive", "page_request": map[string]any{"after_seq": after, "upper_seq": upper, "limit": limit}})
}
func inboxReadPage(f *operatorFixture, after, upper uint64, limit int) *inboxPage {
	f.t.Helper()
	r := inboxReceive(f, after, upper, limit)
	operatorEqual(f.t, "page response", r.Type, "operator_page")
	if r.Page == nil {
		f.t.Fatal("missing page")
	}
	return r.Page
}
func inboxReceived(f *operatorFixture, ids ...string) inboxResponse {
	return inboxCall(f, map[string]any{"type": "operator_received", "message_ids": ids})
}
func inboxAck(f *operatorFixture, id string) inboxResponse {
	return inboxCall(f, map[string]any{"type": "operator_ack", "message_id": id})
}
func inboxMessage(id string) protocol.Message {
	return protocol.Message{Type: "msg", MessageID: id, To: "operator", Durable: true, AckRequested: true, Payload: json.RawMessage(`{"text":"PRIVATE_INBOX_PAYLOAD"}`)}
}
func inboxSend(t *testing.T, p *operatorPeer, m protocol.Message) protocol.Message {
	t.Helper()
	p.write(m)
	r := p.read()
	requireOperatorAccepted(t, r, "operator")
	return r
}
func inboxStage(t *testing.T, p *operatorPeer, id, stage string) {
	t.Helper()
	r := p.read()
	operatorEqual(t, "notification type", r.Type, "ack")
	operatorEqual(t, "notification ID", r.MessageID, id)
	operatorEqual(t, "notification stage", r.Stage, stage)
}
func inboxReceiptCount(f *operatorFixture, column string) int {
	f.t.Helper()
	var n int
	operatorNoError(f.t, f.db.QueryRow("SELECT count(*) FROM durable_inbox WHERE agent_id='operator' AND "+column+" IS NOT NULL").Scan(&n))
	return n
}
func inboxExec(f *operatorFixture, query string) {
	f.t.Helper()
	_, err := f.db.Exec(query)
	operatorNoError(f.t, err)
}

func TestOperatorInboxProactiveReceiveAndExplicitAck(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	accepted := inboxSend(t, p, inboxMessage("proactive"))
	page := inboxReadPage(f, 0, 0, 0)
	operatorEqual(t, "page size", len(page.Messages), 1)
	m := page.Messages[0]
	operatorEqual(t, "sender", m.From, "author")
	operatorEqual(t, "origin", m.Origin, "agent")
	operatorEqual(t, "recipient", m.To, "operator")
	operatorEqual(t, "cursor", page.Next, accepted.Seq)
	operatorEqual(t, "more", page.More, false)
	operatorEqual(t, "receive is not receipt", inboxReceiptCount(f, "received_at"), 0)
	operatorEqual(t, "ack before receipt", inboxAck(f, "proactive").Code, "invalid_ack")
	r := inboxReceived(f, "proactive", "proactive")
	operatorEqual(t, "received response", r.Stage, "adapter_received")
	operatorEqual(t, "deduplicated receipts", len(r.Receipts), 1)
	operatorEqual(t, "receipt ID", r.Receipts[0].MessageID, "proactive")
	inboxStage(t, p, "proactive", "adapter_received")
	operatorEqual(t, "received remains pending", len(inboxReadPage(f, 0, 0, 16).Messages), 1)
	operatorEqual(t, "human ack response", inboxAck(f, "proactive").Stage, "agent_acknowledged")
	inboxStage(t, p, "proactive", "agent_acknowledged")
	operatorEqual(t, "acked no longer pending", len(inboxReadPage(f, 0, 0, 16).Messages), 0)
}

func TestOperatorInboxReplyMembership(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("member", 5)
	b := f.peer("outsider", 5)
	requireOperatorAccepted(t, f.send(operatorRequest("parent", "member")), "member")
	a.read()
	req := inboxMessage("reply")
	req.InReplyTo = "parent"
	inboxSend(t, a, req)
	b.write(req)
	operatorEqual(t, "different sender cannot reuse ID", b.read().Code, "duplicate_message")
	req.MessageID = "wrong-member"
	b.write(req)
	operatorEqual(t, "wrong member", b.read().Code, "invalid_reply")
	for _, parent := range []string{"missing", "reply"} {
		req.MessageID = "invalid-" + parent
		req.InReplyTo = parent
		a.write(req)
		operatorEqual(t, "invalid parent", a.read().Code, "invalid_reply")
	}
	operatorEqual(t, "only parent and valid reply persisted", f.count("messages"), 2)
}

func TestOperatorInboxCapabilityAndOwnership(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	old := f.peer("old", 4)
	a := f.peer("author", 5)
	old.write(inboxMessage("old-version"))
	operatorEqual(t, "v5 required", old.read().Code, "durability_unavailable")
	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		m := inboxMessage(fmt.Sprint(flags))
		m.Durable, m.AckRequested = flags[0], flags[1]
		a.write(m)
		operatorEqual(t, "invalid durability rejected", a.read().Type, "error")
	}
	inboxSend(t, a, inboxMessage("owned"))
	a.write(protocol.Message{Type: "ack", MessageID: "owned", Stage: "adapter_received", SessionID: "operator-inbox", AgentID: "operator"})
	operatorEqual(t, "agent cannot receipt operator inbox", a.read().Code, "invalid_ack")
	operatorEqual(t, "forged receipt unchanged", inboxReceiptCount(f, "received_at"), 0)
	operatorEqual(t, "unknown operation", inboxCall(f, map[string]any{"type": "operator_future"}).Code, "unsupported_operation")
}

func seedInboxMessages(t *testing.T, f *operatorFixture, n int) []uint64 {
	t.Helper()
	var p *operatorPeer
	seqs := make([]uint64, 0, n)
	for i := 0; i < n; i++ {
		if i%16 == 0 {
			p = f.peer(fmt.Sprintf("sender-%d", i/16), 5)
		}
		r := inboxSend(t, p, inboxMessage(fmt.Sprintf("item-%03d", i)))
		seqs = append(seqs, r.Seq)
	}
	return seqs
}
func TestOperatorInboxPagesFreezeUpperBound(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	seqs := seedInboxMessages(t, f, 40)
	first := inboxReadPage(f, 0, 0, 32)
	operatorEqual(t, "first page", len(first.Messages), 32)
	operatorEqual(t, "first cursor", first.Next, seqs[31])
	operatorEqual(t, "more", first.More, true)
	p := f.peer("late", 5)
	inboxSend(t, p, inboxMessage("late"))
	second := inboxReadPage(f, first.Next, first.Upper, 32)
	operatorEqual(t, "remaining original rows", len(second.Messages), 8)
	operatorEqual(t, "second cursor", second.Next, seqs[39])
	operatorEqual(t, "end of finite scan", second.More, false)
	empty := inboxReadPage(f, second.Next, first.Upper, 32)
	operatorEqual(t, "empty page", len(empty.Messages), 0)
	operatorEqual(t, "empty cursor unchanged", empty.Next, second.Next)
	operatorEqual(t, "new scan sees arrival", len(inboxReadPage(f, second.Next, 0, 32).Messages), 1)
	operatorEqual(t, "reread unacked first page", len(inboxReadPage(f, 0, first.Upper, 32).Messages), 32)
}
func TestOperatorInboxInvalidCursorsAndLimits(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	r := inboxSend(t, p, inboxMessage("item"))
	for _, q := range []struct {
		after, upper uint64
		limit        int
	}{{r.Seq + 1, r.Seq, 16}, {0, r.Seq + 1, 16}, {0, 0, -1}, {0, 0, 33}} {
		operatorEqual(t, "invalid page request", inboxReceive(f, q.after, q.upper, q.limit).Code, "malformed_frame")
	}
	operatorEqual(t, "no receipt from invalid cursor", inboxReceiptCount(f, "received_at"), 0)
}
func TestOperatorInboxPageFrameBound(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	for i := 0; i < 3; i++ {
		p := f.peer(fmt.Sprintf("large-%d", i), 5)
		m := inboxMessage(fmt.Sprintf("large-%d", i))
		m.Payload, _ = json.Marshal(strings.Repeat("x", 400<<10))
		inboxSend(t, p, m)
	}
	first := inboxReadPage(f, 0, 0, 32)
	operatorEqual(t, "byte limit cuts page", first.More, true)
	for _, m := range first.Messages {
		var s string
		operatorNoError(t, json.Unmarshal(m.Payload, &s))
		operatorEqual(t, "payload untruncated", len(s), 400<<10)
	}
	operatorEqual(t, "page has progress", first.Next > 0, true)
}
func TestOperatorInboxAuditFailureIsAtomic(t *testing.T) {
	for _, op := range []string{"operator_receive", "operator_received", "operator_ack"} {
		t.Run(op, func(t *testing.T) { checkInboxAuditFailure(t, op) })
	}
}
func checkInboxAuditFailure(t *testing.T, op string) {
	t.Helper()
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxSend(t, p, inboxMessage("audit"))
	if op == "operator_ack" {
		operatorEqual(t, "receipt setup", inboxReceived(f, "audit").Stage, "adapter_received")
		inboxStage(t, p, "audit", "adapter_received")
	}
	beforeReceived := inboxReceiptCount(f, "received_at")
	inboxExec(f, "CREATE TRIGGER reject_inbox_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
	r := inboxCall(f, inboxAuditRequest(op))
	operatorEqual(t, "audit failure", r.Code, "internal")
	operatorEqual(t, "no page on failed audit", r.Page == nil, true)
	operatorEqual(t, "received unchanged", inboxReceiptCount(f, "received_at"), beforeReceived)
	operatorEqual(t, "ack unchanged", inboxReceiptCount(f, "acknowledged_at"), 0)
}
func TestOperatorInboxReceivedBatchValidation(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxSend(t, p, inboxMessage("valid"))
	operatorEqual(t, "mixed batch rejected", inboxReceived(f, "valid", "missing").Code, "invalid_ack")
	operatorEqual(t, "batch atomic", inboxReceiptCount(f, "received_at"), 0)
	operatorEqual(t, "empty batch rejected", inboxReceived(f).Type, "error")
	ids := make([]string, 33)
	for i := range ids {
		ids[i] = "valid"
	}
	operatorEqual(t, "raw batch limit before dedupe", inboxReceived(f, ids...).Type, "error")
	requireOperatorAccepted(t, f.send(operatorRequest("other-inbox", "author")), "author")
	p.read()
	operatorEqual(t, "wrong inbox receipt", inboxReceived(f, "other-inbox").Code, "invalid_ack")
	operatorEqual(t, "wrong inbox ack", inboxAck(f, "other-inbox").Code, "invalid_ack")
}
func inboxTimestamps(f *operatorFixture, id string) string {
	f.t.Helper()
	var result string
	operatorNoError(f.t, f.db.QueryRow(`SELECT coalesce(i.received_at,'')||'|'||coalesce(i.acknowledged_at,'')||'|'||coalesce(r.adapter_received_at,'')||'|'||coalesce(r.agent_acknowledged_at,'') FROM durable_inbox i JOIN message_receipts r USING(message_id,agent_id) WHERE i.message_id=? AND i.agent_id='operator'`, id).Scan(&result))
	return result
}
func TestOperatorInboxReceiptRetriesKeepTimestamps(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	req := inboxMessage("repeat")
	inboxSend(t, p, req)
	operatorEqual(t, "received", inboxReceived(f, "repeat").Stage, "adapter_received")
	inboxStage(t, p, "repeat", "adapter_received")
	operatorEqual(t, "acked", inboxAck(f, "repeat").Stage, "agent_acknowledged")
	inboxStage(t, p, "repeat", "agent_acknowledged")
	before := inboxTimestamps(f, "repeat")
	operatorEqual(t, "ack retry", inboxAck(f, "repeat").Type, "ack")
	operatorEqual(t, "received retry", inboxReceived(f, "repeat").Type, "ack")
	operatorEqual(t, "timestamps stable", inboxTimestamps(f, "repeat"), before)
	// Any duplicated notification would precede this retry response on the same socket.
	r := inboxSend(t, p, req)
	operatorEqual(t, "retry frozen", r.Repeated, true)
	operatorEqual(t, "retained human stage", r.Receipts[0].Stage, "agent_acknowledged")
	requireOperatorAuditPrivate(t, f.db)
}
func TestOperatorInboxRestartAndOriginalSenderSession(t *testing.T) {
	database := filepath.Join(t.TempDir(), "inbox.db")
	var upper uint64
	t.Run("persist", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := f.peer("author", 5)
		inboxSend(t, p, inboxMessage("restart"))
		f.peer("welcome-gap", 5)
		upper = inboxReadPage(f, 0, 0, 16).Upper
	})
	t.Run("restart", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := f.peer("author", 5)
		page := inboxReadPage(f, 0, upper, 16)
		operatorEqual(t, "retained page", len(page.Messages), 1)
		operatorEqual(t, "retained origin", page.Messages[0].Origin, "agent")
		operatorEqual(t, "receipt", inboxReceived(f, "restart").Type, "ack")
		operatorEqual(t, "ack", inboxAck(f, "restart").Type, "ack")
		// The new session must not get notifications meant for the old sender session.
		r := inboxSend(t, p, inboxMessage("restart"))
		operatorEqual(t, "recovered stage", r.Receipts[0].Stage, "agent_acknowledged")
	})
}

// Query both receipt tables: a transaction must never update just one of them.
func TestOperatorInboxReceiptTableFailureRollsBackAudit(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxSend(t, p, inboxMessage("rollback"))
	before := f.count("operator_audit")
	inboxExec(f, "CREATE TRIGGER reject_receipt BEFORE UPDATE ON message_receipts BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END")
	operatorEqual(t, "receipt write failure", inboxReceived(f, "rollback").Code, "internal")
	operatorEqual(t, "no partial receipt", inboxReceiptCount(f, "received_at"), 0)
	// A rejected attempt may be separately audited, but no success result may survive.
	var successes int
	operatorNoError(t, f.db.QueryRow("SELECT count(*) FROM operator_audit WHERE result IN ('accepted','adapter_received')").Scan(&successes))
	operatorEqual(t, "no success audit", successes, 0)
	operatorEqual(t, "audit count did not shrink", f.count("operator_audit") >= before, true)
}

func TestOperatorInboxEmptyHistoryAndConcurrentReaders(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("welcome-only", 5)
	empty := inboxReadPage(f, 0, 0, 16)
	operatorEqual(t, "welcome not committed history", empty.Upper, uint64(0))
	operatorEqual(t, "empty history", len(empty.Messages), 0)
	operatorEqual(t, "empty more", empty.More, false)
	inboxSend(t, p, inboxMessage("shared"))
	first := inboxReadPage(f, 0, 0, 16)
	second := inboxReadPage(f, 0, first.Upper, 16)
	operatorEqual(t, "one-shot readers don't claim", second.Messages[0].MessageID, first.Messages[0].MessageID)
	operatorEqual(t, "readers don't receipt", inboxReceiptCount(f, "received_at"), 0)
}
func TestOperatorInboxPagingAcrossAcknowledgment(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxSend(t, p, inboxMessage("one"))
	second := inboxSend(t, p, inboxMessage("two"))
	third := inboxSend(t, p, inboxMessage("three"))
	page := inboxReadPage(f, 0, 0, 1)
	operatorEqual(t, "receipt second", inboxReceived(f, "two").Type, "ack")
	inboxStage(t, p, "two", "adapter_received")
	operatorEqual(t, "ack second", inboxAck(f, "two").Type, "ack")
	inboxStage(t, p, "two", "agent_acknowledged")
	next := inboxReadPage(f, page.Next, page.Upper, 1)
	operatorEqual(t, "skip acked row", next.Messages[0].Seq, third.Seq)
	operatorEqual(t, "cursor not skipped row", next.Next != second.Seq, true)
	operatorEqual(t, "end", next.More, false)
}
func TestOperatorInboxReceiveAuditNeverContainsPayload(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("author", 5)
	inboxSend(t, p, inboxMessage("private"))
	inboxReadPage(f, 0, 0, 16)
	inboxReceived(f, "private")
	inboxStage(t, p, "private", "adapter_received")
	inboxAck(f, "private")
	inboxStage(t, p, "private", "agent_acknowledged")
	var audit string
	operatorNoError(t, f.db.QueryRow("SELECT coalesce(group_concat(coalesce(detail,'')||coalesce(scope,'')), '') FROM operator_audit").Scan(&audit))
	operatorEqual(t, "audit excludes payload", strings.Contains(audit, "PRIVATE_INBOX_PAYLOAD"), false)
	operatorEqual(t, "all three operations audited", f.count("operator_audit") >= 3, true)
}

func inboxAuditRequest(op string) map[string]any {
	request := map[string]any{"type": op}
	switch op {
	case "operator_receive":
		request["page_request"] = map[string]any{"limit": 16}
	case "operator_received":
		request["message_ids"] = []string{"audit"}
	case "operator_ack":
		request["message_id"] = "audit"
	}
	return request
}

func TestOperatorInboxRepliesAndQuotaSurviveSenderRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "sender-restart.db")
	t.Run("persist", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := f.peer("member", 5)
		requireOperatorAccepted(t, f.send(operatorRequest("operator-parent", "member")), "member")
		p.read()
		for i := 0; i < 16; i++ {
			inboxSend(t, p, inboxMessage(fmt.Sprintf("retained-%d", i)))
		}
	})
	t.Run("reconnect", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := f.peer("member", 5)
		p.read() // Replay of the operator parent, still pending in the agent inbox.
		reply := inboxMessage("reply-after-restart")
		reply.InReplyTo = "operator-parent"
		p.write(reply)
		operatorEqual(t, "same sender retains quota", p.read().Code, "inbox_full")
		operatorEqual(t, "receipt frees no quota", inboxReceived(f, "retained-0").Type, "ack")
		operatorEqual(t, "human ack", inboxAck(f, "retained-0").Type, "ack")
		// Membership follows logical sender, not the old recipient session.
		inboxSend(t, p, reply)
	})
}
