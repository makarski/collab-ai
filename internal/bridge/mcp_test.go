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

func connectMCP(t *testing.T, c messagingClient) *mcp.ClientSession {
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

// awaitMCPFrame consumes a single frame at a time so later stages stay observable.
func awaitMCPFrame(t *testing.T, c *mcp.ClientSession) protocol.Message {
	t.Helper()
	out := inboxResult(t, call(t, c, "wait", map[string]any{"timeout_seconds": 1, "limit": 1}))
	if !out.Connected || len(out.Messages) != 1 {
		t.Fatalf("expected one frame: %+v", out)
	}
	return out.Messages[0]
}

func assertStage(t *testing.T, msg protocol.Message, id, stage string) {
	t.Helper()
	if msg.Type != protocol.TypeAck || msg.MessageID != id || msg.Stage != stage {
		t.Fatalf("expected %s for %s: %+v", stage, id, msg)
	}
}

func TestMCPExchange(t *testing.T) {
	path, _ := startBroker(t)
	codex := connectMCP(t, lazyAgent(t, path, "codex"))
	claude := connectMCP(t, lazyAgent(t, path, "claude"))
	listed, err := codex.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	if len(names) != 4 || !names["send"] || !names["receive"] || !names["wait"] || !names["acknowledge"] {
		t.Fatalf("unexpected tools: %v", names)
	}
	registered := inboxResult(t, call(t, codex, "receive", map[string]any{}))
	if registered.SessionID == "" || !registered.AcknowledgmentsSupported {
		t.Fatalf("missing capabilities: %+v", registered)
	}
	sent := call(t, claude, "send", map[string]any{"to": "codex", "text": "review ready"})
	data, _ := json.Marshal(sent.StructuredContent)
	var result SendResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.MessageID == "" || result.Status != "written" || result.DeliveryConfirmed {
		t.Fatalf("send result: %s", data)
	}
	accepted := awaitMCPFrame(t, claude)
	assertStage(t, accepted, result.MessageID, protocol.StageAccepted)
	if len(accepted.Recipients) != 1 || accepted.Recipients[0].AgentID != "codex" || accepted.Recipients[0].SessionID != registered.SessionID {
		t.Fatalf("missing recipient ownership: %+v", accepted)
	}
	request := awaitMCPFrame(t, codex)
	if request.Type != protocol.TypeMsg || request.From != "claude" || request.MessageID != result.MessageID || string(request.Payload) != `{"text":"review ready"}` {
		t.Fatalf("bad review request: %+v", request)
	}
	assertStage(t, awaitMCPFrame(t, claude), result.MessageID, protocol.StageAdapterReceived)
	// Transport receipt must never turn into an automatic agent acknowledgment.
	empty := inboxResult(t, call(t, claude, "wait", map[string]any{"timeout_seconds": 1}))
	if len(empty.Messages) != 0 || !empty.TimedOut {
		t.Fatalf("invented agent acknowledgment: %+v", empty)
	}
	call(t, codex, "acknowledge", map[string]any{"message_id": result.MessageID})
	assertStage(t, awaitMCPFrame(t, codex), result.MessageID, protocol.StageAgentAcknowledged)
	assertStage(t, awaitMCPFrame(t, claude), result.MessageID, protocol.StageAgentAcknowledged)
	call(t, codex, "send", map[string]any{"to": "claude", "text": "reviewed: add a disconnect test", "message_id": "review-reply", "in_reply_to": result.MessageID})
	assertStage(t, awaitMCPFrame(t, codex), "review-reply", protocol.StageAccepted)
	reply := awaitMCPFrame(t, claude)
	if reply.InReplyTo != result.MessageID || reply.MessageID != "review-reply" || reply.From != "codex" {
		t.Fatalf("reply correlation lost: %+v", reply)
	}
	assertStage(t, awaitMCPFrame(t, codex), "review-reply", protocol.StageAdapterReceived)
	call(t, claude, "acknowledge", map[string]any{"message_id": "review-reply"})
	assertStage(t, awaitMCPFrame(t, claude), "review-reply", protocol.StageAgentAcknowledged)
	assertStage(t, awaitMCPFrame(t, codex), "review-reply", protocol.StageAgentAcknowledged)
	call(t, claude, "send", map[string]any{"to": "missing", "text": "hello", "message_id": "missing-request"})
	routingError := awaitMCPFrame(t, claude)
	if routingError.Code != protocol.ErrUnknownRecipient || routingError.MessageID != "missing-request" {
		t.Fatalf("routing error lost: %+v", routingError)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"send", map[string]any{"to": "codex"}},
		{"receive", map[string]any{"limit": 101}},
		{"wait", map[string]any{"timeout_seconds": 31}},
		{"acknowledge", map[string]any{"message_id": ""}},
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
