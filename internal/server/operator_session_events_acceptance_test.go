//go:build linux

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type sessionMark struct {
	Epoch    string `json:"epoch"`
	Revision uint64 `json:"revision"`
}
type sessionPresence struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Version   int    `json:"protocol_version"`
}
type sessionRecipient struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Stage     string `json:"stage"`
}
type sessionSent struct {
	MessageID  string             `json:"message_id"`
	Seq        uint64             `json:"seq"`
	Recipients []sessionRecipient `json:"recipients"`
	Truncated  bool               `json:"receipts_truncated"`
}
type sessionSnapshot struct {
	Mark            sessionMark       `json:"watermark"`
	Agents          []sessionPresence `json:"agents"`
	AgentsTotal     int               `json:"agents_total"`
	AgentsTruncated bool              `json:"agents_truncated"`
	Head            uint64            `json:"inbox_head_seq"`
	Pending         int               `json:"inbox_pending"`
	Sent            []sessionSent     `json:"recent_sent"`
	SentTruncated   bool              `json:"recent_sent_truncated"`
}
type sessionEvent struct {
	Kind      string      `json:"kind"`
	Mark      sessionMark `json:"watermark"`
	MessageID string      `json:"message_id"`
	Seq       uint64      `json:"seq"`
	Direction string      `json:"direction"`
	Head      uint64      `json:"head_seq"`
	Pending   *int        `json:"pending"`
	AgentID   string      `json:"agent_id"`
	SessionID string      `json:"session_id"`
	Stage     string      `json:"stage"`
	State     string      `json:"state"`
	Version   int         `json:"protocol_version"`
}

func sessionDecode[T any](t *testing.T, frame sessionWire, key string) T {
	t.Helper()
	var out T
	if len(frame[key]) == 0 || string(frame[key]) == "null" {
		t.Fatalf("missing %s in %s", key, sessionJSON(frame))
	}
	operatorNoError(t, json.Unmarshal(frame[key], &out))
	return out
}

func (p *sessionProbe) snapshot() sessionSnapshot {
	p.t.Helper()
	r, _ := p.request("operator_snapshot", nil)
	operatorEqual(p.t, "snapshot response", r.text("type"), "operator_snapshot")
	if strings.Contains(sessionJSON(r), "PRIVATE_") {
		p.t.Fatal("metadata snapshot included message body")
	}
	return sessionDecode[sessionSnapshot](p.t, r, "snapshot")
}

func (p *sessionProbe) event(mark *sessionMark) sessionEvent {
	p.t.Helper()
	return sessionCheckEvent(p.t, p.frame(), mark)
}

func sessionCheckEvent(t *testing.T, frame sessionWire, mark *sessionMark) sessionEvent {
	t.Helper()
	operatorEqual(t, "event envelope", frame.text("type"), "event")
	operatorEqual(t, "event has no request ID", frame.text("request_id"), "")
	e := sessionDecode[sessionEvent](t, frame, "event")
	operatorEqual(t, "event epoch", e.Mark.Epoch, mark.Epoch)
	operatorEqual(t, "contiguous event revision", e.Mark.Revision, mark.Revision+1)
	*mark = e.Mark
	return e
}

// An ordinary correlated response is a FIFO barrier without resetting the
// subscription (a snapshot would intentionally discard queued events).
func (p *sessionProbe) barrier() []sessionWire {
	p.t.Helper()
	r, events := p.request("acceptance_barrier", nil)
	operatorEqual(p.t, "barrier error", r.text("code"), "unsupported_operation")
	return events
}

func TestOperatorSessionOpenCapabilitiesAndSnapshotAreNonConsuming(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	inboxSend(t, a, inboxMessage("unread"))
	p, opened := sessionConnect(f)
	operatorEqual(t, "open response first", opened.text("type"), "operator_session")
	info := sessionDecode[struct {
		ID           string    `json:"session_id"`
		Epoch        string    `json:"epoch"`
		Capabilities []string  `json:"capabilities"`
		Opened       time.Time `json:"opened_at"`
	}](t, opened, "session")
	operatorEqual(t, "owner identity", info.ID != "", true)
	operatorEqual(t, "epoch", info.Epoch != "", true)
	operatorEqual(t, "opened time", info.Opened.IsZero(), false)
	for _, capability := range []string{"send", "receive", "received", "ack", "snapshot", "message_stages", "events"} {
		operatorEqual(t, capability, slices.Contains(info.Capabilities, capability), true)
	}
	before := f.count("operator_audit")
	snap := p.snapshot()
	operatorEqual(t, "snapshot epoch", snap.Mark.Epoch, info.Epoch)
	operatorEqual(t, "pending", snap.Pending, 1)
	operatorEqual(t, "presence excludes operator", snap.AgentsTotal, 1)
	operatorEqual(t, "presence entry count", len(snap.Agents), 1)
	operatorEqual(t, "agent identity", snap.Agents[0].AgentID, "alpha")
	operatorEqual(t, "agent version", snap.Agents[0].Version, 5)
	operatorEqual(t, "metadata read unaudited", f.count("operator_audit"), before)
	operatorEqual(t, "snapshot doesn't receipt", inboxReceiptCount(f, "received_at"), 0)
	operatorEqual(t, "snapshot doesn't acknowledge", inboxReceiptCount(f, "acknowledged_at"), 0)
}

func TestOperatorSessionArrivalAndExternalSendEvents(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := sessionOpen(f)
	mark := p.snapshot().Mark
	a := f.peer("alpha", 5)
	e := p.event(&mark)
	operatorEqual(t, "connect event", e.Kind, "presence_changed")
	operatorEqual(t, "presence agent", e.AgentID, "alpha")
	operatorEqual(t, "presence state", e.State, "connected")
	operatorEqual(t, "presence session", e.SessionID != "", true)
	operatorEqual(t, "presence version", e.Version, 5)
	accepted := inboxSend(t, a, inboxMessage("live-incoming"))
	arrival := []sessionEvent{p.event(&mark), p.event(&mark)}
	for _, e := range arrival {
		switch e.Kind {
		case "conversation_changed":
			operatorEqual(t, "incoming ID", e.MessageID, "live-incoming")
			operatorEqual(t, "incoming seq", e.Seq, accepted.Seq)
			operatorEqual(t, "incoming direction", e.Direction, "received")
		case "inbox_changed":
			operatorEqual(t, "incoming head", e.Head, accepted.Seq)
			if e.Pending == nil || *e.Pending != 1 {
				t.Fatalf("pending event: %+v", e)
			}
		default:
			t.Fatalf("unexpected arrival event: %+v", e)
		}
	}
	operatorEqual(t, "distinct arrival notifications", arrival[0].Kind != arrival[1].Kind, true)
	req := operatorRequest("external-live-send", "alpha")
	requireOperatorAccepted(t, f.send(req), "alpha")
	a.read()
	e = p.event(&mark)
	operatorEqual(t, "one-shot send event", e.Kind, "conversation_changed")
	operatorEqual(t, "one-shot send direction", e.Direction, "sent")
	operatorEqual(t, "one-shot send ID", e.MessageID, req.MessageID)
	requireOperatorAccepted(t, f.send(req), "alpha")
	operatorEqual(t, "retry emits no event", len(p.barrier()), 0)
	inboxExec(f, `CREATE TRIGGER fail_live_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	operatorEqual(t, "send audit fails", f.send(operatorRequest("rolled-back", "alpha")).Code, "internal")
	operatorEqual(t, "rollback emits no event", len(p.barrier()), 0)
}

func TestOperatorSessionSnapshotBarrierAndRequestIDReuse(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := sessionOpen(f)
	f.peer("before-snapshot", 5)
	p.send(map[string]any{"type": "operator_snapshot", "request_id": "repeatable"})
	r := p.frame()
	operatorEqual(t, "no event before baseline", r.text("request_id"), "repeatable")
	first := sessionDecode[sessionSnapshot](t, r, "snapshot")
	operatorEqual(t, "pre-baseline presence in snapshot", first.AgentsTotal, 1)
	// Reading the response permits reusing its ID. A later snapshot replaces
	// the baseline and any subsequent event must start immediately above it.
	p.send(map[string]any{"type": "operator_snapshot", "request_id": "repeatable"})
	r = p.frame()
	operatorEqual(t, "completed ID reused", r.text("type"), "operator_snapshot")
	second := sessionDecode[sessionSnapshot](t, r, "snapshot")
	operatorEqual(t, "stable watermark without changes", second.Mark, first.Mark)
	f.peer("after-snapshot", 5)
	mark := second.Mark
	e := p.event(&mark)
	operatorEqual(t, "event above new cut", e.AgentID, "after-snapshot")
}

func TestOperatorSessionEpochChangesOnBrokerRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "session-restart.db")
	var first sessionMark
	t.Run("before", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		a := f.peer("alpha", 5)
		inboxSend(t, a, inboxMessage("survives-restart"))
		first = sessionOpen(f).snapshot().Mark
	})
	t.Run("after", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		snap := sessionOpen(f).snapshot()
		operatorEqual(t, "restart has new epoch", snap.Mark.Epoch != first.Epoch, true)
		operatorEqual(t, "restart keeps pending inbox", snap.Pending, 1)
		operatorEqual(t, "restart doesn't receipt", inboxReceiptCount(f, "received_at"), 0)
	})
}

// Sending an invalid operation on the same agent connection orders the barrier
// after a receipt, whose adapter_received stage has no agent-side confirmation.
func sessionAgentBarrier(t *testing.T, a *operatorPeer) {
	t.Helper()
	a.write(protocol.Message{Type: "acceptance_barrier"})
	operatorEqual(t, "agent barrier", a.read().Type, "error")
}

func TestOperatorSessionReceiptEventsCommitOnlyAndNeverRepeat(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	inboxSend(t, a, inboxMessage("receipt-events"))
	p := sessionOpen(f)
	mark := p.snapshot().Mark
	for _, stage := range []struct {
		kind, want, column string
		fields             map[string]any
		pending            int
	}{
		{"operator_received", "adapter_received", "received_at", map[string]any{"message_ids": []string{"receipt-events"}}, 1},
		{"operator_ack", "agent_acknowledged", "acknowledged_at", map[string]any{"message_id": "receipt-events"}, 0},
	} {
		inboxExec(f, `CREATE TRIGGER fail_receipt_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
		r, events := p.request(stage.kind, stage.fields)
		operatorEqual(t, "receipt audit failure", r.text("code"), "internal")
		operatorEqual(t, "failure emits no event", len(append(events, p.barrier()...)), 0)
		operatorEqual(t, "failure leaves receipt unchanged", inboxReceiptCount(f, stage.column), 0)
		inboxExec(f, `DROP TRIGGER fail_receipt_audit`)
		r, events = p.request(stage.kind, stage.fields)
		operatorEqual(t, "receipt commits", r.text("stage"), stage.want)
		inboxStage(t, a, "receipt-events", stage.want)
		events = append(events, p.barrier()...)
		operatorEqual(t, "one inbox event per transition", len(events), 1)
		e := sessionCheckEvent(t, events[0], &mark)
		operatorEqual(t, "receipt event kind", e.Kind, "inbox_changed")
		if e.Pending == nil || *e.Pending != stage.pending {
			t.Fatalf("wrong pending after %s: %+v", stage.kind, e)
		}
		before := inboxTimestamps(f, "receipt-events")
		_, events = p.request(stage.kind, stage.fields)
		operatorEqual(t, "retry emits no event", len(append(events, p.barrier()...)), 0)
		operatorEqual(t, "retry retains timestamps", inboxTimestamps(f, "receipt-events"), before)
	}
}
