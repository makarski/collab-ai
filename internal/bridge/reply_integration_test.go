package bridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"collab-ai/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func replyToolResult(t *testing.T, result *mcp.CallToolResult) ReplyResult {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	requireNoError(t, err)
	var out ReplyResult
	requireNoError(t, json.Unmarshal(data, &out))
	return out
}

func TestMCPWaitReplyKeepsReceiptsAndRequiresExplicitAcknowledgment(t *testing.T) {
	socket, _ := startBroker(t)
	requester := connectMCP(t, lazyAgent(t, socket, "requester"))
	peer := connectAgent(t, socket, "peer")
	call(t, requester, "send", map[string]any{"to": "peer", "text": "review", "message_id": "request"})
	assertEqual(t, "request arrived", nextFrame(t, peer).MessageID, "request")
	sendTracked(t, peer, SendRequest{To: "requester", Text: "another topic", MessageID: "unrelated"})
	sendTracked(t, peer, SendRequest{To: "requester", Text: "reviewed", MessageID: "answer", InReplyTo: "request"})
	out := replyToolResult(t, call(t, requester, "wait_reply", map[string]any{"message_id": "request", "from": "peer"}))
	assertEqual(t, "outcome", out.Outcome, "reply")
	assertEqual(t, "reply ID", out.Inbox.Messages[0].MessageID, "answer")
	assertEqual(t, "reply sender", out.Inbox.Messages[0].From, "peer")
	assertEqual(t, "still needs acknowledgment", out.Inbox.Messages[0].AckRequested, true)
	assertEqual(t, "pending after wait", brokerStatus(t, socket).DurablePending.Total, 3)
	assertStage(t, awaitMCPFrame(t, requester), "request", protocol.StageAccepted)
	assertStage(t, awaitMCPFrame(t, requester), "request", protocol.StageAdapterReceived)
	assertEqual(t, "unrelated message retained", awaitMCPFrame(t, requester).MessageID, "unrelated")
	call(t, requester, "acknowledge", map[string]any{"message_id": "answer"})
	assertStage(t, awaitMCPFrame(t, requester), "answer", protocol.StageAgentAcknowledged)
	assertEqual(t, "pending after explicit acknowledgment", brokerStatus(t, socket).DurablePending.Total, 2)
}

func TestMCPWaitReplyRoutingErrorIsNotTimeout(t *testing.T) {
	socket, _ := startBroker(t)
	c := connectMCP(t, lazyAgent(t, socket, "requester"))
	call(t, c, "send", map[string]any{"to": "missing", "text": "review", "message_id": "request"})
	out := replyToolResult(t, call(t, c, "wait_reply", map[string]any{"message_id": "request", "from": "missing", "timeout_seconds": 1}))
	assertEqual(t, "outcome", out.Outcome, "broker_error")
	assertEqual(t, "error", out.Inbox.Messages[0].Code, protocol.ErrUnknownRecipient)
	assertEqual(t, "not timeout", out.Inbox.TimedOut, false)
	assertEqual(t, "still connected", out.Inbox.Connected, true)
}

func TestMCPWaitReplyRejectsInvalidArgumentsWithoutRegistration(t *testing.T) {
	c := lazyAgent(t, "/unused.sock", "requester")
	session := connectMCP(t, c)
	for _, args := range []map[string]any{
		{"message_id": "request"}, {"from": "peer"},
		{"message_id": "request", "from": "*"},
		{"message_id": "request", "from": "peer", "timeout_seconds": -1},
		{"message_id": "request", "from": "peer", "timeout_seconds": 31},
	} {
		out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "wait_reply", Arguments: args})
		if err == nil {
			assertEqual(t, "validation failure", out.IsError, true)
		}
	}
	assertEqual(t, "never registered", c.client == nil, true)
}

func TestWaitReplyRecoversUnacknowledgedReplyAfterBrokerCrash(t *testing.T) {
	socket := crashSocket(t)
	stop := startCrashBroker(t, socket)
	requester := connectAgent(t, socket, "requester")
	peer := connectAgent(t, socket, "peer")
	sendTracked(t, peer, SendRequest{To: "requester", Text: "reviewed", MessageID: "answer", InReplyTo: "request"})
	first := waitReply(t, requester, "request", "peer").Inbox.Messages[0]
	stop()
	requester.Close()
	peer.Close()
	startCrashBroker(t, socket)
	recovered := connectAgent(t, socket, "requester")
	out := waitReply(t, recovered, "request", "peer")
	first.Replayed = true
	assertReply(t, out, first)
	requireNoError(t, recovered.Acknowledge(context.Background(), "answer"))
	assertStage(t, nextFrame(t, recovered), "answer", protocol.StageAgentAcknowledged)
}

func TestListenerWaitReplyCannotStealHostSubmission(t *testing.T) {
	// An unbuffered publisher blocks until this test accepts host submission.
	p := &testPublisher{messages: make(chan protocol.Message)}
	l, peer := listenerFixture(t, p)
	sendTracked(t, peer, SendRequest{To: "worker", Text: "reviewed", MessageID: "answer", InReplyTo: "request"})
	out := waitReply(t, l, "request", "peer")
	assertEqual(t, "outcome", out.Outcome, "reply")
	published := awaitPublished(t, p)
	assertReply(t, out, published)
	assertStage(t, nextFrame(t, peer), "answer", protocol.StageAccepted)
	assertStage(t, nextFrame(t, peer), "answer", protocol.StageAdapterReceived)
	assertInboxTimeout(t, peer)
	fallback, err := l.Receive(context.Background(), 20, 0)
	requireNoError(t, err)
	assertEqual(t, "fallback consumed once", len(fallback.Messages), 0)
	requireNoError(t, l.Acknowledge(context.Background(), "answer"))
	assertStage(t, nextFrame(t, peer), "answer", protocol.StageAgentAcknowledged)
}

func TestListenerWaitReplyPreservesFallbackOrderAndBytes(t *testing.T) {
	p := &testPublisher{messages: make(chan protocol.Message, 3)}
	l, peer := listenerFixture(t, p)
	for _, id := range []string{"before", "request", "after"} {
		sendTracked(t, peer, SendRequest{To: "worker", Text: id, MessageID: id + "-reply", InReplyTo: id})
	}
	before, wanted, after := awaitPublished(t, p), awaitPublished(t, p), awaitPublished(t, p)
	assertReply(t, waitReply(t, l, "request", "peer"), wanted)
	l.mu.Lock()
	bytes := l.bytes
	l.mu.Unlock()
	beforeData, err := json.Marshal(before)
	requireNoError(t, err)
	afterData, err := json.Marshal(after)
	requireNoError(t, err)
	assertEqual(t, "fallback bytes", bytes, len(beforeData)+len(afterData))
	out, err := l.Receive(context.Background(), 20, 0)
	requireNoError(t, err)
	assertFrames(t, out.Messages, []protocol.Message{before, after})
}

func TestListenerWaitReplyWakesOnShutdown(t *testing.T) {
	l, _ := listenerFixture(t, &testPublisher{messages: make(chan protocol.Message, 1)})
	done := make(chan ReplyResult, 1)
	go func() {
		out, err := l.WaitReply(context.Background(), "request", "peer", time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	l.Close()
	out := <-done
	assertEqual(t, "outcome", out.Outcome, "disconnected")
	assertEqual(t, "not timeout", out.Inbox.TimedOut, false)
}
