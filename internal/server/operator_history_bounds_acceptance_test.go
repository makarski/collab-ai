//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

func historyPayload(size int) json.RawMessage {
	return json.RawMessage(`"` + strings.Repeat("x", size-2) + `"`)
}

func TestOperatorHistoryNewSendEncodedPayloadLimit(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	req := operatorRequest("at-cap", "alpha")
	req.Payload = historyPayload(512 << 10)
	requireOperatorAccepted(t, f.send(req), "alpha")
	a.read()
	page := historyRead(f, 0, 0, 1)
	operatorEqual(t, "boundary payload inline", len(page.Entries[0].Message.Payload), 512<<10)
	operatorEqual(t, "boundary body not omitted", page.Entries[0].Omitted, false)
	req.MessageID = "over-cap"
	req.Payload = historyPayload((512 << 10) + 1)
	r := f.send(req)
	operatorEqual(t, "oversized rejection", r.Code, "malformed_frame")
	operatorEqual(t, "size reason", strings.Contains(r.Detail, "payload too large"), true)
	operatorEqual(t, "rejected body not persisted", f.count("messages"), 1)
}

func TestOperatorHistoryPayloadCapCountsJSONExpansion(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	f.peer("alpha", 5)
	for i, body := range []string{strings.Repeat("<", 90000), strings.Repeat("\u2028", 90000)} {
		p := operatorDial(t, f.operator)
		operatorNoError(t, p.SetWriteDeadline(time.Now().Add(2*time.Second)))
		// Preserve literal HTML in the incoming JSON. The outbound frame writer
		// escapes it, so counting only raw incoming payload bytes is insufficient.
		enc := json.NewEncoder(p.Conn)
		enc.SetEscapeHTML(false)
		operatorNoError(t, enc.Encode(map[string]any{"type": "operator_send", "message_id": fmt.Sprintf("expanded-%d", i), "to": "alpha", "payload": body}))
		operatorEqual(t, "expanded payload rejected", p.read().Code, "malformed_frame")
	}
	operatorEqual(t, "expanded bodies not persisted", f.count("messages"), 0)
}

func TestOperatorHistoryByteBoundPagesWithoutDroppingBodies(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	for i := 0; i < 3; i++ {
		req := operatorRequest(fmt.Sprintf("body-%d", i), "alpha")
		req.Payload = historyPayload(400 << 10)
		requireOperatorAccepted(t, f.send(req), "alpha")
		a.read()
	}
	page := historyRead(f, 0, 0, 32)
	operatorEqual(t, "byte bound reduces count", len(page.Entries), 2)
	operatorEqual(t, "byte bound reports more", page.More, true)
	for _, entry := range page.Entries {
		operatorEqual(t, "full body", len(entry.Message.Payload), 400<<10)
		operatorEqual(t, "not omitted merely to fill page", entry.Omitted, false)
	}
	last := historyRead(f, page.Next, page.Upper, 32)
	operatorEqual(t, "remaining oldest", last.Entries[0].Message.MessageID, "body-0")
	operatorEqual(t, "remaining full body", len(last.Entries[0].Message.Payload), 400<<10)
	operatorEqual(t, "all entries visited", last.More, false)
}

// Seed a pre-cap message using a normal accepted record, replacing only its
// retained payload/accounting. This models a message admitted by the old broker.
func historyRetainedPayload(f *operatorFixture, delivered protocol.Message, payload json.RawMessage) {
	f.t.Helper()
	_, err := f.db.Exec(`UPDATE messages SET payload=? WHERE seq=?`, string(payload), delivered.Seq)
	operatorNoError(f.t, err)
	delivered.Payload = payload
	wire, err := store.DurableWireBytes(delivered)
	operatorNoError(f.t, err)
	_, err = f.db.Exec(`UPDATE durable_messages SET wire_bytes=?,storage_bytes=? WHERE message_id=?`, wire, wire, delivered.MessageID)
	operatorNoError(f.t, err)
}

func TestOperatorHistoryRetainedLargeRetrySurvivesNewCap(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	delivered := historySend(f, a, "grandfathered")
	body := historyPayload(600 << 10)
	historyRetainedPayload(f, delivered, body)
	req := operatorRequest("grandfathered", "alpha")
	req.Payload = body
	retry := f.send(req)
	requireOperatorAccepted(t, retry, "alpha")
	operatorEqual(t, "old exact retry retained", retry.Repeated, true)
	operatorEqual(t, "old sequence retained", retry.Seq, delivered.Seq)
	page := historyRead(f, 0, 0, 1)
	operatorEqual(t, "old over-cap body still fits", len(page.Entries[0].Message.Payload), len(body))
	operatorEqual(t, "cap isn't history omission threshold", page.Entries[0].Omitted, false)
	req.Payload = historyPayload((600 << 10) + 1)
	operatorEqual(t, "changed old retry remains duplicate", f.send(req).Code, "duplicate_message")
}

func TestOperatorHistoryRetainedNearFrameBodyOmittedWithoutSkipping(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	historySend(f, a, "older")
	large := historySend(f, a, "legacy-near-frame")
	large.Payload = json.RawMessage(`""`)
	large.Replayed = true
	encoded, err := json.Marshal(large)
	operatorNoError(t, err)
	body := historyPayload(protocol.MaxFrameBytes - len(encoded) + 1)
	historyRetainedPayload(f, large, body)
	page := historyRead(f, 0, 0, 1)
	operatorEqual(t, "large row not skipped", page.Entries[0].Message.MessageID, large.MessageID)
	operatorEqual(t, "body explicitly omitted", page.Entries[0].Omitted, true)
	operatorEqual(t, "payload key absent", len(page.Entries[0].Message.Payload), 0)
	operatorEqual(t, "older still available", page.More, true)
	last := historyRead(f, page.Next, page.Upper, 1)
	operatorEqual(t, "continues past omitted entry", last.Entries[0].Message.MessageID, "older")
}

func TestOperatorHistoryRetainedBodyThatFitsIsNotOmitted(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	delivered := historySend(f, a, "legacy-still-fits")
	body := historyPayload(protocol.MaxFrameBytes - 2048)
	historyRetainedPayload(f, delivered, body)
	page := historyRead(f, 0, 0, 1)
	// Prove the full response fits, including its metadata, instead of relying
	// on a payload-size assumption. The typed fixture includes extra zero fields.
	complete := page
	complete.Entries = append([]historyEntry(nil), page.Entries...)
	complete.Entries[0].Message.Payload = body
	complete.Entries[0].Omitted = false
	encoded, err := json.Marshal(map[string]any{"type": "operator_history", "history": complete})
	operatorNoError(t, err)
	operatorEqual(t, "fixture full frame fits", len(encoded)+1 <= protocol.MaxFrameBytes, true)
	operatorEqual(t, "fitting old body stays inline", page.Entries[0].Omitted, false)
	operatorEqual(t, "fitting body preserved", len(page.Entries[0].Message.Payload), len(body))
}

func TestOperatorHistoryFanoutReceiptSummaryAndOneShotStages(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	for i := 0; i < 20; i++ {
		f.peer(fmt.Sprintf("recipient-%02d", i), 5)
	}
	operatorEqual(t, "fanout accepted", f.send(operatorRequest("fanout", "*")).Stage, "accepted")
	p := sessionOpen(f)
	mark := p.snapshot().Mark
	page := historyRead(f, 0, 0, 1)
	entry := page.Entries[0]
	operatorEqual(t, "history recipient cap", len(entry.Recipients), 16)
	operatorEqual(t, "receipt truncation explicit", entry.Truncated, true)
	before := f.count("operator_audit")
	r := historyCall(f, map[string]any{"type": "operator_message_stages", "message_id": "fanout", "after_agent": entry.Recipients[15].AgentID, "limit": 16})
	operatorEqual(t, "one-shot stages allowed with owner", r.text("type"), "operator_message_stages")
	stages := sessionDecode[sessionStagePage](t, r, "stages")
	operatorEqual(t, "remaining receipts", len(stages.Recipients), 4)
	operatorEqual(t, "receipt pages complete", stages.More, false)
	operatorEqual(t, "stage read not audited", f.count("operator_audit"), before)
	operatorEqual(t, "metadata read watermark", stages.Mark, mark)
}

func TestOperatorHistoryBudgetIncludesEscapedSessionRequestID(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	delivered := historySend(f, a, "request-envelope")
	historyRetainedPayload(f, delivered, historyPayload(protocol.MaxFrameBytes-1024))
	r := historyCall(f, map[string]any{"type": "operator_history", "page_request": map[string]any{"limit": 1}})
	plain := sessionDecode[historyPage](t, r, "history")
	operatorEqual(t, "body fits without request ID", plain.Entries[0].Omitted, false)
	id := strings.Repeat("\x01", 128)
	r["request_id"], _ = json.Marshal(id)
	encoded, err := json.Marshal(r)
	operatorNoError(t, err)
	operatorEqual(t, "escaped ID would overflow full body", len(encoded)+1 > protocol.MaxFrameBytes, true)
	p := sessionOpen(f)
	p.send(map[string]any{"type": "operator_history", "request_id": id, "page_request": map[string]any{"limit": 1}})
	response := p.frame() // Also asserts the final serialized frame limit.
	operatorEqual(t, "long ID remains correlated", response.text("request_id"), id)
	page := sessionDecode[historyPage](t, response, "history")
	operatorEqual(t, "body omitted only for larger envelope", page.Entries[0].Omitted, true)
}

func TestOperatorHistoryNullAndLegacyAbsentPayloadsRemainReadable(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	for _, tc := range []struct {
		id      string
		payload json.RawMessage
	}{
		{"explicit-null", json.RawMessage(`null`)},
		{"absent-body", nil},
	} {
		req := operatorRequest(tc.id, "alpha")
		req.Payload = tc.payload
		requireOperatorAccepted(t, f.send(req), "alpha")
		a.read()
	}
	page := historyRead(f, 0, 0, 16)
	operatorEqual(t, "both payload variants readable", len(page.Entries), 2)
	for _, entry := range page.Entries {
		operatorEqual(t, "missing content represented as JSON null", string(entry.Message.Payload), "null")
		operatorEqual(t, "not an oversized-body omission", entry.Omitted, false)
	}
}
