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

func TestStatusDoesNotRegisterConsumeOrAcknowledge(t *testing.T) {
	h := runningStatusHub(t)
	ctx := context.Background()
	empty := hubStatus(t, h)
	if empty.Health != "ready" || *empty.ConnectedSessions != 0 || empty.DurablePending.Total != 0 || len(empty.Sessions) != 0 {
		t.Fatalf("empty snapshot: %+v", empty)
	}
	a, b := newClient("a"), newClient("b")
	a.ProtocolVersion, b.ProtocolVersion = protocol.Version, protocol.Version
	h.Register(ctx, a)
	h.Register(ctx, b)
	first, second := recv(t, a), recv(t, b)
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatal("status allocated a sequence")
	}
	msg := protocol.Message{Type: protocol.TypeMsg, MessageID: "pending", To: "b", Payload: []byte(`{"text":"private"}`), AckRequested: true, Durable: true}
	h.Submit(ctx, Inbound{From: a, Msg: msg})
	accepted := recv(t, a)
	if accepted.Seq != 3 {
		t.Fatal("status changed message sequence")
	}
	for range 3 {
		out := hubStatus(t, h)
		if *out.ConnectedSessions != 2 || len(out.Sessions) != 2 || out.DurablePending.Total != 1 {
			t.Fatalf("snapshot: %+v", out)
		}
		if out.LegacyUnacknowledged != nil {
			t.Fatal("unknown legacy count became zero")
		}
		for _, session := range out.Sessions {
			if session.State != "transport_connected" || session.LastSeenAt == nil {
				t.Fatalf("live state missing: %+v", session)
			}
		}
	}
	if len(b.Send) != 1 || len(a.Send) != 0 {
		t.Fatal("status consumed or generated messaging frames")
	}
	if recv(t, b).MessageID != msg.MessageID {
		t.Fatal("message lost")
	}
	h.Submit(ctx, Inbound{From: b, Msg: protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: protocol.StageAdapterReceived}})
	recv(t, a)
	if hubStatus(t, h).DurablePending.Total != 1 {
		t.Fatal("adapter receipt treated as acknowledgment")
	}
	h.Submit(ctx, Inbound{From: b, Msg: protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: protocol.StageAgentAcknowledged}})
	recv(t, a)
	recv(t, b)
	if hubStatus(t, h).DurablePending.Total != 0 {
		t.Fatal("committed acknowledgment not reflected")
	}
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
	if *out.ConnectedSessions != 1 || len(out.Sessions) != 3 {
		t.Fatalf("sessions: %+v", out)
	}
	states := map[string]string{}
	for _, s := range out.Sessions {
		states[s.SessionID] = s.State
	}
	if states["unclean"] != "stale" || states["closed"] != "disconnected" || states[c.SessionID] != "transport_connected" {
		t.Fatalf("states: %+v", states)
	}
	h.Unregister(c)
	out = hubStatus(t, h)
	if *out.ConnectedSessions != 0 || out.Sessions[0].State != "disconnected" {
		t.Fatalf("disconnect not observed: %+v", out)
	}
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
	if len(out.Sessions) != protocol.StatusLimit || !out.SessionsTruncated || out.Sessions[0].SessionID != c.SessionID {
		t.Fatalf("live session hidden by history: %+v", out)
	}
}

func TestStatusStorageFailurePreservesConnectivityAndUnknownCounts(t *testing.T) {
	h := runningStatusHub(t)
	h.store.Close()
	out := hubStatus(t, h)
	if out.Health != "degraded" || !out.Reachable || out.ConnectedSessions == nil || out.DurablePending != nil || out.HistoryAvailable {
		t.Fatalf("storage failure concealed: %+v", out)
	}
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
