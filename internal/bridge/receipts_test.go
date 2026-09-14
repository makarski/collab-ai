package bridge

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

func nextFrame(t *testing.T, c *Client) protocol.Message {
	t.Helper()
	out, err := c.Receive(context.Background(), 1, time.Second)
	if err != nil || len(out.Messages) != 1 {
		t.Fatalf("expected frame: %+v %v", out, err)
	}
	return out.Messages[0]
}

func TestDuplicateMessagingSessionCannotEvictOwner(t *testing.T) {
	path, _ := startBroker(t)
	owner := connectAgent(t, path, "codex")
	peer := connectAgent(t, path, "claude")
	rejected := lazyAgent(t, path, "codex")
	for i := 0; i < 2; i++ {
		if _, err := rejected.Receive(context.Background(), 20, 0); err == nil || !strings.Contains(err.Error(), owner.sessionID) || !strings.Contains(err.Error(), protocol.ErrDuplicateID) {
			t.Fatalf("missing actionable owner rejection: %v", err)
		}
	}
	if err := peer.Send(context.Background(), "codex", "owner is still here"); err != nil {
		t.Fatal(err)
	}
	if got := nextFrame(t, owner); got.From != "claude" {
		t.Fatalf("owner displaced: %+v", got)
	}
	oldSession := owner.sessionID
	owner.Close()
	// Retry only an initial registration; release is processed asynchronously.
	deadline := time.Now().Add(time.Second)
	for {
		out, err := rejected.Receive(context.Background(), 20, 0)
		if err == nil {
			if !out.Connected || out.SessionID == "" || out.SessionID == oldSession {
				t.Fatalf("reused session identity: %+v", out)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := peer.Send(context.Background(), "codex", "new owner"); err != nil {
		t.Fatal(err)
	}
	out, err := rejected.Receive(context.Background(), 20, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertSinglePayload(t, out, `{"text":"new owner"}`)
}

func TestBroadcastAcknowledgmentsFreezeMembership(t *testing.T) {
	path, _ := startBroker(t)
	sender := connectAgent(t, path, "claude")
	a := connectAgent(t, path, "codex")
	b := connectAgent(t, path, "reviewer")
	result, err := sender.SendMessage(context.Background(), "*", "review", "broadcast-review", "")
	if err != nil {
		t.Fatal(err)
	}
	accepted := nextFrame(t, sender)
	assertStage(t, accepted, result.MessageID, protocol.StageAccepted)
	if len(accepted.Recipients) != 2 || accepted.Recipients[0].AgentID != "codex" || accepted.Recipients[1].AgentID != "reviewer" {
		t.Fatalf("broadcast scope: %+v", accepted)
	}
	late := connectAgent(t, path, "late")
	for _, c := range []*Client{a, b} {
		if got := nextFrame(t, c); got.MessageID != result.MessageID || got.To != "*" {
			t.Fatalf("bad broadcast: %+v", got)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		receipt := nextFrame(t, sender)
		assertStage(t, receipt, result.MessageID, protocol.StageAdapterReceived)
		seen[receipt.AgentID] = true
	}
	if !seen["codex"] || !seen["reviewer"] {
		t.Fatalf("receipts not per recipient: %v", seen)
	}
	if err := late.Acknowledge(context.Background(), result.MessageID); err != nil {
		t.Fatal(err)
	}
	if got := nextFrame(t, late); got.Code != protocol.ErrInvalidAck {
		t.Fatalf("late peer acknowledged broadcast: %+v", got)
	}
	// Same ID is never routed twice, including different content.
	if _, err := sender.SendMessage(context.Background(), "*", "changed content", result.MessageID, ""); err != nil {
		t.Fatal(err)
	}
	if got := nextFrame(t, sender); got.Code != protocol.ErrDuplicateMessage || got.MessageID != result.MessageID {
		t.Fatalf("duplicate not correlated: %+v", got)
	}
	for _, c := range []*Client{a, b} {
		if out, err := c.Receive(context.Background(), 20, 10*time.Millisecond); err != nil || !out.TimedOut {
			t.Fatalf("duplicate delivered: %+v %v", out, err)
		}
	}
}

func TestRecipientDisconnectBetweenAcknowledgmentStages(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-adapter-receipt", true: "after-adapter-receipt"}[receipt], func(t *testing.T) {
			path, _ := startBroker(t)
			sender := connectAgent(t, path, "claude")
			// A wire client lets the test disconnect before or after an actual receipt.
			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			reader := bufio.NewReader(conn)
			if err := protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeHello, AgentID: "codex", ProtocolVersion: protocol.Version}); err != nil {
				t.Fatal(err)
			}
			var welcome protocol.Message
			if err := protocol.ReadFrame(reader, &welcome); err != nil {
				t.Fatal(err)
			}
			sent, err := sender.SendMessage(context.Background(), "codex", "review", "disconnect-review", "")
			if err != nil {
				t.Fatal(err)
			}
			assertStage(t, nextFrame(t, sender), sent.MessageID, protocol.StageAccepted)
			var msg protocol.Message
			if err := protocol.ReadFrame(reader, &msg); err != nil {
				t.Fatal(err)
			}
			if receipt {
				if err := protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeAck, MessageID: sent.MessageID, Stage: protocol.StageAdapterReceived}); err != nil {
					t.Fatal(err)
				}
				assertStage(t, nextFrame(t, sender), sent.MessageID, protocol.StageAdapterReceived)
			}
			conn.Close()
			got := nextFrame(t, sender)
			if got.Code != protocol.ErrRecipientDisconnected || got.MessageID != sent.MessageID || got.SessionID != welcome.SessionID {
				t.Fatalf("disconnect hidden: %+v", got)
			}
			newOwner := connectAgent(t, path, "codex")
			if err := newOwner.Acknowledge(context.Background(), sent.MessageID); err != nil {
				t.Fatal(err)
			}
			if got := nextFrame(t, newOwner); got.Code != protocol.ErrInvalidAck {
				t.Fatalf("new session acknowledged old message: %+v", got)
			}
		})
	}
}

func TestLegacyRecipientDoesNotImplyReceipt(t *testing.T) {
	path, _ := startBroker(t)
	sender := connectAgent(t, path, "claude")
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(conn)
	if err := protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeHello, AgentID: "legacy"}); err != nil {
		t.Fatal(err)
	}
	var msg protocol.Message
	if err := protocol.ReadFrame(r, &msg); err != nil {
		t.Fatal(err)
	}
	// Old clients can send without IDs or acknowledgment negotiation.
	if err := protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeMsg, To: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got := nextFrame(t, sender); got.Type != protocol.TypeMsg || got.MessageID == "" || got.AckRequested {
		t.Fatalf("legacy message failed: %+v", got)
	}
	sent, err := sender.SendMessage(context.Background(), "legacy", "review", "", "")
	if err != nil {
		t.Fatal(err)
	}
	assertStage(t, nextFrame(t, sender), sent.MessageID, protocol.StageAccepted)
	if err := protocol.ReadFrame(r, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != protocol.TypeMsg || msg.MessageID != sent.MessageID {
		t.Fatalf("legacy recipient did not receive message: %+v", msg)
	}
	out, err := sender.Receive(context.Background(), 20, 20*time.Millisecond)
	if err != nil || !out.TimedOut {
		t.Fatalf("invented legacy receipt: %+v %v", out, err)
	}
}

func TestOldBrokerCapabilitiesAreExplicit(t *testing.T) {
	c, _ := pipeClient(t) // No negotiated protocol version, like an old welcome.
	out, err := c.Receive(context.Background(), 20, 0)
	if err != nil || out.AcknowledgmentsSupported {
		t.Fatalf("invented capability: %+v %v", out, err)
	}
	if _, err := c.SendMessage(context.Background(), "peer", "review", "", ""); err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("silent send downgrade: %v", err)
	}
	if err := c.Acknowledge(context.Background(), "review"); err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("silent acknowledgment downgrade: %v", err)
	}
}
