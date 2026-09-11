package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"collab-ai/internal/hub"
	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "collab-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "broker.sock")
}

func TestListenPreservesExistingPaths(t *testing.T) {
	for _, kind := range []string{"file", "live socket", "stale socket", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := socketPath(t)
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing-target", path); err != nil {
					t.Fatal(err)
				}
			default:
				ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				ln.SetUnlinkOnClose(false)
				defer ln.Close()
				if kind == "stale socket" {
					ln.Close()
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			srv := New(path, nil, testLog())
			if err := srv.Listen(context.Background()); err == nil {
				t.Fatal("replaced existing path")
			}
			if err := srv.Close(); err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing path changed: %v", err)
			}
			if kind == "live socket" {
				c, err := net.DialTimeout("unix", path, time.Second)
				if err != nil {
					t.Fatal("original listener no longer reachable:", err)
				}
				c.Close()
			}
		})
	}
}

func startTestServer(t *testing.T) (*Server, context.CancelFunc, <-chan error, *hub.Hub) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := hub.New(st, 0, testLog())
	go h.Run(ctx)
	srv := New(socketPath(t), h, testLog())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() { defer close(stopped); done <- srv.Listen(ctx) }()
	t.Cleanup(func() {
		cancel()
		srv.Close()
		select {
		case <-stopped:
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
	return srv, cancel, done, h
}

func dialTestServer(t *testing.T, srv *Server) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("unix", srv.socketPath, 100*time.Millisecond)
		if err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitServer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestShutdownClosesEstablishedAndHandshakeConnections(t *testing.T) {
	for _, method := range []string{"cancel", "close"} {
		t.Run(method, func(t *testing.T) {
			srv, cancel, done, _ := startTestServer(t)
			established := dialTestServer(t, srv)
			established.SetDeadline(time.Now().Add(2 * time.Second))
			if err := protocol.WriteFrame(established, protocol.Message{Type: protocol.TypeHello, AgentID: "codex"}); err != nil {
				t.Fatal(err)
			}
			var welcome protocol.Message
			if err := protocol.ReadFrame(bufio.NewReader(established), &welcome); err != nil {
				t.Fatal(err)
			}
			pending := dialTestServer(t, srv) // Never sends hello.
			if method == "cancel" {
				cancel()
			} else if err := srv.Close(); err != nil {
				t.Fatal(err)
			}
			awaitServer(t, done)
			for _, c := range []net.Conn{established, pending} {
				c.SetReadDeadline(time.Now().Add(time.Second))
				_, err := c.Read(make([]byte, 1))
				if err == nil {
					t.Fatal("connection remains open")
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					t.Fatal("connection was not closed")
				}
			}
			if _, err := os.Lstat(srv.socketPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned socket not removed: %v", err)
			}
		})
	}
}

func TestClosePreservesReplacementPath(t *testing.T) {
	srv, _, done, _ := startTestServer(t)
	dialTestServer(t, srv)
	if err := os.Remove(srv.socketPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srv.socketPath, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	awaitServer(t, done)
	data, err := os.ReadFile(srv.socketPath)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement path removed: %v", err)
	}
}

// Clamp the production deadline so a regression need not wait five seconds.
type shortWriteConn struct{ net.Conn }

func (c shortWriteConn) SetWriteDeadline(d time.Time) error {
	if d.IsZero() {
		return errors.New("missing write deadline")
	}
	return c.Conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
}

func TestWriteTimeoutClosesReadSide(t *testing.T) {
	local, remote := net.Pipe()
	defer remote.Close()
	defer local.Close()
	client := &hub.Client{ID: "slow", Send: make(chan protocol.Message, 1)}
	client.Send <- protocol.Message{Type: protocol.TypeWelcome}
	done := make(chan struct{})
	go New("", nil, testLog()).writePump(context.Background(), client, shortWriteConn{local}, done)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writer blocked without a deadline")
	}
	if _, err := local.Read(make([]byte, 1)); err == nil {
		t.Fatal("writer did not close connection")
	}
}
