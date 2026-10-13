//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"collab-ai/internal/protocol"
)

type historyEntry struct {
	Message       protocol.Message   `json:"message"`
	Direction     string             `json:"direction"`
	Omitted       bool               `json:"body_omitted"`
	SenderSession string             `json:"sender_session"`
	Recipients    []sessionRecipient `json:"recipients"`
	Truncated     bool               `json:"receipts_truncated"`
	OperatorStage string             `json:"operator_stage"`
}
type historyPage struct {
	Mark    sessionMark    `json:"watermark"`
	Upper   uint64         `json:"upper_seq"`
	Next    uint64         `json:"next_before_seq"`
	More    bool           `json:"more"`
	Entries []historyEntry `json:"entries"`
}

func historyQuery(before, upper uint64, limit int) map[string]any {
	return map[string]any{"page_request": map[string]any{"before_seq": before, "upper_seq": upper, "limit": limit}}
}

func historyCall(f *operatorFixture, request map[string]any) sessionWire {
	f.t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(f.t, f.operator)}
	p.send(request)
	r := p.frame()
	p.closed()
	return r
}

func historyRead(f *operatorFixture, before, upper uint64, limit int) historyPage {
	f.t.Helper()
	req := historyQuery(before, upper, limit)
	req["type"] = "operator_history"
	r := historyCall(f, req)
	operatorEqual(f.t, "history response", r.text("type"), "operator_history")
	return sessionDecode[historyPage](f.t, r, "history")
}

func historySend(f *operatorFixture, p *operatorPeer, id string) protocol.Message {
	f.t.Helper()
	requireOperatorAccepted(f.t, f.send(operatorRequest(id, "alpha")), "alpha")
	return p.read()
}

func TestOperatorHistoryProvenanceScopeAndAcknowledgedEntries(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	b := f.peer("beta", 5)
	sent := historySend(f, a, "sent")
	inboxSend(t, a, inboxMessage("received"))
	operatorEqual(t, "receive receipt", inboxReceived(f, "received").Stage, "adapter_received")
	inboxStage(t, a, "received", "adapter_received")
	operatorEqual(t, "human receipt", inboxAck(f, "received").Stage, "agent_acknowledged")
	inboxStage(t, a, "received", "agent_acknowledged")
	a.write(protocol.Message{Type: "msg", MessageID: "private-between-agents", To: "beta", Payload: json.RawMessage(`"private"`), Durable: true, AckRequested: true})
	operatorEqual(t, "ordinary message accepted", a.read().Stage, "accepted")
	b.read()
	// A legacy sender named operator is not trusted operator provenance.
	inboxExec(f, `INSERT INTO messages(seq,ts,sender,recipient,payload) VALUES(900,CURRENT_TIMESTAMP,'operator','alpha','"legacy"');
INSERT INTO message_metadata(message_id,seq,sender_session,in_reply_to,ack_requested,origin) VALUES('legacy-operator',900,'legacy','',1,NULL);`)
	page := historyRead(f, 0, 0, 32)
	operatorEqual(t, "only operator conversation", len(page.Entries), 2)
	operatorEqual(t, "global upper includes unrelated rows", page.Upper, uint64(900))
	operatorEqual(t, "acked entry retained", page.Entries[0].Message.MessageID, "received")
	operatorEqual(t, "incoming stage", page.Entries[0].OperatorStage, "agent_acknowledged")
	operatorEqual(t, "incoming direction", page.Entries[0].Direction, "received")
	operatorEqual(t, "sender provenance present", page.Entries[0].SenderSession != "", true)
	operatorEqual(t, "sent entry", page.Entries[1].Message.MessageID, sent.MessageID)
	operatorEqual(t, "trusted sent origin", page.Entries[1].Message.Origin, "operator")
	operatorEqual(t, "sent direction", page.Entries[1].Direction, "sent")
	operatorEqual(t, "body preserved", string(page.Entries[1].Message.Payload), string(sent.Payload))
	operatorEqual(t, "history complete", page.More, false)
	operatorEqual(t, "no internal placeholder", strings.Contains(sessionJSON(page), "operator-inbox"), false)
}

func TestOperatorHistoryFixedUpperPagingAndLiveReceipts(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	for i := 0; i < 20; i++ {
		historySend(f, a, fmt.Sprintf("history-%02d", i))
	}
	first := historyRead(f, 0, 0, 0)
	operatorEqual(t, "default page count", len(first.Entries), 16)
	operatorEqual(t, "newest first", first.Entries[0].Message.MessageID, "history-19")
	operatorEqual(t, "first page more", first.More, true)
	newer := historySend(f, a, "arrived-after-cut")
	last := historyRead(f, first.Next, first.Upper, 32)
	operatorEqual(t, "older page count", len(last.Entries), 4)
	operatorEqual(t, "stable upper", last.Upper, first.Upper)
	operatorEqual(t, "oldest entry", last.Entries[3].Message.MessageID, "history-00")
	operatorEqual(t, "older page complete", last.More, false)
	empty := historyRead(f, last.Next, first.Upper, 32)
	operatorEqual(t, "empty page", len(empty.Entries), 0)
	operatorEqual(t, "empty cursor unchanged", empty.Next, last.Next)
	operatorEqual(t, "empty complete", empty.More, false)
	operatorEqual(t, "new scan includes arrival", historyRead(f, 0, 0, 1).Entries[0].Message.Seq, newer.Seq)
	// A fixed upper is membership only; receipt state stays current.
	a.write(protocol.Message{Type: "ack", MessageID: "history-19", Stage: "adapter_received"})
	sessionAgentBarrier(t, a)
	page := historyRead(f, 0, first.Upper, 1)
	operatorEqual(t, "live stage", page.Entries[0].Recipients[0].Stage, "adapter_received")
}

func TestOperatorHistoryEmptyAndInvalidCursors(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	f.peer("welcome-only", 5)
	page := historyRead(f, 0, 0, 0)
	operatorEqual(t, "welcome isn't committed upper", page.Upper, uint64(0))
	operatorEqual(t, "empty entries", len(page.Entries), 0)
	operatorEqual(t, "empty more", page.More, false)
	for _, fields := range []map[string]any{
		{"before_seq": 1}, {"upper_seq": 1}, {"limit": -1}, {"limit": 33},
		{"before_seq": -1}, {"upper_seq": -1}, {"before_seq": json.Number("18446744073709551616")},
	} {
		r := historyCall(f, map[string]any{"type": "operator_history", "page_request": fields})
		operatorEqual(t, "invalid history bounds", r.text("code"), "malformed_frame")
	}
	inboxExec(f, `INSERT INTO messages(seq,ts,sender,recipient,payload) VALUES(100,CURRENT_TIMESTAMP,'alpha','beta','{}')`)
	page = historyRead(f, 99, 0, 16)
	operatorEqual(t, "unrelated history captures upper", page.Upper, uint64(100))
	operatorEqual(t, "gap cursor allowed", page.Next, uint64(99))
	operatorEqual(t, "no matching messages", len(page.Entries), 0)
}

func TestOperatorHistorySessionReadDoesNotConsumeOrResetBaseline(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	inboxSend(t, a, inboxMessage("still-pending"))
	p, opened := sessionConnect(f)
	info := sessionDecode[struct {
		Capabilities []string `json:"capabilities"`
	}](t, opened, "session")
	operatorEqual(t, "history capability", strings.Contains(sessionJSON(info.Capabilities), `"history"`), true)
	mark := p.snapshot().Mark
	before := inboxTimestamps(f, "still-pending")
	page := historyRead(f, 0, 0, 16)
	operatorEqual(t, "one-shot allowed with owner", len(page.Entries), 1)
	r, events := p.request("operator_history", historyQuery(0, 0, 16))
	operatorEqual(t, "session history response", r.text("type"), "operator_history")
	operatorEqual(t, "history emits no events", len(events), 0)
	operatorEqual(t, "same watermark", sessionDecode[historyPage](t, r, "history").Mark, mark)
	operatorEqual(t, "receipts unchanged", inboxTimestamps(f, "still-pending"), before)
	operatorEqual(t, "owner still exclusive", inboxReceived(f, "still-pending").Code, "owner_busy")
	f.peer("new-agent", 5)
	operatorEqual(t, "subscription still active", p.event(&mark).AgentID, "new-agent")
}

func TestOperatorHistoryAuditFailureReturnsNoBody(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	inboxSend(t, a, inboxMessage("audited"))
	p := sessionOpen(f)
	mark := p.snapshot().Mark
	before := f.count("operator_audit")
	historyRead(f, 0, 0, 16)
	operatorEqual(t, "one audit per read", f.count("operator_audit"), before+1)
	inboxExec(f, `CREATE TRIGGER fail_history_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	r, _ := p.request("operator_history", historyQuery(0, 0, 16))
	operatorEqual(t, "audit failure", r.text("code"), "internal")
	operatorEqual(t, "no returned history", len(r["history"]), 0)
	operatorEqual(t, "failure leaves watermark", p.snapshot().Mark, mark)
	operatorEqual(t, "no implicit receipt", inboxReceiptCount(f, "received_at"), 0)
	requireOperatorAuditPrivate(t, f.db)
}

func TestOperatorHistoryCursorSurvivesRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "history.db")
	var first historyPage
	t.Run("persist", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		a := f.peer("alpha", 5)
		historySend(f, a, "oldest")
		historySend(f, a, "newest")
		first = historyRead(f, 0, 0, 1)
	})
	t.Run("restart", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		page := historyRead(f, first.Next, first.Upper, 1)
		operatorEqual(t, "retained cursor", page.Entries[0].Message.MessageID, "oldest")
		operatorEqual(t, "new epoch", page.Mark.Epoch != first.Mark.Epoch, true)
		operatorEqual(t, "retained upper", page.Upper, first.Upper)
	})
}
