//go:build linux

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"collab-ai/internal/hub"
	"collab-ai/internal/protocol"
)

// Keep the real writer and ownership lifecycle, but shorten only its write
// deadline. net.Pipe gives deterministic backpressure without filling OS buffers.
type sessionDeadlineConn struct{ net.Conn }

func (c sessionDeadlineConn) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		deadline = time.Now().Add(100 * time.Millisecond)
	}
	return c.Conn.SetWriteDeadline(deadline)
}

func sessionPipe(f *operatorFixture) (*sessionProbe, <-chan struct{}) {
	f.t.Helper()
	server, client := net.Pipe()
	f.t.Cleanup(func() { server.Close(); client.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	f.t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		f.endpoint.runSession(ctx, operatorSessionPeer{conn: sessionDeadlineConn{server}, r: bufio.NewReader(server), uid: os.Getuid()}, protocol.Message{
			Type: protocol.TypeOperatorSessionOpen, RequestID: "open",
		})
	}()
	p := &sessionProbe{operatorPeer: &operatorPeer{Conn: client, r: bufio.NewReader(client), t: f.t}}
	operatorEqual(f.t, "pipe session opened", p.frame().text("type"), "operator_session")
	return p, done
}

func sessionAwaitTransport(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session transport did not stop")
	}
}

func TestOperatorSessionStalledWriterDisconnectsAndReleases(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 5)
	p, done := sessionPipe(f)
	// The snapshot response blocks because the pipe's operator end never reads.
	p.send(map[string]any{"type": "operator_snapshot", "request_id": "blocked"})
	// Hub routing must stay available while that response is blocked.
	requireOperatorAccepted(t, f.send(operatorRequest("while-stalled", "alpha")), "alpha")
	a.read()
	sessionAwaitTransport(t, done)
	sessionAcquireAfterClose(f)
}

func TestOperatorSessionStalledOpenResponseReleasesOwnership(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	server, client := net.Pipe()
	done := make(chan struct{})
	t.Cleanup(func() {
		server.Close()
		client.Close()
		sessionAwaitTransport(t, done)
	})
	go func() {
		defer close(done)
		defer server.Close()
		f.endpoint.runSession(context.Background(), operatorSessionPeer{conn: sessionDeadlineConn{server}, r: bufio.NewReader(server), uid: os.Getuid()}, protocol.Message{
			Type: protocol.TypeOperatorSessionOpen, RequestID: "unread-open",
		})
	}()
	// The caller never reads even the opening response. Its write deadline
	// must close the session, rather than enter an idle read with ownership.
	sessionAwaitTransport(t, done)
	sessionAcquireAfterClose(f)
}

func TestOperatorSessionIdleOwnerDoesNotExpireOnWriteDeadline(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p, done := sessionPipe(f)
	p.snapshot()
	// There is no pending write. Passing the transport's shortened deadline
	// must neither close its reader nor release inbox ownership.
	time.Sleep(150 * time.Millisecond)
	_, busy := sessionConnect(f)
	operatorEqual(t, "idle owner retained", busy.text("code"), "owner_busy")
	p.snapshot()
	p.Close()
	sessionAwaitTransport(t, done)
}

func TestOperatorSessionReaderRejectsDuplicateAndNinthInFlight(t *testing.T) {
	for _, duplicate := range []bool{true, false} {
		t.Run(fmt.Sprintf("duplicate_%t", duplicate), func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			sess := hub.NewOperatorSession(os.Getuid())
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			opened := f.endpoint.hub.OpenOperatorSession(ctx, protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open"}, sess)
			operatorEqual(t, "reader session open", opened.Type, protocol.TypeOperatorSession)
			defer f.endpoint.hub.CloseOperatorSession(sess)
			input := sessionInflightInput(t, duplicate)
			// No writer runs, so all accepted IDs remain in flight even after
			// the hub has produced their responses.
			f.endpoint.readSession(ctx, bufio.NewReader(input), sess)
			sess.Finish()
			violations := sessionDrainViolations(ctx, sess)
			operatorEqual(t, "one terminal in-flight error", violations, 1)
			operatorNoError(t, ctx.Err())
		})
	}
}

func sessionInflightInput(t *testing.T, duplicate bool) *bytes.Buffer {
	t.Helper()
	ids := []string{"duplicate", "duplicate"}
	if !duplicate {
		ids = make([]string, 9)
		for i := range ids {
			ids[i] = fmt.Sprintf("request-%d", i)
		}
	}
	var input bytes.Buffer
	for _, id := range ids {
		operatorNoError(t, json.NewEncoder(&input).Encode(map[string]string{"type": "unknown-op", "request_id": id}))
	}
	return &input
}

func sessionDrainViolations(ctx context.Context, sess *hub.OperatorSession) int {
	var violations int
	for {
		m, ok := sess.Next(ctx)
		if !ok {
			return violations
		}
		if m.Code == protocol.ErrMalformedFrame {
			violations++
		}
	}
}
