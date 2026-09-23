package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"collab-ai/internal/host"
	"github.com/coder/websocket"
)

type terminalEndpoint struct {
	dir             string
	server          *http.Server
	cancel          context.CancelFunc
	result          chan error
	stopped         chan struct{}
	sessionDone     chan struct{}
	mu              sync.Mutex
	claimed, closed bool
}

func newTerminalEndpoint(ctx context.Context, run func(context.Context, io.ReadWriteCloser) error) (*terminalEndpoint, error) {
	// A short private path avoids macOS's Unix-socket path length limit.
	dir, err := os.MkdirTemp("/tmp", "collab-terminal-")
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "host.sock"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	e := &terminalEndpoint{dir: dir, cancel: cancel, result: make(chan error, 2), stopped: make(chan struct{}), sessionDone: make(chan struct{})}
	e.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.connect(ctx, w, r, run) })}
	go func() {
		defer close(e.stopped)
		if err := e.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			e.result <- err
		}
	}()
	return e, nil
}

func (e *terminalEndpoint) URL() string { return "unix://" + filepath.Join(e.dir, "host.sock") }

func (e *terminalEndpoint) connect(ctx context.Context, w http.ResponseWriter, r *http.Request, run func(context.Context, io.ReadWriteCloser) error) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser clients are not supported", http.StatusForbidden)
		return
	}
	e.mu.Lock()
	if e.claimed || e.closed {
		e.mu.Unlock()
		http.Error(w, "one terminal session is supported", http.StatusConflict)
		return
	}
	e.claimed = true
	e.mu.Unlock()
	defer close(e.sessionDone)
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		e.result <- err
		return
	}
	stream := host.NewWebSocketStream(ctx, conn)
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	defer stop()
	e.result <- run(ctx, stream)
}

func (e *terminalEndpoint) Close() {
	e.mu.Lock()
	e.closed = true
	claimed := e.claimed
	e.mu.Unlock()
	e.cancel()
	e.server.Close()
	<-e.stopped
	if claimed {
		<-e.sessionDone
	}
	os.RemoveAll(e.dir)
}
