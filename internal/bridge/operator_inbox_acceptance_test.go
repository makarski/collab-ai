package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestOperatorInboxWaitReplyAcceptsOperatorSender(t *testing.T) {
	c, _ := pipeClient(t)
	want := replyFrame("question", "operator")
	want.Origin = "operator"
	unrelated := replyFrame("question", "peer")
	requireNoError(t, c.bufferMessage(unrelated))
	requireNoError(t, c.bufferMessage(want))
	out, err := c.WaitReply(context.Background(), "question", "operator", time.Second)
	requireNoError(t, err)
	assertReply(t, out, want)
	remaining, err := c.Receive(context.Background(), 10, 0)
	requireNoError(t, err)
	assertFrames(t, remaining.Messages, []protocol.Message{unrelated})
	if protocol.ValidateAgentID("operator") == nil {
		t.Fatal("reply filter relaxed reserved registration identity")
	}
}
func TestOperatorInboxWaitReplyStillRejectsBroadcast(t *testing.T) {
	c, _ := pipeClient(t)
	_, err := c.WaitReply(context.Background(), "question", "*", time.Second)
	if err == nil {
		t.Fatal("broadcast sender filter accepted")
	}
}
