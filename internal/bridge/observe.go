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
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return DelegatedInbox{}, err
		}
		out, changed, err := c.observerSnapshot(after, limit)
		if err != nil || len(out.Inbox.Messages) > 0 || !out.Inbox.Connected || wait == 0 {
			return out, err
		}
		select {
		case <-ctx.Done():
			return DelegatedInbox{}, ctx.Err()
		case <-changed:
		case <-timer.C:
			out, _, err = c.observerSnapshot(after, limit)
			out.Inbox.TimedOut = err == nil && out.Inbox.Connected && len(out.Inbox.Messages) == 0
			return out, err
		}
	}
}

func (c *Client) observerSnapshot(after uint64, limit int) (DelegatedInbox, <-chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
