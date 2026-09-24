package dashboard

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"collab-ai/internal/bridge"
	"collab-ai/internal/hub"
	"collab-ai/internal/protocol"
	"collab-ai/internal/server"
	"collab-ai/internal/status"
	"collab-ai/internal/store"
)

func realBroker(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "collab-dashboard-")
	must(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "state.db"))
	must(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, 0, log)
	go h.Run(ctx)
	path := filepath.Join(dir, "broker.sock")
	srv := server.New(path, h, log)
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Listen(ctx) }()
	t.Cleanup(func() { cancel(); srv.Close(); <-stopped; <-h.Done(); st.Close() })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := status.Fetch(ctx, path); err == nil {
			return path
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("broker did not start")
	return ""
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func testAgent(t *testing.T, path, id string) *bridge.Client {
	t.Helper()
	c, err := bridge.Dial(context.Background(), bridge.ClientConfig{SocketPath: path, AgentID: id})
	must(t, err)
	t.Cleanup(c.Close)
	return c
}
func next(t *testing.T, c *bridge.Client) protocol.Message {
	t.Helper()
	inbox, err := c.Receive(context.Background(), 1, time.Second)
	must(t, err)
	if len(inbox.Messages) != 1 {
		t.Fatal("message timed out")
	}
	return inbox.Messages[0]
}
func refreshBroker(t *testing.T, m *model) {
	t.Helper()
	result := m.refresh()()
	m.Update(result)
	if m.latest.Health != "ready" {
		t.Fatalf("broker not ready: %+v", m.latest)
	}
}

func TestDashboardRefreshLeavesRealBrokerOwnersAndMessagesIntact(t *testing.T) {
	path := realBroker(t)
	sender := testAgent(t, path, "claude")
	receiver := testAgent(t, path, "codex")
	m := fixture(t, status.Fetch)
	m.cfg.Socket = path
	refreshBroker(t, m)
	owner := m.snapshot.Sessions[1].SessionID
	_, err := sender.SendMessage(context.Background(), bridge.SendRequest{To: "codex", Text: "private review", MessageID: "request"})
	must(t, err)
	if next(t, sender).Stage != protocol.StageAccepted {
		t.Fatal("message not accepted")
	}
	if next(t, sender).Stage != protocol.StageAdapterReceived {
		t.Fatal("adapter did not receive")
	}
	for range 3 {
		refreshBroker(t, m)
	}
	if *m.snapshot.ConnectedSessions != 2 || len(m.snapshot.Sessions) != 2 || m.snapshot.Sessions[1].SessionID != owner {
		t.Fatal("dashboard changed ownership or registered an inspector")
	}
	if m.snapshot.DurablePending.Total != 1 {
		t.Fatal("dashboard acknowledged delivery")
	}
	message := next(t, receiver)
	if message.MessageID != "request" || message.Type != protocol.TypeMsg {
		t.Fatal("dashboard stole recipient message")
	}
	press(m, "q")
	_, err = sender.SendMessage(context.Background(), bridge.SendRequest{To: "codex", Text: "after UI exit", MessageID: "second"})
	must(t, err)
	if next(t, sender).Stage != protocol.StageAccepted {
		t.Fatal("UI exit stopped routing")
	}
	// Receipt events can precede the next message in the recipient's inbox.
	var second protocol.Message
	for range 3 {
		second = next(t, receiver)
		if second.Type == protocol.TypeMsg {
			break
		}
	}
	if second.MessageID != "second" || second.Seq != message.Seq+1 {
		t.Fatal("inspection allocated sequences or affected routing")
	}
}
