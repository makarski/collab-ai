package host

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"collab-ai/internal/bridge"
	"collab-ai/internal/protocol"
)

// This fixture observes the real adapter connection while controlling broker
// frames. Persistence/receipt semantics are exercised by the bridge suite.
func lifecycleProxy(t *testing.T) (*Proxy, chan Frame, chan Frame, net.Listener) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "collab-host-")
	check(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "broker.sock")
	ln, err := net.Listen("unix", path)
	check(t, err)
	t.Cleanup(func() { ln.Close() })
	p, up, down := proxyFixture(t)
	lazy, err := bridge.NewLazyClient(bridge.ClientConfig{AgentID: "worker", SocketPath: path})
	check(t, err)
	p.Listener = bridge.NewListener(context.Background(), lazy, p)
	t.Cleanup(p.Listener.Close)
	return p, up, down, ln
}

func acceptOwner(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()
	check(t, ln.(*net.UnixListener).SetDeadline(time.Now().Add(2*time.Second)))
	conn, err := ln.Accept()
	check(t, err)
	t.Cleanup(func() { conn.Close() })
	check(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	var hello protocol.Message
	check(t, protocol.ReadFrame(bufio.NewReader(conn), &hello))
	if hello.Type != protocol.TypeHello || hello.AgentID != "worker" {
		t.Fatalf("unexpected owner: %+v", hello)
	}
	check(t, protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeWelcome, AgentID: "worker", SessionID: "broker-session", ProtocolVersion: protocol.Version}))
	return conn
}

func TestManagedThreadAutomaticallyListensAndSurvivesTurnBoundaries(t *testing.T) {
	p, up, down, ln := lifecycleProxy(t)
	check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`1`), Method: "thread/start", Params: json.RawMessage(`{}`)}))
	frameAt(t, up)
	if p.Listener.Status().State != "inactive" {
		t.Fatal("listener started before successful thread response")
	}
	done := make(chan error, 1)
	go func() {
		done <- p.FromHost(context.Background(), Frame{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"thread":{"id":"owned-thread"}}`)})
	}()
	// The operator sees thread creation before any automatic replay can start work.
	if string(frameAt(t, down).ID) != "1" {
		t.Fatal("thread creation response lost")
	}
	conn := acceptOwner(t, ln)
	check(t, <-done)
	for _, event := range []string{"turn/completed", "thread/compacted", "turn/started"} {
		check(t, p.FromHost(context.Background(), Frame{Method: event, Params: json.RawMessage(`{"threadId":"owned-thread"}`)}))
		frameAt(t, down)
		check(t, protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeMsg, From: "peer", MessageID: event}))
		request := frameAt(t, up)
		if request.Method != "turn/start" {
			t.Fatalf("no automatic delivery after %s: %+v", event, request)
		}
		check(t, p.FromHost(context.Background(), Frame{ID: request.ID, Result: json.RawMessage(`{"turn":{"id":"notification-turn"}}`)}))
		if p.Listener.Status().SessionID != "broker-session" {
			t.Fatal("turn boundary changed listener ownership")
		}
	}
}

func TestManagedThreadFailureCannotActivateAndCanRetry(t *testing.T) {
	p, up, down, ln := lifecycleProxy(t)
	check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`1`), Method: "thread/start", Params: json.RawMessage(`{}`)}))
	frameAt(t, up)
	check(t, p.FromHost(context.Background(), Frame{ID: json.RawMessage(`1`), Error: json.RawMessage(`{"code":-1,"message":"failed"}`)}))
	frameAt(t, down)
	check(t, p.FromHost(context.Background(), Frame{ID: json.RawMessage(`2`), Result: json.RawMessage(`{"thread":{"id":"foreign-thread"}}`)}))
	frameAt(t, down)
	if p.Listener.Status().State != "inactive" || p.ThreadID() != "" {
		t.Fatal("failed or foreign response activated listener")
	}
	done := make(chan error, 1)
	go func() {
		check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`3`), Method: "thread/start", Params: json.RawMessage(`{}`)}))
		done <- p.FromHost(context.Background(), Frame{ID: json.RawMessage(`3`), Result: json.RawMessage(`{"thread":{"id":"owned-thread"}}`)})
	}()
	frameAt(t, up)
	frameAt(t, down)
	acceptOwner(t, ln)
	check(t, <-done)
	if p.Listener.Status().State != "listening_delivery_unconfirmed" {
		t.Fatal("retry did not activate")
	}
}

func TestAutomaticStartupFailureIsReturned(t *testing.T) {
	p, up, down := proxyFixture(t)
	lazy, err := bridge.NewLazyClient(bridge.ClientConfig{AgentID: "worker", SocketPath: "/unused/missing-broker"})
	check(t, err)
	p.Listener = bridge.NewListener(context.Background(), lazy, p)
	t.Cleanup(p.Listener.Close)
	check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`1`), Method: "thread/start", Params: json.RawMessage(`{}`)}))
	frameAt(t, up)
	err = p.FromHost(context.Background(), Frame{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"thread":{"id":"owned-thread"}}`)})
	frameAt(t, down)
	if err == nil || p.Listener.Status().Error == "" {
		t.Fatal("failed automatic startup was silent")
	}
}
