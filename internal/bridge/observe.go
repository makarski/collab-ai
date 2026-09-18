package bridge

import (
	"context"
	"errors"
	"time"

	"collab-ai/internal/protocol"
)

// DelegatedInbox is a non-consuming view of frames still retained by the parent.
// Cursor is local to this adapter lifetime, not a broker sequence or an ack.
type DelegatedInbox struct {
	Inbox      Inbox  `json:"inbox"`
	NextCursor uint64 `json:"next_cursor"`
}

func (c *Client) signalObserversLocked() {
	if c.changed != nil {
		close(c.changed)
		c.changed = nil
	}
}

func (c *Client) observe(ctx context.Context, after uint64, limit int, wait time.Duration) (DelegatedInbox, error) {
	if err := validateListenerRead(limit, wait); err != nil {
		return DelegatedInbox{}, err
	}
	if wait == 0 {
		out, _, err := c.observerSnapshot(ctx, after, limit)
		return out, err
	}
	return c.waitObserved(ctx, after, limit, wait)
}

func (out DelegatedInbox) ready() bool {
	return len(out.Inbox.Messages) > 0 || !out.Inbox.Connected
}

func (c *Client) waitObserved(ctx context.Context, after uint64, limit int, wait time.Duration) (DelegatedInbox, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		out, changed, err := c.observerSnapshot(ctx, after, limit)
		if err != nil || out.ready() {
			return out, err
		}
		select {
		case <-ctx.Done():
			return DelegatedInbox{}, ctx.Err()
		case <-changed:
		case <-timer.C:
			return c.observedTimeout(ctx, after, limit)
		}
	}
}

func (c *Client) observedTimeout(ctx context.Context, after uint64, limit int) (DelegatedInbox, error) {
	out, _, err := c.observerSnapshot(ctx, after, limit)
	if err != nil {
		return out, err
	}
	out.Inbox.TimedOut = !out.ready()
	return out, nil
}

func (c *Client) observerSnapshot(ctx context.Context, after uint64, limit int) (DelegatedInbox, <-chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return DelegatedInbox{}, nil, err
	}
	if after > c.nextCursor {
		return DelegatedInbox{}, nil, errors.New("cursor is ahead of this adapter; restart the delegated listener with after_cursor 0")
	}
	if c.changed == nil {
		c.changed = make(chan struct{})
	}
	out := DelegatedInbox{NextCursor: c.nextCursor, Inbox: Inbox{
		Messages: []protocol.Message{}, Connected: c.err == nil, SessionID: c.sessionID,
		AcknowledgmentsSupported: c.protocolVersion >= protocol.AcknowledgmentVersion,
		DurabilitySupported:      c.protocolVersion >= protocol.DurableVersion,
	}}
	if c.err != nil {
		out.Inbox.Error = c.err.Error()
	}
	for _, q := range c.inbox {
		if q.cursor <= after {
			continue
		}
		out.Inbox.Messages = append(out.Inbox.Messages, q.message)
		if len(out.Inbox.Messages) == limit {
			out.NextCursor = q.cursor
			break
		}
	}
	return out, c.changed, nil
}
