package chat_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
)

func chatSocket(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chat.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		serve(conn)
	}()
	return path
}

func chatHandshake() protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorSession, RequestID: "open", Session: &protocol.SessionInfo{SessionID: "owner", Epoch: "epoch", OpenedAt: time.Now(), Capabilities: append([]string(nil), protocol.SessionCapabilities...)}}
}

func TestChatHandshakeRejectsMalformedOrUncorrelatedResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*protocol.Message)
	}{
		{"wrong request ID", func(m *protocol.Message) { m.RequestID = "another-open" }},
		{"missing request ID", func(m *protocol.Message) { m.RequestID = "" }},
		{"empty session ID", func(m *protocol.Message) { m.Session.SessionID = "" }},
		{"empty epoch", func(m *protocol.Message) { m.Session.Epoch = "" }},
		{"wrong type", func(m *protocol.Message) { m.Type = protocol.TypeAck }},
		{"missing session", func(m *protocol.Message) { m.Session = nil }},
		{"busy on wrong type", func(m *protocol.Message) {
			m.Code = protocol.ErrOwnerBusy
			m.Owner = &protocol.SessionInfo{SessionID: "other"}
			m.Type = protocol.TypeMsg
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := chatHandshake()
			tc.change(&response)
			path := chatSocket(t, func(c net.Conn) {
				var req protocol.Message
				if protocol.ReadFrame(bufio.NewReader(c), &req) != nil {
					return
				}
				protocol.WriteFrame(c, response)
			})
			tr, _, err := chat.SocketDialer(path, time.Second)(context.Background())
			if tr != nil {
				tr.Close()
			}
			if !errors.Is(err, chat.ErrMalformed) {
				t.Fatalf("expected malformed classification, got transport=%T err=%v", tr, err)
			}
		})
	}
}

func TestChatHandshakeRequiresEveryReadCapability(t *testing.T) {
	for _, missing := range []string{"events", "snapshot", "history", "message_stages"} {
		t.Run(missing, func(t *testing.T) {
			response := chatHandshake()
			response.Session.Capabilities = nil
			for _, cap := range protocol.SessionCapabilities {
				if cap != missing {
					response.Session.Capabilities = append(response.Session.Capabilities, cap)
				}
			}
			path := chatSocket(t, func(c net.Conn) {
				var req protocol.Message
				if protocol.ReadFrame(bufio.NewReader(c), &req) == nil {
					protocol.WriteFrame(c, response)
				}
			})
			tr, _, err := chat.SocketDialer(path, time.Second)(context.Background())
			if tr != nil {
				tr.Close()
			}
			if err == nil {
				t.Fatalf("handshake accepted without %s", missing)
			}
		})
	}
}

func TestChatTransportPreservesModelRequestIDAndClearsOpenDeadline(t *testing.T) {
	written := make(chan protocol.Message, 1)
	path := chatSocket(t, func(c net.Conn) {
		r := bufio.NewReader(c)
		var req protocol.Message
		if protocol.ReadFrame(r, &req) != nil {
			return
		}
		protocol.WriteFrame(c, chatHandshake())
		if protocol.ReadFrame(r, &req) != nil {
			return
		}
		written <- req
		// A response after the handshake timeout is still valid on an idle session.
		time.Sleep(100 * time.Millisecond)
		protocol.WriteFrame(c, protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: req.RequestID, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch"}}})
	})
	tr, _, err := chat.SocketDialer(path, 50*time.Millisecond)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	id, err := tr.Request(protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: "model-q7"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "model-q7" {
		t.Fatalf("request ID replaced: %q", id)
	}
	select {
	case req := <-written:
		if req.RequestID != id {
			t.Fatalf("wire ID %q != returned ID %q", req.RequestID, id)
		}
	case <-time.After(time.Second):
		t.Fatal("request not written")
	}
	select {
	case frame, ok := <-tr.Incoming():
		if !ok || frame.RequestID != id {
			t.Fatalf("idle connection lost: open=%v frame=%+v", ok, frame)
		}
	case <-time.After(time.Second):
		t.Fatal("response not delivered")
	}
}
