package bridge

import (
	"context"
	"errors"
	"time"

	"collab-ai/internal/protocol"
)

// ReplyResult consumes at most one frame. A reply is neither an acknowledgment
// nor evidence of task completion; the original frame remains authoritative.
type ReplyResult struct {
	Outcome string `json:"outcome"`
	Inbox   Inbox  `json:"inbox"`
}

func validateReplyWait(id, from string, wait time.Duration) error {
	if len(id) == 0 || len(id) > 128 {
		return errors.New("message_id must be 1 to 128 bytes")
	}
	if err := protocol.ValidateAgentID(from); err != nil {
		return errors.New("from must be an explicit agent ID, 1 to 128 bytes, other than *")
	}
	if wait <= 0 || wait > 30*time.Second {
		return errors.New("reply wait must be greater than zero and at most 30 seconds")
	}
	return nil
}

func matchesReply(msg protocol.Message, id, from string) bool {
	switch msg.Type {
	case protocol.TypeMsg:
		return msg.InReplyTo == id && msg.From == from
	case protocol.TypeError:
		if msg.MessageID != id {
			return false
		}
		return msg.AgentID == "" || msg.AgentID == from
	default:
		return false
	}
}

// Called under the owning queue's lock. Remove only the first matching frame,
// preserving the order, cursors, and byte accounting of everything else.
func takeReply(queue *[]queuedMessage, bytes *int, id, from string) []protocol.Message {
	for i, q := range *queue {
		if !matchesReply(q.message, id, from) {
			continue
		}
		*bytes -= q.size
		copy((*queue)[i:], (*queue)[i+1:])
		last := len(*queue) - 1
		(*queue)[last] = queuedMessage{}
		*queue = (*queue)[:last]
		return []protocol.Message{q.message}
	}
	return []protocol.Message{}
}

func replyResult(out Inbox) ReplyResult {
	result := ReplyResult{Outcome: "timeout", Inbox: out}
	if len(out.Messages) > 0 {
		result.Outcome = "reply"
		if out.Messages[0].Type == protocol.TypeError {
			result.Outcome = "broker_error"
		}
	} else if !out.Connected {
		result.Outcome = "disconnected"
	}
	return result
}

// Snapshot and subscription are atomic under the queue lock. Broadcast signals
// wake all selective readers, so a different request cannot steal a wakeup.
func awaitReply(ctx context.Context, wait time.Duration, snapshot func(context.Context) (Inbox, <-chan struct{}, error)) (ReplyResult, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		out, changed, err := snapshot(ctx)
		if err != nil {
			return ReplyResult{}, err
		}
		if len(out.Messages) > 0 || !out.Connected {
			return replyResult(out), nil
		}
		select {
		case <-ctx.Done():
			return ReplyResult{}, ctx.Err()
		case <-changed:
		case <-timer.C:
			return replyTimeout(ctx, snapshot)
		}
	}
}

func replyTimeout(ctx context.Context, snapshot func(context.Context) (Inbox, <-chan struct{}, error)) (ReplyResult, error) {
	// Inspect once more to retain a reply/disconnect arriving at the deadline.
	out, _, err := snapshot(ctx)
	if err != nil {
		return ReplyResult{}, err
	}
	out.TimedOut = len(out.Messages) == 0 && out.Connected
	return replyResult(out), nil
}
