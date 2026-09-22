package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

func runningStatusHub(t *testing.T) *Hub {
	t.Helper()
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); <-h.Done() })
	return h
}

func hubStatus(t *testing.T, h *Hub) protocol.StatusSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := h.Status(ctx)
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

func assertLiveStatus(t *testing.T, out protocol.StatusSnapshot) {
	t.Helper()
	assertStatusEqual(t, "connected count", *out.ConnectedSessions, 2)
	assertStatusEqual(t, "session count", len(out.Sessions), 2)
	assertStatusEqual(t, "pending deliveries", out.DurablePending.Total, 1)
	assertStatusEqual(t, "legacy count", out.LegacyUnacknowledged, nil)
	for _, session := range out.Sessions {
		assertStatusEqual(t, "live session state", session.State, "transport_connected")
		if session.LastSeenAt == nil {
			t.Fatalf("last seen missing: %+v", session)
		}
	}
}

func TestStatusDoesNotRegisterConsumeOrAcknowledge(t *testing.T) {
	h := runningStatusHub(t)
	ctx := context.Background()
	empty := hubStatus(t, h)
	assertStatusEqual(t, "empty health", empty.Health, "ready")
	assertStatusEqual(t, "empty connected count", *empty.ConnectedSessions, 0)
	assertStatusEqual(t, "empty pending count", empty.DurablePending.Total, 0)
	assertStatusEqual(t, "empty session count", len(empty.Sessions), 0)
	a, b := newClient("a"), newClient("b")
	a.ProtocolVersion, b.ProtocolVersion = protocol.Version, protocol.Version
	h.Register(ctx, a)
	h.Register(ctx, b)
	first, second := recv(t, a), recv(t, b)
	assertStatusEqual(t, "first welcome sequence", first.Seq, 1)
	assertStatusEqual(t, "second welcome sequence", second.Seq, 2)
	msg := protocol.Message{Type: protocol.TypeMsg, MessageID: "pending", To: "b", Payload: []byte(`{"text":"private"}`), AckRequested: true, Durable: true}
	h.Submit(ctx, Inbound{From: a, Msg: msg})
	accepted := recv(t, a)
	assertStatusEqual(t, "message sequence", accepted.Seq, 3)
	for range 3 {
		assertLiveStatus(t, hubStatus(t, h))
	}
	assertStatusEqual(t, "recipient queue length", len(b.Send), 1)
	assertStatusEqual(t, "sender queue length", len(a.Send), 0)
	assertStatusEqual(t, "delivered message ID", recv(t, b).MessageID, msg.MessageID)
	h.Submit(ctx, Inbound{From: b, Msg: protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: protocol.StageAdapterReceived}})
	recv(t, a)
	assertStatusEqual(t, "pending after adapter receipt", hubStatus(t, h).DurablePending.Total, 1)
	h.Submit(ctx, Inbound{From: b, Msg: protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: protocol.StageAgentAcknowledged}})
	recv(t, a)
	recv(t, b)
	assertStatusEqual(t, "pending after acknowledgment", hubStatus(t, h).DurablePending.Total, 0)
}

func TestStatusDistinguishesStaleDisconnectedAndReplacementSessions(t *testing.T) {
	h := runningStatusHub(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	for _, id := range []string{"unclean", "closed"} {
		if err := h.store.RecordConnect(ctx, store.SessionRecord{Identity: protocol.Recipient{AgentID: "worker", SessionID: id}, ConnectedAt: old}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.RecordDisconnect(ctx, "closed", old.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	c := newClient("worker")
	h.Register(ctx, c)
	recv(t, c)
	out := hubStatus(t, h)
	assertStatusEqual(t, "connected count", *out.ConnectedSessions, 1)
	assertStatusEqual(t, "session count", len(out.Sessions), 3)
	states := map[string]string{}
	for _, s := range out.Sessions {
		states[s.SessionID] = s.State
	}
	assertStatusEqual(t, "unclean session state", states["unclean"], "stale")
	assertStatusEqual(t, "closed session state", states["closed"], "disconnected")
	assertStatusEqual(t, "current session state", states[c.SessionID], "transport_connected")
	h.Unregister(c)
	out = hubStatus(t, h)
	assertStatusEqual(t, "connected after disconnect", *out.ConnectedSessions, 0)
	assertStatusEqual(t, "disconnected session state", out.Sessions[0].State, "disconnected")
}

func TestStatusPrioritizesLiveSessionsAndSignalsTruncation(t *testing.T) {
	h := runningStatusHub(t)
	ctx := context.Background()
	c := newClient("live")
	h.Register(ctx, c)
	recv(t, c)
	for i := range protocol.StatusLimit + 1 {
		if err := h.store.RecordConnect(ctx, store.SessionRecord{Identity: protocol.Recipient{AgentID: "old", SessionID: fmt.Sprint(i)}, ConnectedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	out := hubStatus(t, h)
	assertStatusEqual(t, "session limit", len(out.Sessions), protocol.StatusLimit)
	assertStatusEqual(t, "sessions truncated", out.SessionsTruncated, true)
	assertStatusEqual(t, "first session", out.Sessions[0].SessionID, c.SessionID)
}

func TestStatusStorageFailurePreservesConnectivityAndUnknownCounts(t *testing.T) {
	h := runningStatusHub(t)
	h.store.Close()
	out := hubStatus(t, h)
	assertStatusEqual(t, "health with closed storage", out.Health, "degraded")
	assertStatusEqual(t, "reachable with closed storage", out.Reachable, true)
	if out.ConnectedSessions == nil {
		t.Fatal("closed storage hid known connected count")
	}
	assertStatusEqual(t, "pending with closed storage", out.DurablePending, nil)
	assertStatusEqual(t, "history with closed storage", out.HistoryAvailable, false)
}

func TestStatusCancellationAndShutdown(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Status(ctx); err == nil {
		t.Fatal("cancelled inspection waited for inactive hub")
	}
	go h.Run(ctx)
	<-h.Done()
	if _, err := h.Status(context.Background()); err == nil {
		t.Fatal("stopped broker returned status")
	}
}
