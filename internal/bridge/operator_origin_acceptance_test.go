package bridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestOperatorAcceptanceClaudeChannelOrigin(t *testing.T) {
	for _, origin := range []string{"operator", "agent", ""} {
		t.Run("origin="+origin, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			st, ct := mcp.NewInMemoryTransports()
			channel := &ChannelTransport{Transport: st}
			server, e := channel.Connect(ctx)
			requireNoError(t, e)
			defer server.Close()
			client, e := ct.Connect(ctx)
			requireNoError(t, e)
			defer client.Close()
			done := make(chan error, 1)
			go func() {
				done <- channel.Publish(ctx, protocol.Message{Type: "msg", From: "operator", Origin: origin, MessageID: "provenance"})
			}()
			frame, e := client.Read(ctx)
			requireNoError(t, e)
			requireNoError(t, <-done)
			request, ok := frame.(*jsonrpc.Request)
			if !ok {
				t.Fatalf("unexpected channel frame: %T", frame)
			}
			var params struct {
				Content string
				Meta    map[string]string
			}
			requireNoError(t, json.Unmarshal(request.Params, &params))
			want := origin
			if want == "" {
				want = "unknown"
			}
			if params.Meta["origin"] != want {
				t.Fatalf("channel metadata origin %q, want %q", params.Meta["origin"], want)
			}
			var msg protocol.Message
			requireNoError(t, json.Unmarshal([]byte(params.Content), &msg))
			if msg.Origin != want {
				t.Fatalf("channel content origin %q, want %q", msg.Origin, want)
			}
		})
	}
}

func TestOperatorAcceptanceReceiveOrigin(t *testing.T) {
	for _, origin := range []string{"operator", "agent", ""} {
		t.Run("origin="+origin, func(t *testing.T) {
			c, remote := pipeClient(t)
			remote.SetWriteDeadline(time.Now().Add(time.Second))
			requireNoError(t, protocol.WriteFrame(remote, protocol.Message{Type: "msg", From: "operator", Origin: origin, MessageID: "received-origin"}))
			out, e := c.Receive(context.Background(), 1, time.Second)
			requireNoError(t, e)
			if len(out.Messages) != 1 {
				t.Fatal("message missing")
			}
			want := origin
			if want == "" {
				want = "unknown"
			}
			if out.Messages[0].Origin != want {
				t.Fatalf("receive origin %q want %q", out.Messages[0].Origin, want)
			}
		})
	}
}
