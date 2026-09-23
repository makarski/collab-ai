package bridge

import (
	"context"
	"time"

	"collab-ai/internal/protocol"
)

// WaitReply consumes one reply from the expected peer, or one correlated broker
// error. Other traffic stays queued for ordinary Receive or another reply wait.
func (c *Client) WaitReply(ctx context.Context, id, from string, wait time.Duration) (ReplyResult, error) {
	if err := validateReplyWait(id, from, wait); err != nil {
		return ReplyResult{}, err
	}
	return awaitReply(ctx, wait, func(ctx context.Context) (Inbox, <-chan struct{}, error) {
		return c.replySnapshot(ctx, id, from)
	})
}

func (c *Client) replySnapshot(ctx context.Context, id, from string) (Inbox, <-chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Inbox{}, nil, err
	}
	if c.changed == nil {
		c.changed = make(chan struct{})
	}
	out := Inbox{Connected: c.err == nil, SessionID: c.sessionID,
		AcknowledgmentsSupported: c.protocolVersion >= protocol.AcknowledgmentVersion, DurabilitySupported: c.protocolVersion >= protocol.DurableVersion}
	if c.err != nil {
		out.Error = c.err.Error()
	}
	out.Messages = takeReply(&c.inbox, &c.inboxBytes, id, from)
	return out, c.changed, nil
}

func (c *LazyClient) WaitReply(ctx context.Context, id, from string, wait time.Duration) (ReplyResult, error) {
	if err := validateReplyWait(id, from, wait); err != nil {
		return ReplyResult{}, err
	}
	client, err := c.connection(ctx)
	if err != nil {
		return ReplyResult{}, err
	}
	return client.WaitReply(ctx, id, from, wait)
}
