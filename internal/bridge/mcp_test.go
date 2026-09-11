package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/hub"
	"collab-ai/internal/protocol"
	"collab-ai/internal/server"
	"collab-ai/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func startBroker(t *testing.T) (string, context.CancelFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "collab-mcp-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "broker.sock")
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, 0, log)
	go h.Run(ctx)
	srv := server.New(path, h, log)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Listen(ctx); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("server shutdown timed out")
		}
		select {
		case <-h.Done():
		case <-time.After(3 * time.Second):
			t.Error("hub shutdown timed out")
		}
		st.Close()
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return path, cancel
}

func connectAgent(t *testing.T, path, id string) *Client {
	t.Helper()
	c, err := Dial(context.Background(), path, id, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func connectMCP(t *testing.T, c *Client) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := NewMCP(c).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, c *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := c.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s failed: %+v", name, result.Content)
	}
	return result
}

func inboxResult(t *testing.T, r *mcp.CallToolResult) Inbox {
	t.Helper()
	data, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out Inbox
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCPExchange(t *testing.T) {
	path, _ := startBroker(t)
	codex := connectMCP(t, connectAgent(t, path, "codex"))
	claude := connectMCP(t, connectAgent(t, path, "claude"))
	listed, err := codex.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	if len(names) != 3 || !names["send"] || !names["receive"] || !names["wait"] {
		t.Fatalf("unexpected tools: %v", names)
	}
	// Start a real MCP wait before sending via the other MCP connection.
	waiting := make(chan *mcp.CallToolResult, 1)
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		r, err := codex.CallTool(waitCtx, &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{"timeout_seconds": 1}})
		if err != nil {
			t.Error(err)
		}
		waiting <- r
	}()
	sent := call(t, claude, "send", map[string]any{"to": "codex", "text": "review ready"})
	data, _ := json.Marshal(sent.StructuredContent)
	if !strings.Contains(string(data), `"delivery_confirmed":false`) {
		t.Fatalf("send overclaims delivery: %s", data)
	}
	result := <-waiting
	if result == nil || result.IsError {
		t.Fatal("wait failed")
	}
	inbox := inboxResult(t, result)
	if len(inbox.Messages) != 1 || inbox.Messages[0].From != "claude" || string(inbox.Messages[0].Payload) != `{"text":"review ready"}` {
		t.Fatalf("unexpected inbox: %+v", inbox)
	}
	empty := inboxResult(t, call(t, codex, "receive", map[string]any{}))
	if !empty.Connected || len(empty.Messages) != 0 {
		t.Fatalf("receive did not consume messages: %+v", empty)
	}
	call(t, claude, "send", map[string]any{"to": "*", "text": "broadcast"})
	broadcast := inboxResult(t, call(t, codex, "wait", map[string]any{"timeout_seconds": 1}))
	if len(broadcast.Messages) != 1 || broadcast.Messages[0].To != "*" {
		t.Fatalf("bad broadcast: %+v", broadcast)
	}
	if self := inboxResult(t, call(t, claude, "receive", map[string]any{})); len(self.Messages) != 0 {
		t.Fatal("sender received its own broadcast")
	}
	call(t, claude, "send", map[string]any{"to": "missing", "text": "hello"})
	routingError := inboxResult(t, call(t, claude, "wait", map[string]any{"timeout_seconds": 1}))
	if len(routingError.Messages) != 1 || routingError.Messages[0].Code != protocol.ErrUnknownRecipient {
		t.Fatalf("routing error lost: %+v", routingError)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"send", map[string]any{"to": "codex"}},
		{"receive", map[string]any{"limit": 101}},
		{"wait", map[string]any{"timeout_seconds": 31}},
	} {
		r, err := codex.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err == nil && !r.IsError {
			t.Fatalf("accepted invalid arguments for %s", tc.name)
		}
	}
}

func TestReceiveTimeoutCancellationAndDisconnect(t *testing.T) {
	path, stop := startBroker(t)
	c := connectAgent(t, path, "codex")
	out, err := c.Receive(context.Background(), 20, 10*time.Millisecond)
	if err != nil || !out.TimedOut || !out.Connected {
		t.Fatalf("bad timeout: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Receive(ctx, 20, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if err := c.Send(context.Background(), "codex", "buffered"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.notify:
	case <-time.After(time.Second):
		t.Fatal("socket reader did not queue message")
	}
	stop()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("disconnect not detected")
	}
	out, err = c.Receive(context.Background(), 20, time.Second)
	if err != nil || out.Connected || out.Error == "" || len(out.Messages) != 1 {
		t.Fatalf("buffered frame or disconnect status lost: %+v %v", out, err)
	}
	if err := c.Send(context.Background(), "claude", "late"); err == nil {
		t.Fatal("send succeeded after disconnect")
	}
}

func pipeClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	local, remote := net.Pipe()
	c := &Client{conn: local, writeGate: make(chan struct{}, 1), notify: make(chan struct{}, 1), done: make(chan struct{}), readDone: make(chan struct{})}
	go c.readLoop(bufio.NewReader(local))
	t.Cleanup(c.Close)
	t.Cleanup(func() { remote.Close() })
	return c, remote
}

func TestInboxOverflowIsBoundedAndReported(t *testing.T) {
	c, remote := pipeClient(t)
	payload, _ := json.Marshal(strings.Repeat("x", protocol.MaxFrameBytes-100))
	remote.SetWriteDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 5; i++ {
		if err := protocol.WriteFrame(remote, protocol.Message{Type: protocol.TypeMsg, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("overflow did not disconnect")
	}
	out, err := c.Receive(context.Background(), 20, 0)
	if err != nil || out.Connected || !strings.Contains(out.Error, "overflow") || len(out.Messages) != 4 {
		t.Fatalf("overflow was hidden: count=%d error=%s err=%v", len(out.Messages), out.Error, err)
	}
}

func TestCanceledSocketWriteReturns(t *testing.T) {
	c, _ := pipeClient(t) // Peer never reads our write.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Send(ctx, "claude", "hello"); err == nil {
		t.Fatal("blocked write unexpectedly succeeded")
	}
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("failed write left connection open")
	}
}
