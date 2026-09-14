package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

func TestReceiptMigrationPersistenceAndOwnership(t *testing.T) {
	path := t.TempDir() + "/old.db"
	// Start with the original history schema, not the new schema helper.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE messages (seq INTEGER PRIMARY KEY, ts TIMESTAMP NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL, payload TEXT NOT NULL); INSERT INTO messages VALUES (9, CURRENT_TIMESTAMP, 'old', '*', '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	msg := protocol.Message{MessageID: "review", Seq: 10, TS: &now, From: "claude", To: "codex", InReplyTo: "context", AckRequested: true}
	recipients := []protocol.Recipient{{AgentID: "codex", SessionID: "codex-session"}}
	if err := st.PersistMessage(ctx, msg, "claude-session", recipients); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Acknowledge(ctx, "review", "codex", "codex-session", protocol.StageAgentAcknowledged); !errors.Is(err, ErrInvalidAck) {
		t.Fatalf("agent acknowledgment before receipt: %v", err)
	}
	for _, identity := range []protocol.Recipient{{AgentID: "intruder", SessionID: "codex-session"}, {AgentID: "codex", SessionID: "another-session"}} {
		if _, _, err := st.Acknowledge(ctx, "review", identity.AgentID, identity.SessionID, protocol.StageAdapterReceived); !errors.Is(err, ErrInvalidAck) {
			t.Fatalf("foreign receipt accepted: %v", err)
		}
	}
	sender, session, err := st.Acknowledge(ctx, "review", "codex", "codex-session", protocol.StageAdapterReceived)
	if err != nil || sender != "claude" || session != "claude-session" {
		t.Fatalf("receipt: %s %s %v", sender, session, err)
	}
	pending, err := st.UnacknowledgedForSession(ctx, "codex-session", 0)
	if err != nil || len(pending) != 1 {
		t.Fatalf("adapter receipt removed pending agent acknowledgment: %+v %v", pending, err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A duplicate ID after restart must not insert another message or change content.
	msg.Seq = 11
	if err := st.PersistMessage(ctx, msg, "other-sender", recipients); !errors.Is(err, ErrDuplicateMessage) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	if seq, err := st.LastSeq(ctx); err != nil || seq != 10 {
		t.Fatalf("sequence/history changed: %d %v", seq, err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := st.Acknowledge(ctx, "review", "codex", "codex-session", protocol.StageAgentAcknowledged); err != nil {
			t.Fatal(err)
		}
	}
	pending, err = st.UnacknowledgedForSession(ctx, "codex-session", 0)
	if err != nil || len(pending) != 0 {
		t.Fatalf("agent acknowledgment not persisted: %+v %v", pending, err)
	}
	var reply string
	if err := st.db.QueryRow(`SELECT in_reply_to FROM message_metadata WHERE message_id = 'review'`).Scan(&reply); err != nil || reply != "context" {
		t.Fatalf("correlation lost: %s %v", reply, err)
	}
	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy history lost: %d %v", count, err)
	}
}

func TestAcceptanceTransactionRollsBackOnRecipientFailure(t *testing.T) {
	st := openTemp(t)
	now := time.Now().UTC()
	msg := protocol.Message{MessageID: "rollback", Seq: 1, TS: &now, From: "claude", To: "*", AckRequested: true}
	duplicate := protocol.Recipient{AgentID: "codex", SessionID: "owner"}
	if err := st.PersistMessage(context.Background(), msg, "sender", []protocol.Recipient{duplicate, duplicate}); err == nil {
		t.Fatal("duplicate membership succeeded")
	}
	var count int
	for _, table := range []string{"messages", "message_metadata", "message_receipts"} {
		if err := st.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial acceptance in %s: %d %v", table, count, err)
		}
	}
}
