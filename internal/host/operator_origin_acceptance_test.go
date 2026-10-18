package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestOperatorAcceptanceCodexTurnOrigin(t *testing.T) {
	for _, origin := range []string{"operator", "agent", ""} {
		t.Run("origin="+origin, func(t *testing.T) {
			p, up, down := proxyFixture(t)
			startThread(t, p)
			frameAt(t, up)
			frameAt(t, down)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- p.Publish(ctx, protocol.Message{Type: "msg", From: "operator", MessageID: "provenance", Origin: origin})
			}()
			request := frameAt(t, up)
			if request.Method != "turn/start" {
				t.Fatalf("wrong method: %s", request.Method)
			}
			var params struct {
				ToolOutput struct{ Output string } `json:"toolOutput"`
			}
			check(t, json.Unmarshal(request.Params, &params))
			var message protocol.Message
			check(t, json.Unmarshal([]byte(params.ToolOutput.Output), &message))
			want := origin
			if want == "" {
				want = "unknown"
			}
			if message.Origin != want {
				t.Fatalf("host origin %q want %q", message.Origin, want)
			}
			check(t, p.FromHost(ctx, Frame{ID: request.ID, Result: json.RawMessage(`{"turn":{"id":"origin-turn"}}`)}))
			check(t, <-done)
		})
	}
}
