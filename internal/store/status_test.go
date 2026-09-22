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
	var before, after int
	if err := st.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	out := readStatus(t, st)
	if out.Pending.Total != 2 || len(out.Pending.Recipients) != 2 {
		t.Fatalf("pending: %+v", out)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "PRIVATE_MESSAGE_BODY") {
		t.Fatal("status leaked a message body")
	}
	if err := st.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("status mutated the database")
	}
	ack := Acknowledgment{MessageID: msg.MessageID, Recipient: recipients[0], Stage: protocol.StageAdapterReceived, ProtocolVersion: protocol.Version}
	checkDurableAck(t, st, ack)
	if readStatus(t, st).Pending.Total != 2 {
		t.Fatal("adapter receipt cleared pending count")
	}
	ack.Stage = protocol.StageAgentAcknowledged
	checkDurableAck(t, st, ack)
	out = readStatus(t, st)
	if out.Pending.Total != 1 || out.Pending.Recipients[0].AgentID != "b" {
		t.Fatalf("explicit ack not reflected: %+v", out)
	}
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
	if len(out.Sessions) != protocol.StatusLimit+1 || out.Sessions[0].AgentID != recipients[101].AgentID {
		t.Fatalf("history bound/order: %+v", out.Sessions)
	}
	if out.Sessions[0].State != "disconnected" || out.Sessions[1].State != "stale" {
		t.Fatal("invented live history")
	}
	if out.Sessions[1].LastSeenAt != nil || out.Sessions[1].DisconnectReason != nil {
		t.Fatal("invented lifecycle details")
	}
	if out.Pending.Total != 102 || len(out.Pending.Recipients) != 100 || !out.Pending.RecipientsTruncated {
		t.Fatalf("pending bound: %+v", out.Pending)
	}
	if out.Pending.Recipients[0].AgentID != "agent-000" {
		t.Fatal("recipient order unstable")
	}
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
