package bridge

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type replyArgs struct {
	MessageID      string `json:"message_id" jsonschema:"Original request message ID; matches a reply's in_reply_to or a broker error's message_id"`
	From           string `json:"from" jsonschema:"Expected reply sender's logical agent ID; required, cannot be *"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"Wait up to 1 to 30 seconds; default 30. A timeout does not mean the request failed"`
}

func addReplyTool(s *mcp.Server, c messagingClient) {
	mcp.AddTool(s, &mcp.Tool{Name: "wait_reply", Description: "Consume one reply matching message_id and from, or one correlated broker error. Leaves other messages and all receipts queued. Returns reply, broker_error, timeout, or disconnected with inbox metadata. Does not acknowledge, resend, or imply task completion. In host mode consumes only the fallback copy."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args replyArgs) (*mcp.CallToolResult, any, error) {
			seconds := args.TimeoutSeconds
			if seconds == 0 {
				seconds = 30
			}
			if seconds < 1 || seconds > 30 {
				return nil, nil, errors.New("timeout_seconds must be between 1 and 30")
			}
			out, err := c.WaitReply(ctx, args.MessageID, args.From, time.Duration(seconds)*time.Second)
			return nil, out, err
		})
}
