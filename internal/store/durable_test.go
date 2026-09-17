package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

func durableReview(id string) protocol.Message {
	now := time.Now().UTC()
	return protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: 1, TS: &now, From: "sender", SessionID: "sender-old", To: "recipient", Payload: []byte(`{"text":"review"}`), InReplyTo: "context", AckRequested: true, Durable: true}
}

func persistDurableReview(t *testing.T, st *Store, msg protocol.Message) {
	t.Helper()
	err := st.PersistMessage(context.Background(), msg, msg.SessionID, []protocol.Recipient{{AgentID: "recipient", SessionID: "old"}})
	if err != nil {
		t.Fatal(err)
	}
}

func durableAck(session, stage string) Acknowledgment {
	return Acknowledgment{MessageID: "review", Recipient: protocol.Recipient{AgentID: "recipient", SessionID: session}, Stage: stage, ProtocolVersion: protocol.Version}
}

func claimReview(t *testing.T, st *Store, session string) []protocol.Message {
	t.Helper()
	msgs, err := st.ClaimPending(context.Background(), protocol.Recipient{AgentID: "recipient", SessionID: session})
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func checkDurableAck(t *testing.T, st *Store, ack Acknowledgment) {
	t.Helper()
	if _, err := st.Acknowledge(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
}

func TestDurableReplayOwnershipAndLostAckConfirmation(t *testing.T) {
	path := t.TempDir() + "/durable.db"
	st := openReceiptStore(t, path)
	persistDurableReview(t, st, durableReview("review"))
	checkDurableAck(t, st, durableAck("old", protocol.StageAdapterReceived))
	st.Close()
	st = openReceiptStore(t, path)
	msgs := claimReview(t, st, "new")
	if len(msgs) != 1 {
		t.Fatalf("replay: %+v", msgs)
	}
	if !msgs[0].Replayed || msgs[0].InReplyTo != "context" {
		t.Fatal("replay identity/correlation missing")
	}
	for _, session := range []string{"old", "new"} {
		_, err := st.Acknowledge(context.Background(), durableAck(session, protocol.StageAgentAcknowledged))
		if !errors.Is(err, ErrInvalidAck) {
			t.Fatalf("premature/old owner acknowledgment: %v", err)
		}
	}
	checkDurableAck(t, st, durableAck("new", protocol.StageAdapterReceived))
	checkDurableAck(t, st, durableAck("new", protocol.StageAgentAcknowledged))
	// Simulate losing the confirmation after the commit and restarting both ends.
	st.Close()
	st = openReceiptStore(t, path)
	if len(claimReview(t, st, "latest")) != 0 {
		t.Fatal("committed acknowledgment replayed")
	}
	checkDurableAck(t, st, durableAck("latest", protocol.StageAgentAcknowledged))
	assertTableCount(t, st, "messages", 1)
}

func TestDurableRetriesPreserveMembershipAndIsolation(t *testing.T) {
	st := openTemp(t)
	msg := durableReview("review")
	persistDurableReview(t, st, msg)
	msg.SessionID = "sender-new"
	snapshot, err := st.LookupRetry(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Recipients[0].SessionID != "old" {
		t.Fatal("acceptance membership changed")
	}
	claimReview(t, st, "new")
	checkDurableAck(t, st, durableAck("new", protocol.StageAdapterReceived))
	checkDurableAck(t, st, durableAck("new", protocol.StageAgentAcknowledged))
	snapshot, err = st.LookupRetry(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Receipts[0].Stage != protocol.StageAgentAcknowledged {
		t.Fatal("retry did not report completed receipt")
	}
	if snapshot.Receipts[0].SessionID != "new" {
		t.Fatal("receipt reported wrong owner")
	}
	msg.From = "intruder"
	if _, err := st.LookupRetry(context.Background(), msg); !errors.Is(err, ErrDuplicateMessage) {
		t.Fatalf("sender isolation: %v", err)
	}
	ack := durableAck("new", protocol.StageAgentAcknowledged)
	ack.Recipient.AgentID = "intruder"
	if _, err := st.Acknowledge(context.Background(), ack); !errors.Is(err, ErrInvalidAck) {
		t.Fatalf("recipient isolation: %v", err)
	}
}

func TestPendingLimitRejectsAtomicallyAndAckFreesCapacity(t *testing.T) {
	st := openTemp(t)
	for i := range MaxPendingMessages {
		msg := durableReview(fmt.Sprint(i))
		msg.Seq = uint64(i + 1)
		persistDurableReview(t, st, msg)
	}
	msg := durableReview("overflow")
	msg.Seq = 100
	err := st.PersistMessage(context.Background(), msg, msg.SessionID, []protocol.Recipient{{AgentID: "recipient", SessionID: "old"}})
	if !errors.Is(err, ErrInboxFull) {
		t.Fatalf("limit not enforced: %v", err)
	}
	assertTableCount(t, st, "messages", MaxPendingMessages)
	ack := durableAck("old", protocol.StageAdapterReceived)
	ack.MessageID = "0"
	checkDurableAck(t, st, ack)
	ack.Stage = protocol.StageAgentAcknowledged
	checkDurableAck(t, st, ack)
	persistDurableReview(t, st, msg)
}

func TestDurableMigrationDoesNotReplayLegacyHistory(t *testing.T) {
	path := t.TempDir() + "/migration.db"
	st := openReceiptStore(t, path)
	persistReview(t, st)
	st.Close()
	st = openReceiptStore(t, path)
	msgs, err := st.ClaimPending(context.Background(), protocol.Recipient{AgentID: "codex", SessionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatal("legacy history silently enrolled")
	}
	assertTableCount(t, st, "messages", 1)
}

func TestDurableAckFailureRetainsPending(t *testing.T) {
	st := openTemp(t)
	persistDurableReview(t, st, durableReview("review"))
	checkDurableAck(t, st, durableAck("old", protocol.StageAdapterReceived))
	_, err := st.db.Exec(`CREATE TRIGGER reject_ack BEFORE UPDATE OF agent_acknowledged_at ON message_receipts BEGIN SELECT RAISE(ABORT, 'storage failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Acknowledge(context.Background(), durableAck("old", protocol.StageAgentAcknowledged)); err == nil {
		t.Fatal("failed acknowledgment succeeded")
	}
	if len(claimReview(t, st, "new")) != 1 {
		t.Fatal("failed acknowledgment cleared pending work")
	}
}

func TestDurableByteAndRetentionLimits(t *testing.T) {
	cases := map[string]string{
		"inbox bytes":    fmt.Sprintf(`UPDATE durable_messages SET wire_bytes = %d`, MaxPendingBytes),
		"global bytes":   fmt.Sprintf(`UPDATE durable_messages SET wire_bytes = %d`, MaxGlobalPendingBytes),
		"retained bytes": fmt.Sprintf(`UPDATE durable_messages SET storage_bytes = %d`, MaxRetainedBytes),
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			st := openTemp(t)
			persistDurableReview(t, st, durableReview("review"))
			if _, err := st.db.Exec(setup); err != nil {
				t.Fatal(err)
			}
			msg := durableReview("new")
			msg.Seq = 2
			err := st.PersistMessage(context.Background(), msg, msg.SessionID, []protocol.Recipient{{AgentID: "recipient", SessionID: "old"}, {AgentID: "other", SessionID: "other"}})
			if !errors.Is(err, ErrInboxFull) {
				t.Fatalf("limit not enforced: %v", err)
			}
			assertTableCount(t, st, "messages", 1)
			assertTableCount(t, st, "durable_inbox", 1)
		})
	}
}

func TestDurableEncodedBytesAreCharged(t *testing.T) {
	st := openTemp(t)
	msg := durableReview("large")
	msg.Payload = []byte(`"` + strings.Repeat("x", MaxPendingBytes) + `"`)
	err := st.PersistMessage(context.Background(), msg, msg.SessionID, []protocol.Recipient{{AgentID: "recipient"}})
	if !errors.Is(err, ErrInboxFull) {
		t.Fatalf("oversized durable inbox accepted: %v", err)
	}
	assertTableCount(t, st, "messages", 0)
}

func TestLegacyOwnerCannotAcknowledgeDurableMessage(t *testing.T) {
	st := openTemp(t)
	persistDurableReview(t, st, durableReview("review"))
	claimReview(t, st, "new")
	ack := durableAck("new", protocol.StageAdapterReceived)
	ack.ProtocolVersion = 2
	if _, err := st.Acknowledge(context.Background(), ack); !errors.Is(err, ErrInvalidAck) {
		t.Fatalf("legacy session cleared durable state: %v", err)
	}
	assertTableCount(t, st, "durable_inbox", 1)
}
