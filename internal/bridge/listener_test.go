package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

type testPublisher struct {
	messages chan protocol.Message
	err      error
}

func (p *testPublisher) Publish(ctx context.Context, msg protocol.Message) error {
	if p.err != nil {
		return p.err
	}
	select {
	case p.messages <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func listenerFixture(t *testing.T, host Publisher) (*Listener, *Client) {
	t.Helper()
	path, _ := startBroker(t)
	l := NewListener(context.Background(), lazyAgent(t, path, "worker"), host)
	t.Cleanup(l.Close)
	_, err := l.Activate(context.Background())
	requireNoError(t, err)
	return l, connectAgent(t, path, "peer")
}

func awaitPublished(t *testing.T, p *testPublisher) protocol.Message {
	t.Helper()
	select {
	case msg := <-p.messages:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("host submission timed out")
		return protocol.Message{}
	}
}

func TestListenerSubmissionNeedsExplicitAcknowledgment(t *testing.T) {
	p := &testPublisher{messages: make(chan protocol.Message, 1)}
	l, peer := listenerFixture(t, p)
	request := SendRequest{To: "worker", Text: "review the cancellation path", MessageID: "listener-review"}
	_, err := peer.SendMessage(context.Background(), request)
	requireNoError(t, err)
	msg := awaitPublished(t, p)
	assertEqual(t, "published ID", msg.MessageID, request.MessageID)
	assertStage(t, nextFrame(t, peer), request.MessageID, protocol.StageAccepted)
	assertStage(t, nextFrame(t, peer), request.MessageID, protocol.StageAdapterReceived)
	inbox, err := peer.Receive(context.Background(), 1, 20*time.Millisecond)
	requireNoError(t, err)
	assertEqual(t, "automatic agent acknowledgment", len(inbox.Messages), 0)
	requireNoError(t, l.Acknowledge(context.Background(), request.MessageID))
	assertStage(t, nextFrame(t, peer), request.MessageID, protocol.StageAgentAcknowledged)
	fallback, err := l.Receive(context.Background(), 1, 0)
	requireNoError(t, err)
	assertEqual(t, "fallback retains submitted message", fallback.Messages[0].MessageID, request.MessageID)
}

func TestListenerConcurrentArrivalsAndFallback(t *testing.T) {
	p := &testPublisher{messages: make(chan protocol.Message, 32)}
	l, peer := listenerFixture(t, p)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			_, err := peer.SendMessage(context.Background(), SendRequest{To: "worker", Text: "review", MessageID: fmt.Sprint(i)})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	seen := make(map[string]bool)
	for range 16 {
		seen[awaitPublished(t, p).MessageID] = true
	}
	assertEqual(t, "unique submissions", len(seen), 16)
	out, err := l.Receive(context.Background(), 100, 0)
	requireNoError(t, err)
	assertEqual(t, "retained frames", len(out.Messages), 16)
}

func TestListenerHostFailureRetainsFallback(t *testing.T) {
	p := &testPublisher{err: errors.New("host unavailable")}
	l, peer := listenerFixture(t, p)
	_, err := peer.SendMessage(context.Background(), SendRequest{To: "worker", Text: "review", MessageID: "unavailable"})
	requireNoError(t, err)
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not stop")
	}
	status := l.Status()
	assertEqual(t, "state", status.State, "disconnected")
	assertEqual(t, "submitted", status.Submitted, 0)
	assertEqual(t, "manual fallback", status.ManualCheckRequired, true)
	out, err := l.Receive(context.Background(), 1, 0)
	requireNoError(t, err)
	assertEqual(t, "connected", out.Connected, false)
	assertEqual(t, "retained ID", out.Messages[0].MessageID, "unavailable")
}

func TestListenerCancellationWhilePublishing(t *testing.T) {
	p := &testPublisher{messages: make(chan protocol.Message)}
	l, peer := listenerFixture(t, p)
	sent, err := peer.SendMessage(context.Background(), SendRequest{To: "worker", Text: "review"})
	requireNoError(t, err)
	assertStage(t, nextFrame(t, peer), sent.MessageID, protocol.StageAccepted)
	assertStage(t, nextFrame(t, peer), sent.MessageID, protocol.StageAdapterReceived)
	l.Close()
	assertEqual(t, "stopped", l.Status().State, "stopped")
}

func TestListenerOverflowIsTerminal(t *testing.T) {
	p := &testPublisher{messages: make(chan protocol.Message, 1)}
	l, _ := listenerFixture(t, p)
	for range maxInboxMessages {
		requireNoError(t, l.retain(protocol.Message{Type: protocol.TypeAck}))
	}
	err := l.retain(protocol.Message{Type: protocol.TypeMsg})
	assertErrorContains(t, err, "overflow")
	l.fail(err)
	assertEqual(t, "overflow state", l.Status().State, "disconnected")
}
