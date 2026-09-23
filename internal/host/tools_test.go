package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"collab-ai/internal/bridge"
)

func toolProxy(t *testing.T) (*Proxy, <-chan Frame) {
	t.Helper()
	p, upstream, _ := proxyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client, err := bridge.NewLazyClient(bridge.ClientConfig{AgentID: "test-worker", SocketPath: "/unused"})
	check(t, err)
	p.Listener = bridge.NewListener(ctx, client, p)
	t.Cleanup(p.Listener.Close)
	p.Tools, err = OpenTools(ctx, p.Listener)
	check(t, err)
	t.Cleanup(p.Tools.Close)
	p.threadID = "owned-thread"
	return p, upstream
}

func TestDynamicToolResponseShape(t *testing.T) {
	p, upstream := toolProxy(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.ServeTools(ctx) }()
	request := Frame{ID: json.RawMessage(`7`), Method: "item/tool/call", Params: json.RawMessage(`{"threadId":"owned-thread","tool":"collab_listener_status","arguments":{}}`)}
	check(t, p.FromHost(ctx, request))
	response := frameAt(t, upstream)
	assertToolResponse(t, response)
	cancel()
	<-done
}

// Codex 0.154.0's generated DynamicToolCallResponse schema requires contentItems
// entries with type=inputText and text (not content). Also verified by live calls.
func assertToolResponse(t *testing.T, response Frame) {
	t.Helper()
	if string(response.ID) != "7" {
		t.Fatal("tool response ID lost")
	}
	var result struct {
		Success      bool `json:"success"`
		ContentItems []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"contentItems"`
	}
	check(t, json.Unmarshal(response.Result, &result))
	if !result.Success {
		t.Fatalf("tool failed: %s", response.Result)
	}
	if len(result.ContentItems) != 1 {
		t.Fatal("expected one content item")
	}
	item := result.ContentItems[0]
	if item.Type != "inputText" {
		t.Fatal("wrong content type")
	}
	var mcpResult struct {
		StructuredContent bridge.ListenerStatus `json:"structuredContent"`
	}
	check(t, json.Unmarshal([]byte(item.Text), &mcpResult))
	if mcpResult.StructuredContent.State != "inactive" {
		t.Fatal("status tool connected or returned wrong output")
	}
}

func TestDynamicToolHandlerError(t *testing.T) {
	p, upstream := toolProxy(t)
	request := Frame{ID: json.RawMessage(`9`), Params: json.RawMessage(`{"tool":"collab_missing","arguments":{}}`)}
	check(t, p.respondTool(context.Background(), request))
	response := frameAt(t, upstream)
	if string(response.ID) != "9" {
		t.Fatal("error response ID lost")
	}
	if len(response.Error) == 0 {
		t.Fatalf("expected RPC error: %+v", response)
	}
}

func TestDynamicToolValidationFailure(t *testing.T) {
	for _, tool := range []string{"collab_wait", "collab_wait_reply"} {
		t.Run(tool, func(t *testing.T) { assertInvalidToolTimeout(t, tool) })
	}
}

func assertInvalidToolTimeout(t *testing.T, tool string) {
	t.Helper()
	p, upstream := toolProxy(t)
	params, err := json.Marshal(map[string]any{"tool": tool, "arguments": map[string]any{
		"timeout_seconds": 31, "message_id": "request", "from": "peer",
	}})
	check(t, err)
	request := Frame{ID: json.RawMessage(`10`), Params: params}
	check(t, p.respondTool(context.Background(), request))
	var result struct {
		Success      bool  `json:"success"`
		ContentItems []any `json:"contentItems"`
	}
	check(t, json.Unmarshal(frameAt(t, upstream).Result, &result))
	if result.Success {
		t.Fatal("invalid wait succeeded")
	}
	if len(result.ContentItems) != 1 {
		t.Fatal("validation error content missing")
	}
	if p.Listener.Status().State != "inactive" {
		t.Fatal("validation failure connected to broker")
	}
}
