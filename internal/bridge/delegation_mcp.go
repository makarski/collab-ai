package bridge

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type delegatedWaitArgs struct {
	SocketPath     string `json:"socket_path" jsonschema:"Private local socket returned by the parent's delegate_listener; not the broker socket"`
	Token          string `json:"token" jsonschema:"Read-only capability from the parent; never send to peers or write to configuration"`
	AfterCursor    uint64 `json:"after_cursor,omitempty" jsonschema:"Previous next_cursor after relaying the entire batch; start at 0 for a new or restarted listener"`
	Limit          int    `json:"limit,omitempty" jsonschema:"Maximum frames, 1 to 100; default 20"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"Wait up to 1 to 30 seconds; default 25"`
}

func addDelegationTools(s *mcp.Server, c messagingClient) {
	mcp.AddTool(s, &mcp.Tool{Name: "wait_delegated", Description: "Read the parent's inbox using its explicit local capability, without connecting this adapter to the broker or consuming the parent's copy. Relay every frame and disconnection/error to the parent. Never acknowledges. One outstanding wait per grant; keep next_cursor only after relaying the whole batch."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args delegatedWaitArgs) (*mcp.CallToolResult, any, error) {
			out, err := WaitDelegated(ctx, args.SocketPath, args.Token, delegatedRead{AfterCursor: args.AfterCursor, Limit: args.Limit, TimeoutSeconds: args.TimeoutSeconds})
			return nil, out, err
		})
	// Host listeners already consume the raw client's inbox. Keep the manual
	// adapter's delegation contract separate from those host submission paths.
	owner, ok := c.(*LazyClient)
	if !ok {
		return
	}
	mcp.AddTool(s, &mcp.Tool{Name: "delegate_listener", Description: "Parent only: register the inbox and grant one background listener read-only access for 15 minutes. Returns a private socket and secret token. Replaces any previous grant; keeps the same broker owner. Parent must still drain receive and explicitly acknowledge after reading."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			grant, err := owner.DelegateListener(ctx)
			return nil, grant, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "revoke_listener", Description: "Parent only: revoke this adapter's listener capability and cancel its wait. Does not disconnect the parent or consume or acknowledge messages."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			err := owner.RevokeListener(ctx)
			return nil, map[string]string{"status": "revoked"}, err
		})
}
