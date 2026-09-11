package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

func newTestHub(t *testing.T) *Hub {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(st, 0, log)
}

func newClient(id string) *Client {
	return &Client{ID: id, SessionID: id + "-session", Send: make(chan protocol.Message, 8)}
}

func recv(t *testing.T, c *Client) protocol.Message {
	t.Helper()
	select {
	case msg := <-c.Send:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
		return protocol.Message{}
	}
}

func TestBroadcastExcludesSender(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)

	a, b := newClient("alice"), newClient("bob")
	h.Register(ctx, a)
	h.Register(ctx, b)
	recv(t, a) // welcome
	recv(t, b) // welcome

	h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeMsg, To: protocol.Broadcast, Payload: json.RawMessage(`{"x":1}`)}})

	got := recv(t, b)
	if got.From != "alice" || got.To != protocol.Broadcast || got.Seq == 0 {
		t.Fatalf("bad broadcast frame: %+v", got)
	}

	select {
	case unexpected := <-a.Send:
		t.Fatalf("sender received own broadcast: %+v", unexpected)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDirectMessage(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)

	a, b := newClient("alice"), newClient("bob")
	h.Register(ctx, a)
	h.Register(ctx, b)
	recv(t, a)
	recv(t, b)

	h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeMsg, To: "bob", Payload: json.RawMessage(`{}`)}})

	got := recv(t, b)
	if got.To != "bob" || got.From != "alice" {
		t.Fatalf("bad direct frame: %+v", got)
	}
}

func TestUnknownRecipientSendsError(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)

	a := newClient("alice")
	h.Register(ctx, a)
	recv(t, a)

	h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeMsg, To: "ghost", Payload: json.RawMessage(`{}`)}})

	got := recv(t, a)
	if got.Type != protocol.TypeError || got.Code != protocol.ErrUnknownRecipient {
		t.Fatalf("expected unknown_recipient error, got %+v", got)
	}
}

func TestMissingRecipientSendsError(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)

	a := newClient("alice")
	h.Register(ctx, a)
	recv(t, a)

	h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeMsg, Payload: json.RawMessage(`{}`)}})

	got := recv(t, a)
	if got.Code != protocol.ErrMissingRecipient {
		t.Fatalf("expected missing_recipient error, got %+v", got)
	}
}

func TestSeqMonotonicAndResumed(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := New(st, 100, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)

	a := newClient("alice")
	h.Register(ctx, a)
	welcome := recv(t, a)
	if welcome.Seq != 101 {
		t.Fatalf("welcome seq = %d, want 101 (resumed from 100)", welcome.Seq)
	}

	h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeMsg, To: "x", Payload: json.RawMessage(`{}`)}})
	recv(t, a) // unknown_recipient error; seq was consumed by the failed route

	// two frames consumed -> last persisted seq must be 102
	last, err := st.LastSeq(context.Background())
	if err != nil {
		t.Fatalf("LastSeq: %v", err)
	}
	if last != 102 {
		t.Fatalf("LastSeq = %d, want 102", last)
	}
}

func TestReplacementRejectsStaleSubmissions(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	old, b := newClient("codex"), newClient("claude")
	disconnected := make(chan struct{})
	old.Disconnect = func() { close(disconnected) }
	h.Register(ctx, old)
	h.Register(ctx, b)
	recv(t, old)
	recv(t, b)
	// Even a full old queue cannot block replacement.
	for i := 0; i < cap(old.Send); i++ {
		old.Send <- protocol.Message{}
	}
	replacement := newClient("codex")
	replacement.SessionID = "replacement"
	h.Register(ctx, replacement)
	recv(t, replacement)
	select {
	case <-disconnected:
	default:
		t.Fatal("old transport not closed")
	}
	h.Submit(ctx, Inbound{From: old, Msg: protocol.Message{Type: protocol.TypeMsg}})
	h.Submit(ctx, Inbound{From: old, Msg: protocol.Message{Type: protocol.TypeMsg, To: "claude", Payload: json.RawMessage(`"stale"`)}})
	h.Unregister(old) // Must not unregister the new owner.
	h.Submit(ctx, Inbound{From: replacement, Msg: protocol.Message{Type: protocol.TypeMsg, To: "claude", Payload: json.RawMessage(`"current"`)}})
	if got := recv(t, b); string(got.Payload) != `"current"` {
		t.Fatalf("stale submission delivered: %+v", got)
	}
}

func TestSlowReaderDoesNotBlockOtherAgents(t *testing.T) {
	for _, to := range []string{"slow", protocol.Broadcast} {
		t.Run(to, func(t *testing.T) {
			h := newTestHub(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer func() { cancel(); <-h.Done() }()
			go h.Run(ctx)
			a, b, c := newClient("sender"), newClient("slow"), newClient("healthy")
			disconnected := make(chan struct{})
			b.Disconnect = func() { close(disconnected) }
			for _, client := range []*Client{a, b, c} {
				h.Register(ctx, client)
				recv(t, client)
			}
			for i := 0; i < cap(b.Send); i++ {
				b.Send <- protocol.Message{}
			}
			h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeMsg, To: to}})
			if to == "slow" {
				if got := recv(t, a); got.Code != protocol.ErrRecipientUnavailable {
					t.Fatalf("unexpected result: %+v", got)
				}
				h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeMsg, To: "healthy"}})
			}
			recv(t, c)
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("slow transport not closed")
			}
			d := newClient("newcomer")
			h.Register(ctx, d)
			recv(t, d)
		})
	}
}

func TestFullErrorQueueDisconnectsSender(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	a := newClient("sender")
	disconnected := make(chan struct{})
	a.Disconnect = func() { close(disconnected) }
	h.Register(ctx, a)
	recv(t, a)
	for i := 0; i < cap(a.Send); i++ {
		a.Send <- protocol.Message{}
	}
	h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeHello}})
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("error response blocked routing")
	}
}

func TestShutdownRecordsSessionsAndUnblocksAPIs(t *testing.T) {
	path := t.TempDir() + "/test.db"
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	a := newClient("codex")
	h.Register(ctx, a)
	recv(t, a)
	cancel()
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("hub failed to stop")
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if h.Register(context.Background(), newClient("late")) {
			t.Error("registered after shutdown")
		}
		if h.Submit(context.Background(), Inbound{From: a}) {
			t.Error("submitted after shutdown")
		}
		h.Unregister(a)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("API blocked after shutdown")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var open int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE disconnected_at IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("%d sessions left open", open)
	}
}

func TestBrokerMetadataCannotOverflowRecipientFrame(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	a, b := newClient(strings.Repeat("a", 200)), newClient("bob")
	h.Register(ctx, a)
	h.Register(ctx, b)
	recv(t, a)
	recv(t, b)
	payload, _ := json.Marshal(strings.Repeat("x", protocol.MaxFrameBytes-100))
	msg := protocol.Message{Type: protocol.TypeMsg, To: "bob", Payload: payload}
	encoded, _ := json.Marshal(msg)
	if len(encoded)+1 > protocol.MaxFrameBytes {
		t.Fatal("test input must fit the wire limit")
	}
	h.Submit(ctx, Inbound{From: a, Msg: msg})
	if got := recv(t, a); got.Code != protocol.ErrMalformedFrame {
		t.Fatalf("expected size error: %+v", got)
	}
	h.Submit(ctx, Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeMsg, To: "bob"}})
	if got := recv(t, b); len(got.Payload) != 0 {
		t.Fatal("oversized message reached recipient")
	}
}
