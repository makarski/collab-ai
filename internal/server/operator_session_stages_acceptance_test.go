//go:build linux

package server

import (
	"fmt"
	"os"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

type sessionStagePage struct {
	Mark       sessionMark        `json:"watermark"`
	MessageID  string             `json:"message_id"`
	Recipients []sessionRecipient `json:"recipients"`
	Next       string             `json:"next_after_agent"`
	More       bool               `json:"more"`
}

func (p *sessionProbe) stages(id, after string, limit int) sessionStagePage {
	p.t.Helper()
	r, _ := p.request("operator_message_stages", map[string]any{"message_id": id, "after_agent": after, "limit": limit})
	operatorEqual(p.t, "stages response", r.text("type"), "operator_message_stages")
	page := sessionDecode[sessionStagePage](p.t, r, "stages")
	operatorEqual(p.t, "stages message", page.MessageID, id)
	return page
}

func TestOperatorSessionStagePagingAndValidation(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	for _, id := range []string{"charlie", "alpha", "bravo"} {
		f.peer(id, 5)
	}
	requireOperatorAccepted(t, f.send(operatorRequest("paged-stages", "*")), "alpha", "bravo", "charlie")
	p := sessionOpen(f)
	mark := p.snapshot().Mark
	before := f.count("operator_audit")
	first := p.stages("paged-stages", "", 2)
	operatorEqual(t, "first page size", len(first.Recipients), 2)
	operatorEqual(t, "first recipient sorted", first.Recipients[0].AgentID, "alpha")
	operatorEqual(t, "exclusive cursor", first.Next, "bravo")
	operatorEqual(t, "first more", first.More, true)
	operatorEqual(t, "live page watermark", first.Mark, mark)
	last := p.stages("paged-stages", first.Next, 2)
	operatorEqual(t, "last size", len(last.Recipients), 1)
	operatorEqual(t, "last recipient", last.Recipients[0].AgentID, "charlie")
	operatorEqual(t, "last more", last.More, false)
	empty := p.stages("paged-stages", last.Next, 2)
	operatorEqual(t, "empty size", len(empty.Recipients), 0)
	operatorEqual(t, "empty cursor preserved", empty.Next, last.Next)
	operatorEqual(t, "empty more", empty.More, false)
	operatorEqual(t, "metadata reads not audited", f.count("operator_audit"), before)
	for _, fields := range []map[string]any{
		{"message_id": "paged-stages", "limit": -1},
		{"message_id": "paged-stages", "limit": 257},
		{"message_id": "paged-stages", "after_agent": "not-a-recipient"},
	} {
		r, _ := p.request("operator_message_stages", fields)
		operatorEqual(t, "invalid stage cursor/limit", r.text("code"), "malformed_frame")
	}
	operatorEqual(t, "valid default after errors", len(p.stages("paged-stages", "", 0).Recipients), 3)
}

func TestOperatorSessionStagesRejectAgentMessagesAndUnknownIDs(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	inboxSend(t, a, inboxMessage("agent-origin"))
	p := sessionOpen(f)
	for _, id := range []string{"agent-origin", "missing"} {
		r, _ := p.request("operator_message_stages", map[string]any{"message_id": id})
		operatorEqual(t, "not operator-origin", r.text("code"), "invalid_message")
	}
	operatorEqual(t, "metadata doesn't receipt inbox", inboxReceiptCount(f, "received_at"), 0)
}

func TestOperatorSessionStagesReclaimResetsCurrentSessionReceipt(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	requireOperatorAccepted(t, f.send(operatorRequest("reclaimed", "alpha")), "alpha")
	a.read()
	p := sessionOpen(f)
	mark := p.snapshot().Mark
	initial := p.stages("reclaimed", "", 1).Recipients[0]
	a.write(protocol.Message{Type: "ack", MessageID: "reclaimed", Stage: "adapter_received"})
	sessionAgentBarrier(t, a)
	e := p.event(&mark)
	operatorEqual(t, "received event", e.Kind, "stage_changed")
	operatorEqual(t, "received stage", e.Stage, "adapter_received")
	operatorEqual(t, "original delivery owner", e.SessionID, initial.SessionID)
	operatorEqual(t, "live received page", p.stages("reclaimed", "", 1).Recipients[0].Stage, "adapter_received")
	a.write(protocol.Message{Type: "ack", MessageID: "reclaimed", Stage: "adapter_received"})
	sessionAgentBarrier(t, a)
	operatorEqual(t, "duplicate receipt no event", len(p.barrier()), 0)
	a.Close()
	e = p.event(&mark)
	operatorEqual(t, "disconnect observed", e.State, "disconnected")
	b := f.peer("alpha", 5)
	operatorEqual(t, "pending replay", b.read().MessageID, "reclaimed")
	changes := []sessionEvent{p.event(&mark), p.event(&mark)}
	var reclaimed sessionEvent
	for _, e := range changes {
		if e.Kind == "stage_changed" {
			reclaimed = e
		}
	}
	operatorEqual(t, "reclaim event message", reclaimed.MessageID, "reclaimed")
	operatorEqual(t, "reclaim resets stage", reclaimed.Stage, "accepted")
	operatorEqual(t, "new delivery session", reclaimed.SessionID != initial.SessionID, true)
	page := p.stages("reclaimed", "", 1)
	operatorEqual(t, "page sees reclaim", page.Recipients[0].Stage, "accepted")
	operatorEqual(t, "page sees new owner", page.Recipients[0].SessionID, reclaimed.SessionID)
	operatorEqual(t, "page at latest revision", page.Mark, mark)
	b.write(protocol.Message{Type: "ack", MessageID: "reclaimed", Stage: "adapter_received"})
	sessionAgentBarrier(t, b)
	p.event(&mark)
	b.write(protocol.Message{Type: "ack", MessageID: "reclaimed", Stage: "agent_acknowledged"})
	operatorEqual(t, "agent acknowledgment confirmed", b.read().Stage, "agent_acknowledged")
	e = p.event(&mark)
	operatorEqual(t, "terminal stage event", e.Stage, "agent_acknowledged")
	operatorEqual(t, "terminal page", p.stages("reclaimed", "", 1).Recipients[0].Stage, "agent_acknowledged")
}

func TestOperatorSessionSnapshotBoundsAndStageDefault(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	var first *operatorPeer
	for i := 0; i < 101; i++ {
		peer := f.peer(fmt.Sprintf("agent-%03d", i), 5)
		if i == 0 {
			first = peer
		}
	}
	for i := 0; i < 17; i++ {
		requireOperatorAccepted(t, f.send(operatorRequest(fmt.Sprintf("recent-%02d", i), "agent-000")), "agent-000")
		first.read()
	}
	p := sessionOpen(f)
	snap := p.snapshot()
	operatorEqual(t, "agent total", snap.AgentsTotal, 101)
	operatorEqual(t, "agent cap", len(snap.Agents), 100)
	operatorEqual(t, "agent truncation explicit", snap.AgentsTruncated, true)
	operatorEqual(t, "first sorted agent", snap.Agents[0].AgentID, "agent-000")
	operatorEqual(t, "last sorted agent", snap.Agents[99].AgentID, "agent-099")
	operatorEqual(t, "recent cap", len(snap.Sent), 16)
	operatorEqual(t, "recent truncation explicit", snap.SentTruncated, true)
	operatorEqual(t, "newest first", snap.Sent[0].MessageID, "recent-16")
	operatorEqual(t, "oldest retained", snap.Sent[15].MessageID, "recent-01")
	// A separate group exercises the default64 receipt page independently of
	// the snapshot's bounded recent-message view.
	req := operatorRequest("many-recipients", "*")
	accepted := f.send(req)
	operatorEqual(t, "fan-out accepted", accepted.Stage, "accepted")
	operatorEqual(t, "fan-out size", len(accepted.Recipients), 101)
	page := p.stages(req.MessageID, "", 0)
	operatorEqual(t, "default stage page", len(page.Recipients), 64)
	operatorEqual(t, "default has continuation", page.More, true)
	last := p.stages(req.MessageID, page.Next, 256)
	operatorEqual(t, "remaining recipients", len(last.Recipients), 37)
	operatorEqual(t, "remaining complete", last.More, false)
}
