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

func TestTerminalSocketPreservesFramesAndRejectsOtherClients(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	endpoint, err := newTerminalEndpoint(ctx, func(ctx context.Context, stream io.ReadWriteCloser) error {
		return host.ReadFrames(stream, func(frame host.Frame) error { return host.NewWire(stream).Write(ctx, frame) })
	})
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	info, err := os.Stat(endpoint.dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("terminal socket directory must be private: %v", err)
	}
	_, response, err := dialTerminal(ctx, endpoint, "https://untrusted.example")
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("browser origin was not rejected: %v", err)
	}
	conn, _, err := dialTerminal(ctx, endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, response, err = dialTerminal(ctx, endpoint, "")
	if err == nil || response == nil || response.StatusCode != http.StatusConflict {
		t.Fatalf("second client was not rejected: %v", err)
	}
	// Pretty-printed input must not split into multiple JSONL frames.
	request := []byte("{\n  \"id\": 7,\n  \"method\": \"thread/start\",\n  \"params\": {\"sandbox\": \"read-only\", \"approvalPolicy\": \"on-request\"}\n}")
	if err := conn.Write(ctx, websocket.MessageText, request); err != nil {
		t.Fatal(err)
	}
	kind, data, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("read echo: %v", err)
	}
	var got, want host.Frame
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request, &want); err != nil {
		t.Fatal(err)
	}
	if string(got.ID) != string(want.ID) || got.Method != want.Method || !strings.Contains(string(got.Params), `"approvalPolicy":"on-request"`) {
		t.Fatalf("frame changed: %s", data)
	}
	// Shutdown cancels the active read and removes only the generated directory.
	endpoint.Close()
	if _, err := os.Stat(endpoint.dir); !os.IsNotExist(err) {
		t.Fatalf("endpoint directory leaked: %v", err)
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
