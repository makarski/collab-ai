package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

func persistReview(t *testing.T, st *Store) protocol.Message {
	t.Helper()
	now := time.Now().UTC()
	msg := protocol.Message{MessageID: "review", Seq: 10, TS: &now, From: "claude", To: "codex", InReplyTo: "context", AckRequested: true}
	recipients := []protocol.Recipient{reviewAck(protocol.StageAdapterReceived).Recipient}
	if err := st.PersistMessage(context.Background(), msg, "claude-session", recipients); err != nil {
		t.Fatal(err)
	}
	return msg
}

func reviewAck(stage string) Acknowledgment {
	return Acknowledgment{MessageID: "review", Recipient: protocol.Recipient{AgentID: "codex", SessionID: "codex-session"}, Stage: stage}
}

func recordReviewAck(t *testing.T, st *Store, stage string) {
	t.Helper()
	sender, err := st.Acknowledge(context.Background(), reviewAck(stage))
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.Recipient{AgentID: "claude", SessionID: "claude-session"}
	if sender != want {
		t.Fatalf("receipt routed to %+v, want %+v", sender, want)
	}
}

func assertPendingCount(t *testing.T, st *Store, want int) {
	t.Helper()
	pending, err := st.UnacknowledgedForSession(context.Background(), "codex-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != want {
		t.Fatalf("pending agent acknowledgments = %d, want %d", len(pending), want)
	}
}

func assertTableCount(t *testing.T, st *Store, table string, want int) {
	t.Helper()
	var count int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s has %d rows, want %d", table, count, want)
	}
}

func openReceiptStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func createLegacyHistory(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE messages (seq INTEGER PRIMARY KEY, ts TIMESTAMP NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL, payload TEXT NOT NULL); INSERT INTO messages VALUES (9, CURRENT_TIMESTAMP, 'old', '*', '{}')`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestReceiptMigrationPreservesHistoryAndCorrelation(t *testing.T) {
	path := t.TempDir() + "/old.db"
	createLegacyHistory(t, path)
	st := openReceiptStore(t, path)
	persistReview(t, st)
	st.Close()
	st = openReceiptStore(t, path)
	assertTableCount(t, st, "messages", 2)
	var reply string
	if err := st.db.QueryRow(`SELECT in_reply_to FROM message_metadata WHERE message_id = 'review'`).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	if reply != "context" {
		t.Fatalf("correlation lost: %s", reply)
	}
}

func TestReceiptRequiresAssignedSessionAndStageOrder(t *testing.T) {
	st := openTemp(t)
	persistReview(t, st)
	cases := map[string]Acknowledgment{
		"before receipt": reviewAck(protocol.StageAgentAcknowledged),
		"wrong agent":    {MessageID: "review", Recipient: protocol.Recipient{AgentID: "intruder", SessionID: "codex-session"}, Stage: protocol.StageAdapterReceived},
		"wrong session":  {MessageID: "review", Recipient: protocol.Recipient{AgentID: "codex", SessionID: "another-session"}, Stage: protocol.StageAdapterReceived},
	}
	for name, ack := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := st.Acknowledge(context.Background(), ack); !errors.Is(err, ErrInvalidAck) {
				t.Fatalf("foreign or premature receipt accepted: %v", err)
			}
		})
	}
}

func TestReceiptStagesSurviveRestartAndAreIdempotent(t *testing.T) {
	path := t.TempDir() + "/receipts.db"
	st := openReceiptStore(t, path)
	persistReview(t, st)
	recordReviewAck(t, st, protocol.StageAdapterReceived)
	assertPendingCount(t, st, 1)
	st.Close()
	st = openReceiptStore(t, path)
	recordReviewAck(t, st, protocol.StageAgentAcknowledged)
	recordReviewAck(t, st, protocol.StageAgentAcknowledged)
	st.Close()
	st = openReceiptStore(t, path)
	assertPendingCount(t, st, 0)
}

func TestDuplicateMessageRejectedAfterRestart(t *testing.T) {
	path := t.TempDir() + "/receipts.db"
	st := openReceiptStore(t, path)
	msg := persistReview(t, st)
	st.Close()
	st = openReceiptStore(t, path)
	msg.Seq = 11
	recipients := []protocol.Recipient{reviewAck(protocol.StageAdapterReceived).Recipient}
	if err := st.PersistMessage(context.Background(), msg, "other-sender", recipients); !errors.Is(err, ErrDuplicateMessage) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	seq, err := st.LastSeq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if seq != 10 {
		t.Fatalf("sequence/history changed: %d", seq)
	}
	assertTableCount(t, st, "messages", 1)
}

func TestAcceptanceTransactionRollsBackOnRecipientFailure(t *testing.T) {
	st := openTemp(t)
	now := time.Now().UTC()
	msg := protocol.Message{MessageID: "rollback", Seq: 1, TS: &now, From: "claude", To: "*", AckRequested: true}
	duplicate := protocol.Recipient{AgentID: "codex", SessionID: "owner"}
	if err := st.PersistMessage(context.Background(), msg, "sender", []protocol.Recipient{duplicate, duplicate}); err == nil {
		t.Fatal("duplicate membership succeeded")
	}
	for _, table := range []string{"messages", "message_metadata", "message_receipts"} {
		assertTableCount(t, st, table, 0)
	}
}
