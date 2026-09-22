package server

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/hub"
	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

func statusConnection(t *testing.T, ctx context.Context, h *hub.Hub) net.Conn {
	t.Helper()
	local, remote := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer local.Close()
		New("", h, testLog()).writeStatus(ctx, local)
	}()
	t.Cleanup(func() { remote.Close(); local.Close(); <-done })
	remote.SetReadDeadline(time.Now().Add(5 * time.Second))
	return remote
}

func readStatusResponse(t *testing.T, conn net.Conn) protocol.StatusSnapshot {
	t.Helper()
	var reply protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(conn), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Type != protocol.TypeStatus || reply.Status == nil {
		t.Fatalf("invalid status response: %+v", reply)
	}
	return *reply.Status
}

func TestStatusWaitsForHubBeyondTwoSeconds(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := hub.New(st, 0, testLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := statusConnection(t, ctx, h)
	// Keep the hub unavailable past the old server cap, within a 5s client wait.
	time.Sleep(2100 * time.Millisecond)
	go h.Run(ctx)
	defer func() { cancel(); <-h.Done() }()
	out := readStatusResponse(t, conn)
	if out.Health != "ready" {
		t.Fatalf("server gave up before client deadline: %+v", out)
	}
}

func TestStatusPreservesHubFailureCause(t *testing.T) {
	for _, cause := range []string{"context canceled", "context deadline exceeded", "broker stopped"} {
		t.Run(cause, func(t *testing.T) {
			h := hub.New(nil, 0, testLog())
			ctx, cancel := failedStatusContext(cause, h)
			defer cancel()
			out := readStatusResponse(t, statusConnection(t, ctx, h))
			if out.Health != "unavailable" || !out.Reachable {
				t.Fatalf("incorrect availability: %+v", out)
			}
			if !strings.Contains(out.Error, cause) {
				t.Fatalf("error %q omitted %q", out.Error, cause)
			}
			if out.ConnectedSessions != nil || out.DurablePending != nil {
				t.Fatalf("failure invented counts: %+v", out)
			}
		})
	}
}

func failedStatusContext(cause string, h *hub.Hub) (context.Context, context.CancelFunc) {
	if cause == "context deadline exceeded" {
		return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if cause == "broker stopped" {
		h.Run(ctx)
		return context.WithCancel(context.Background())
	}
	return ctx, cancel
}
