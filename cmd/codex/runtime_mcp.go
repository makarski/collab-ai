package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"collab-ai/internal/bridge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This endpoint exposes the existing listener, never another broker connection.
// Its private directory limits access to the local user's launched processes.
type runtimeMCP struct {
	dir    string
	ln     net.Listener
	cancel context.CancelFunc
	done   chan struct{}
}

func newRuntimeMCP(ctx context.Context, listener *bridge.Listener) (*runtimeMCP, error) {
	dir, err := os.MkdirTemp("/tmp", "collab-tools-")
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "mcp.sock"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	e := &runtimeMCP{dir: dir, ln: ln, cancel: cancel, done: make(chan struct{})}
	go e.serve(ctx, bridge.NewHostMCP(listener, false))
	return e, nil
}

func (e *runtimeMCP) config() (map[string]any, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": binary, "args": []string{"--mcp-socket", e.ln.Addr().String()}, "required": true, "enabled": true}, nil
}

func (e *runtimeMCP) serve(ctx context.Context, server *mcp.Server) {
	defer close(e.done)
	var sessions sync.WaitGroup
	defer sessions.Wait()
	stop := context.AfterFunc(ctx, func() { e.ln.Close() })
	defer stop()
	for {
		conn, err := e.ln.Accept()
		if err != nil {
			return
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			serveRuntimeMCP(ctx, server, conn)
		}()
	}
}

func serveRuntimeMCP(ctx context.Context, server *mcp.Server, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	err := server.Run(ctx, &mcp.IOTransport{Reader: conn, Writer: conn})
	if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
		return
	}
	if err != nil {
		log.Printf("runtime MCP connection: %v", err)
	}
}

func (e *runtimeMCP) Close() {
	e.cancel()
	e.ln.Close()
	<-e.done
	os.RemoveAll(e.dir)
}

// Codex launches this stdio relay as an MCP subprocess. It has no inbox ID and
// cannot independently register with the broker.
func relayMCP(ctx context.Context, socket string, stdio operatorIO) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close(); stdio.input.Close() })
	defer stop()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, stdio.input)
		conn.Close()
		done <- err
	}()
	_, outputErr := io.Copy(stdio.output, conn)
	conn.Close()
	stdio.input.Close()
	inputErr := <-done
	if ctx.Err() != nil {
		return nil
	}
	return errors.Join(relayError(inputErr), relayError(outputErr))
}

func relayError(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		return nil // Closing either side intentionally interrupts the other copy.
	}
	return err
}
