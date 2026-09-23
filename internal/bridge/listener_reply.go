package bridge

import (
	"context"
	"time"
)

// WaitReply uses the fallback copy: the publisher remains the sole consumer of
// the raw broker inbox. Draining a reply here cannot prevent host submission.
func (l *Listener) WaitReply(ctx context.Context, id, from string, wait time.Duration) (ReplyResult, error) {
	if err := validateReplyWait(id, from, wait); err != nil {
		return ReplyResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ReplyResult{}, err
	}
	if _, err := l.Activate(ctx); err != nil {
		return ReplyResult{}, err
	}
	return awaitReply(ctx, wait, func(ctx context.Context) (Inbox, <-chan struct{}, error) {
		return l.replySnapshot(ctx, id, from)
	})
}

func (l *Listener) replySnapshot(ctx context.Context, id, from string) (Inbox, <-chan struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Inbox{}, nil, err
	}
	if l.changed == nil {
		l.changed = make(chan struct{})
	}
	out := Inbox{Connected: l.status.State == "listening_delivery_unconfirmed",
		SessionID: l.status.SessionID, Error: l.status.Error,
		AcknowledgmentsSupported: l.status.AcknowledgmentsSupported, DurabilitySupported: l.status.DurabilitySupported}
	out.Messages = takeReply(&l.queue, &l.bytes, id, from)
	return out, l.changed, nil
}
