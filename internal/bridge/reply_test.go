package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

func replyFrame(id, from string) protocol.Message {
	return protocol.Message{Type: protocol.TypeMsg, MessageID: id + "-reply", InReplyTo: id, From: from,
		Payload: json.RawMessage(`{"text":"reviewed"}`)}
}

func waitReply(t *testing.T, c messagingClient, id, from string) ReplyResult {
	t.Helper()
	out, err := c.WaitReply(context.Background(), id, from, time.Second)
	requireNoError(t, err)
	return out
}

func assertReply(t *testing.T, out ReplyResult, want protocol.Message) {
	t.Helper()
	assertEqual(t, "outcome", out.Outcome, "reply")
	assertEqual(t, "reply count", len(out.Inbox.Messages), 1)
	if !reflect.DeepEqual(out.Inbox.Messages[0], want) {
		t.Fatalf("reply changed: %+v, want %+v", out.Inbox.Messages[0], want)
	}
}

func TestWaitReplyPreservesUnrelatedFramesAndQueueAccounting(t *testing.T) {
	c, _ := pipeClient(t)
	c.sessionID = "owner"
	before := []protocol.Message{
		{Type: protocol.TypeAck, MessageID: "request", Stage: protocol.StageAccepted},
		replyFrame("request", "other-peer"), replyFrame("other-request", "peer"),
		{Type: protocol.TypeError, MessageID: "other-request", Code: protocol.ErrUnknownRecipient},
		{Type: protocol.TypeError, MessageID: "request", AgentID: "other-peer", Code: protocol.ErrRecipientDisconnected},
	}
	want := replyFrame("request", "peer")
	want.Replayed, want.Durable = true, true
	after := replyFrame("later-request", "peer")
	for _, frame := range append(append([]protocol.Message{}, before...), want, after) {
		requireNoError(t, c.bufferMessage(frame))
	}
	out := waitReply(t, c, "request", "peer")
	assertReply(t, out, want)
	assertEqual(t, "session", out.Inbox.SessionID, "owner")
	assertEqual(t, "connected", out.Inbox.Connected, true)
	// Delegated observers share the same retained queue and original cursors.
	observed, err := c.observe(context.Background(), 0, 100, 0)
	requireNoError(t, err)
	assertEqual(t, "arrival cursor", observed.NextCursor, uint64(7))
	remaining := append(before, after)
	assertFrames(t, observed.Inbox.Messages, remaining)
	assertReplyQueueBytes(t, c, remaining)
	drained, err := c.Receive(context.Background(), 100, 0)
	requireNoError(t, err)
	assertFrames(t, drained.Messages, remaining)
	assertReplyQueueBytes(t, c, nil)
}

func assertFrames(t *testing.T, got, want []protocol.Message) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames = %+v, want %+v", got, want)
	}
}

func assertReplyQueueBytes(t *testing.T, c *Client, frames []protocol.Message) {
	t.Helper()
	bytes := 0
	for _, frame := range frames {
		data, err := json.Marshal(frame)
		requireNoError(t, err)
		bytes += len(data)
	}
	c.mu.Lock()
	got := c.inboxBytes
	c.mu.Unlock()
	assertEqual(t, "queue bytes", got, bytes)
}

func TestWaitReplyTimeoutThenLateReply(t *testing.T) {
	c, _ := pipeClient(t)
	receipt := protocol.Message{Type: protocol.TypeAck, MessageID: "request", Stage: protocol.StageAgentAcknowledged}
	requireNoError(t, c.bufferMessage(receipt))
	out, err := c.WaitReply(context.Background(), "request", "peer", 10*time.Millisecond)
	requireNoError(t, err)
	assertEqual(t, "outcome", out.Outcome, "timeout")
	assertEqual(t, "timeout flag", out.Inbox.TimedOut, true)
	assertEqual(t, "still connected", out.Inbox.Connected, true)
	assertEqual(t, "reply count", len(out.Inbox.Messages), 0)
	want := replyFrame("request", "peer")
	requireNoError(t, c.bufferMessage(want))
	assertReply(t, waitReply(t, c, "request", "peer"), want)
	assertStage(t, nextFrame(t, c), "request", protocol.StageAgentAcknowledged)
}

func TestWaitReplyReportsBrokerErrorAndDisconnect(t *testing.T) {
	c, remote := pipeClient(t)
	frame := protocol.Message{Type: protocol.TypeError, MessageID: "request", Code: protocol.ErrUnknownRecipient, Detail: "not registered"}
	requireNoError(t, c.bufferMessage(frame))
	out := waitReply(t, c, "request", "peer")
	assertEqual(t, "outcome", out.Outcome, "broker_error")
	assertFrames(t, out.Inbox.Messages, []protocol.Message{frame})
	want := replyFrame("request", "peer")
	requireNoError(t, c.bufferMessage(want))
	remote.Close()
	waitForClientSignal(t, c.done, "disconnect not observed")
	out = waitReply(t, c, "request", "peer")
	assertReply(t, out, want)
	assertEqual(t, "buffered reply after disconnect", out.Inbox.Connected, false)
	out = waitReply(t, c, "request", "peer")
	assertEqual(t, "terminal outcome", out.Outcome, "disconnected")
	assertEqual(t, "disconnect error", out.Inbox.Error == "", false)
	assertEqual(t, "not a timeout", out.Inbox.TimedOut, false)
}

func TestWaitReplyCancellationPreservesQueuedReply(t *testing.T) {
	c, _ := pipeClient(t)
	want := replyFrame("request", "peer")
	requireNoError(t, c.bufferMessage(want))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.WaitReply(ctx, "request", "peer", time.Second)
	assertEqual(t, "canceled", errors.Is(err, context.Canceled), true)
	assertReply(t, waitReply(t, c, "request", "peer"), want)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = c.WaitReply(ctx, "request", "peer", time.Second)
	assertEqual(t, "waiting canceled", errors.Is(err, context.DeadlineExceeded), true)
}

func TestWaitReplyValidatesBeforeConnecting(t *testing.T) {
	c := lazyAgent(t, "/unused.sock", "owner")
	c.dial = func(context.Context) (*Client, error) {
		t.Error("invalid reply wait dialed")
		return nil, errors.New("unexpected dial")
	}
	l := NewListener(context.Background(), c, nil)
	t.Cleanup(l.Close)
	for _, tc := range []struct {
		id, from string
		wait     time.Duration
	}{
		{"", "peer", time.Second}, {strings.Repeat("x", 129), "peer", time.Second},
		{"request", "", time.Second}, {"request", "*", time.Second},
		{"request", strings.Repeat("x", 129), time.Second},
		{"request", "peer", 0}, {"request", "peer", -time.Second}, {"request", "peer", 31 * time.Second},
	} {
		for _, client := range []messagingClient{c, l} {
			_, err := client.WaitReply(context.Background(), tc.id, tc.from, tc.wait)
			assertEqual(t, "validation error", err == nil, false)
		}
	}
}

func TestWaitReplyConcurrentRequestsWakeIndependently(t *testing.T) {
	c, _ := pipeClient(t)
	results := make(chan ReplyResult, 8)
	ready := make(chan struct{}, 8)
	for i := range 8 {
		go func() {
			out, err := awaitReply(context.Background(), time.Second, replySubscription(c, fmt.Sprint(i), ready))
			if err != nil {
				t.Error(err)
			}
			results <- out
		}()
	}
	for range 8 {
		<-ready
	}
	for i := 7; i >= 0; i-- {
		requireNoError(t, c.bufferMessage(replyFrame(fmt.Sprint(i), "peer")))
	}
	seen := map[string]bool{}
	for range 8 {
		out := <-results
		assertEqual(t, "outcome", out.Outcome, "reply")
		assertEqual(t, "one reply", len(out.Inbox.Messages), 1)
		seen[out.Inbox.Messages[0].InReplyTo] = true
	}
	assertEqual(t, "distinct replies", len(seen), 8)
}

func replySubscription(c *Client, id string, ready chan<- struct{}) func(context.Context) (Inbox, <-chan struct{}, error) {
	first := true
	return func(ctx context.Context) (Inbox, <-chan struct{}, error) {
		out, changed, err := c.replySnapshot(ctx, id, "peer")
		if first {
			first = false
			ready <- struct{}{}
		}
		return out, changed, err
	}
}

func TestWaitReplySameKeyConsumesOnlyOnce(t *testing.T) {
	c, _ := pipeClient(t)
	requireNoError(t, c.bufferMessage(replyFrame("request", "peer")))
	results := make(chan ReplyResult, 2)
	for range 2 {
		go func() {
			out, err := c.WaitReply(context.Background(), "request", "peer", 20*time.Millisecond)
			if err != nil {
				t.Error(err)
			}
			results <- out
		}()
	}
	counts := map[string]int{}
	for range 2 {
		counts[(<-results).Outcome]++
	}
	assertEqual(t, "one reply", counts["reply"], 1)
	assertEqual(t, "one timeout", counts["timeout"], 1)
}

func TestWaitReplyRechecksQueueAtTimeout(t *testing.T) {
	for _, terminal := range []Inbox{
		{Messages: []protocol.Message{replyFrame("request", "peer")}, Connected: true},
		{Messages: []protocol.Message{}, Connected: false, Error: "broker stopped"},
	} {
		calls := 0
		out, err := awaitReply(context.Background(), time.Millisecond, func(context.Context) (Inbox, <-chan struct{}, error) {
			calls++
			if calls == 1 {
				return Inbox{Connected: true}, nil, nil
			}
			return terminal, nil, nil
		})
		requireNoError(t, err)
		assertEqual(t, "deadline snapshot", calls, 2)
		assertEqual(t, "not a timeout", out.Inbox.TimedOut, false)
		assertEqual(t, "terminal outcome", out.Outcome, replyResult(terminal).Outcome)
	}
}

func TestWaitReplyPreservesInboxLimits(t *testing.T) {
	c, _ := pipeClient(t)
	for range maxInboxMessages - 1 {
		requireNoError(t, c.bufferMessage(protocol.Message{Type: protocol.TypeAck}))
	}
	requireNoError(t, c.bufferMessage(replyFrame("request", "peer")))
	assertEqual(t, "matched at queue limit", waitReply(t, c, "request", "peer").Outcome, "reply")
	requireNoError(t, c.bufferMessage(protocol.Message{Type: protocol.TypeAck}))
	assertErrorContains(t, c.bufferMessage(protocol.Message{Type: protocol.TypeAck}), "overflow")
}
