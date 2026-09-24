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
	assertEqual(t, "accepted", next(t, sender).Stage, protocol.StageAccepted)
	assertEqual(t, "received", next(t, sender).Stage, protocol.StageAdapterReceived)
	for range 3 {
		refreshBroker(t, m)
	}
	assertEqual(t, "owner count", *m.snapshot.ConnectedSessions, 2)
	assertEqual(t, "session count", len(m.snapshot.Sessions), 2)
	assertEqual(t, "receiver identity", m.snapshot.Sessions[1].SessionID, owner)
	assertEqual(t, "pending preserved", m.snapshot.DurablePending.Total, 1)
	message := next(t, receiver)
	assertEqual(t, "recipient retained message", message.MessageID, "request")
	assertEqual(t, "recipient frame type", message.Type, protocol.TypeMsg)
	press(m, "q")
	_, err = sender.SendMessage(context.Background(), bridge.SendRequest{To: "codex", Text: "after UI exit", MessageID: "second"})
	must(t, err)
	assertEqual(t, "routing after exit", next(t, sender).Stage, protocol.StageAccepted)
	// Receipt events can precede the next message in the recipient's inbox.
	var second protocol.Message
	for range 3 {
		second = next(t, receiver)
		if second.Type == protocol.TypeMsg {
			break
		}
	}
	assertEqual(t, "second message", second.MessageID, "second")
	assertEqual(t, "message sequence unaffected", second.Seq, message.Seq+1)
}
