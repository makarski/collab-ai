package store

import (
	"context"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestLastSeqResumesAcrossOpen(t *testing.T) {
	path := t.TempDir() + "/test.db"
	ctx := context.Background()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for seq := uint64(1); seq <= 3; seq++ {
		if err := st.InsertMessage(ctx, seq, time.Now(), "a", "*", []byte(`{}`)); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	got, err := st2.LastSeq(ctx)
	if err != nil {
		t.Fatalf("LastSeq: %v", err)
	}
	if got != 3 {
		t.Fatalf("LastSeq = %d, want 3", got)
	}
}

func TestConnectAndDisconnect(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	if err := st.RecordConnect(ctx, "s1", "alice", "harness", "model", time.Now()); err != nil {
		t.Fatalf("RecordConnect: %v", err)
	}
	if err := st.RecordDisconnect(ctx, "s1", time.Now()); err != nil {
		t.Fatalf("RecordDisconnect: %v", err)
	}

	var nullCount int
	err := st.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE disconnected_at IS NULL`).Scan(&nullCount)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if nullCount != 0 {
		t.Fatalf("expected all sessions disconnected, got %d open", nullCount)
	}
}

func TestEmptyLastSeqIsZero(t *testing.T) {
	st := openTemp(t)
	got, err := st.LastSeq(context.Background())
	if err != nil {
		t.Fatalf("LastSeq: %v", err)
	}
	if got != 0 {
		t.Fatalf("LastSeq = %d, want 0", got)
	}
}
