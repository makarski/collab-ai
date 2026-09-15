package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"collab-ai/internal/bridge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolSession reuses the MCP schemas and handlers in the same process and with
// the same inbox owner. It never starts a competing stdio adapter.
type ToolSession struct {
	client *mcp.ClientSession
	server *mcp.ServerSession
	Specs  []any
}

func OpenTools(ctx context.Context, listener *bridge.Listener) (*ToolSession, error) {
	st, ct := mcp.NewInMemoryTransports()
	ss, err := bridge.NewHostMCP(listener, false).Connect(ctx, st, nil)
	if err != nil {
		return nil, err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "collab-codex"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		ss.Close()
		return nil, err
	}
	t := &ToolSession{client: cs, server: ss}
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Close()
		return nil, err
	}
	for _, tool := range listed.Tools {
		t.Specs = append(t.Specs, map[string]any{"type": "function", "name": "collab_" + tool.Name,
			"description": tool.Description, "inputSchema": tool.InputSchema})
	}
	return t, nil
}

func (t *ToolSession) Close() { t.client.Close(); t.server.Close() }

func (t *ToolSession) Call(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(args.Tool, "collab_") {
		return nil, errors.New("unknown collaboration tool")
	}
	result, err := t.client.CallTool(ctx, &mcp.CallToolParams{Name: strings.TrimPrefix(args.Tool, "collab_"), Arguments: args.Arguments})
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": !result.IsError, "contentItems": []any{map[string]any{"type": "inputText", "text": string(data)}}}, nil
}
