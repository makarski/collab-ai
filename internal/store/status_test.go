package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

func readStatus(t *testing.T, st *Store) StatusRecords {
	t.Helper()
	out, err := st.ReadStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertStatusEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func totalChanges(t *testing.T, st *Store) int {
	t.Helper()
	var changes int
	if err := st.db.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	return changes
}

func TestStatusCountsOnlyDurablePendingDeliveries(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	msg := durableReview("broadcast")
	msg.To, msg.Payload = "*", []byte(`{"text":"PRIVATE_MESSAGE_BODY"}`)
	recipients := []protocol.Recipient{{AgentID: "a", SessionID: "a1"}, {AgentID: "b", SessionID: "b1"}}
	if err := st.PersistMessage(ctx, msg, msg.SessionID, recipients); err != nil {
		t.Fatal(err)
	}
	legacy := durableReview("legacy")
	legacy.Seq, legacy.Durable = 2, false
	if err := st.PersistMessage(ctx, legacy, legacy.SessionID, recipients); err != nil {
		t.Fatal(err)
	}
	before := totalChanges(t, st)
	out := readStatus(t, st)
	assertStatusEqual(t, "pending deliveries", out.Pending.Total, 2)
	assertStatusEqual(t, "pending recipients", len(out.Pending.Recipients), 2)
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "PRIVATE_MESSAGE_BODY") {
		t.Fatal("status leaked a message body")
	}
	assertStatusEqual(t, "database changes after status", totalChanges(t, st), before)
	ack := Acknowledgment{MessageID: msg.MessageID, Recipient: recipients[0], Stage: protocol.StageAdapterReceived, ProtocolVersion: protocol.Version}
	checkDurableAck(t, st, ack)
	assertStatusEqual(t, "pending after adapter receipt", readStatus(t, st).Pending.Total, 2)
	ack.Stage = protocol.StageAgentAcknowledged
	checkDurableAck(t, st, ack)
	out = readStatus(t, st)
	assertStatusEqual(t, "pending after acknowledgment", out.Pending.Total, 1)
	assertStatusEqual(t, "remaining recipient", out.Pending.Recipients[0].AgentID, "b")
}

func TestStatusHistoryAndRecipientLimits(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var recipients []protocol.Recipient
	for i := range protocol.StatusLimit + 2 {
		id := fmt.Sprintf("agent-%03d", i)
		r := protocol.Recipient{AgentID: id, SessionID: id + "-session"}
		recipients = append(recipients, r)
		if err := st.RecordConnect(ctx, SessionRecord{Identity: r, ConnectedAt: now.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordDisconnect(ctx, recipients[101].SessionID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	msg := durableReview("many")
	if err := st.PersistMessage(ctx, msg, msg.SessionID, recipients); err != nil {
		t.Fatal(err)
	}
	out := readStatus(t, st)
	assertStatusEqual(t, "history limit", len(out.Sessions), protocol.StatusLimit+1)
	assertStatusEqual(t, "newest session", out.Sessions[0].AgentID, recipients[101].AgentID)
	assertStatusEqual(t, "closed session state", out.Sessions[0].State, "disconnected")
	assertStatusEqual(t, "unclosed session state", out.Sessions[1].State, "stale")
	assertStatusEqual(t, "unrecorded last seen", out.Sessions[1].LastSeenAt, nil)
	assertStatusEqual(t, "unrecorded disconnect reason", out.Sessions[1].DisconnectReason, nil)
	assertStatusEqual(t, "total beyond recipient limit", out.Pending.Total, 102)
	assertStatusEqual(t, "recipient limit", len(out.Pending.Recipients), 100)
	assertStatusEqual(t, "recipients truncated", out.Pending.RecipientsTruncated, true)
	assertStatusEqual(t, "first recipient", out.Pending.Recipients[0].AgentID, "agent-000")
}

func TestStatusReadFailureIsNotAnEmptySnapshot(t *testing.T) {
	st := openTemp(t)
	if _, err := st.db.Exec(`DROP TABLE durable_inbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadStatus(context.Background()); err == nil {
		t.Fatal("missing durable storage reported as zero pending")
	}
}
