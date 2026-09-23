package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/host"
	"github.com/coder/websocket"
)

func dialTerminal(ctx context.Context, endpoint *terminalEndpoint, origin string) (*websocket.Conn, *http.Response, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(endpoint.URL(), "unix://"))
	}}
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	conn, response, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, HTTPHeader: header})
	transport.CloseIdleConnections()
	return conn, response, err
}

func TestTerminalEmptyCloseFrameEndsCleanly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	endpoint, err := newTerminalEndpoint(ctx, func(_ context.Context, stream io.ReadWriteCloser) error {
		return host.ReadFrames(stream, func(host.Frame) error { return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	conn, _, err := dialTerminal(ctx, endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// Codex 0.156.1 sends an empty close frame on terminal exit.
	conn.Close(websocket.StatusNoStatusRcvd, "")
	select {
	case err := <-endpoint.result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("normal terminal exit became an error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("terminal shutdown blocked")
	}
}

func echoTerminal(t *testing.T) (context.Context, *terminalEndpoint) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	endpoint, err := newTerminalEndpoint(ctx, func(ctx context.Context, stream io.ReadWriteCloser) error {
		return host.ReadFrames(stream, func(frame host.Frame) error { return host.NewWire(stream).Write(ctx, frame) })
	})
	terminalCheck(t, err)
	t.Cleanup(endpoint.Close)
	return ctx, endpoint
}

func terminalCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertRejectedUpgrade(t *testing.T, response *http.Response, err error, status int) {
	t.Helper()
	if err == nil || response == nil {
		t.Fatalf("upgrade not rejected: %v", err)
	}
	if response.StatusCode != status {
		t.Fatalf("upgrade status: got %d, want %d", response.StatusCode, status)
	}
}

func TestTerminalSocketIsPrivateAndRejectsOtherClients(t *testing.T) {
	ctx, endpoint := echoTerminal(t)
	info, err := os.Stat(endpoint.dir)
	terminalCheck(t, err)
	if info.Mode().Perm() != 0700 {
		t.Fatal("terminal socket directory is not private")
	}
	_, response, err := dialTerminal(ctx, endpoint, "https://untrusted.example")
	assertRejectedUpgrade(t, response, err, http.StatusForbidden)
	conn, _, err := dialTerminal(ctx, endpoint, "")
	terminalCheck(t, err)
	defer conn.CloseNow()
	_, response, err = dialTerminal(ctx, endpoint, "")
	assertRejectedUpgrade(t, response, err, http.StatusConflict)
}

func TestTerminalSocketPreservesFramesAndCleansUp(t *testing.T) {
	ctx, endpoint := echoTerminal(t)
	conn, _, err := dialTerminal(ctx, endpoint, "")
	terminalCheck(t, err)
	defer conn.CloseNow()
	// Pretty-printed input must not split into multiple JSONL frames.
	request := []byte(`{
		"id": 7,
		"method": "thread/start",
		"params": {"sandbox": "read-only", "approvalPolicy": "on-request"}
	}`)
	terminalCheck(t, conn.Write(ctx, websocket.MessageText, request))
	kind, data, err := conn.Read(ctx)
	terminalCheck(t, err)
	if kind != websocket.MessageText {
		t.Fatal("wrong response framing")
	}
	assertTerminalFrame(t, data)
	// Shutdown cancels the active read and removes only the generated directory.
	endpoint.Close()
	if _, err := os.Stat(endpoint.dir); !os.IsNotExist(err) {
		t.Fatalf("endpoint directory leaked: %v", err)
	}
}

func assertTerminalFrame(t *testing.T, data []byte) {
	t.Helper()
	var got host.Frame
	terminalCheck(t, json.Unmarshal(data, &got))
	if string(got.ID) != "7" || got.Method != "thread/start" {
		t.Fatalf("frame identity changed: %s", data)
	}
	if string(got.Params) != `{"sandbox":"read-only","approvalPolicy":"on-request"}` {
		t.Fatalf("frame params changed: %s", data)
	}
}

func TestTerminalRejectsInvalidFramesAndCancelsSession(t *testing.T) {
	for _, kind := range []websocket.MessageType{websocket.MessageText, websocket.MessageBinary} {
		t.Run(kind.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			endpoint, err := newTerminalEndpoint(ctx, func(_ context.Context, stream io.ReadWriteCloser) error {
				return host.ReadFrames(stream, func(host.Frame) error { t.Error("invalid frame was forwarded"); return nil })
			})
			if err != nil {
				t.Fatal(err)
			}
			defer endpoint.Close()
			conn, _, err := dialTerminal(ctx, endpoint, "")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx, kind, []byte("{}\n{}")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-endpoint.result:
				if err == nil {
					t.Fatal("invalid frame accepted")
				}
			case <-ctx.Done():
				t.Fatal("invalid frame did not terminate session")
			}
		})
	}
}
