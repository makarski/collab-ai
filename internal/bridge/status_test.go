package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
	"collab-ai/internal/status"
)

func brokerStatus(t *testing.T, socket string) protocol.StatusSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := status.Fetch(ctx, socket)
	requireNoError(t, err)
	return out
}

func TestStatusWireInspectionLeavesOwnersAndInboxIntact(t *testing.T) {
	socket, _ := startBroker(t)
	initial := brokerStatus(t, socket)
	assertEqual(t, "no inspection session", *initial.ConnectedSessions, 0)
	parent := connectAgent(t, socket, "codex")
	peer := connectAgent(t, socket, "claude")
	sendTracked(t, peer, SendRequest{To: "codex", Text: "SECRET_REVIEW_TEXT", MessageID: "inspect-pending"})
	assertStage(t, nextFrame(t, peer), "inspect-pending", protocol.StageAccepted)
	assertStage(t, nextFrame(t, peer), "inspect-pending", protocol.StageAdapterReceived)
	for range 3 {
		out := brokerStatus(t, socket)
		assertEqual(t, "owners preserved", *out.ConnectedSessions, 2)
		assertEqual(t, "pending untouched", out.DurablePending.Total, 1)
		data, err := json.Marshal(out)
		requireNoError(t, err)
		if strings.Contains(string(data), "SECRET_REVIEW_TEXT") {
			t.Fatal("status exposed a body")
		}
	}
	assertInboxTimeout(t, peer)
	assertEqual(t, "recipient can consume", nextFrame(t, parent).MessageID, "inspect-pending")
	requireNoError(t, parent.Acknowledge(context.Background(), "inspect-pending"))
	assertStage(t, nextFrame(t, peer), "inspect-pending", protocol.StageAgentAcknowledged)
	assertStage(t, nextFrame(t, parent), "inspect-pending", protocol.StageAgentAcknowledged)
	assertEqual(t, "ack reflected", brokerStatus(t, socket).DurablePending.Total, 0)
	assertOwnerRejected(t, lazyAgent(t, socket, "codex"), parent)
}

func TestStatusConnectionCannotPipelineRegistration(t *testing.T) {
	socket, _ := startBroker(t)
	parent := connectAgent(t, socket, "parent")
	conn, err := net.Dial("unix", socket)
	requireNoError(t, err)
	defer conn.Close()
	requireNoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	_, err = conn.Write([]byte("{\"type\":\"status\",\"agent_id\":\"parent\"}\n{\"type\":\"hello\",\"agent_id\":\"intruder\"}\n"))
	requireNoError(t, err)
	r := bufio.NewReader(conn)
	var reply protocol.Message
	requireNoError(t, protocol.ReadFrame(r, &reply))
	assertEqual(t, "response", reply.Type, protocol.TypeStatus)
	if err := protocol.ReadFrame(r, &reply); err == nil {
		t.Fatal("status connection accepted another request")
	}
	out := brokerStatus(t, socket)
	assertEqual(t, "only parent registered", *out.ConnectedSessions, 1)
	assertEqual(t, "same owner", out.Sessions[0].SessionID, parent.sessionID)
	assertEqual(t, "no probe history", len(out.Sessions), 1)
}

func TestStatusAfterBrokerCrashDoesNotInventLiveSessions(t *testing.T) {
	socket := crashSocket(t)
	stop := startCrashBroker(t, socket)
	parent := connectAgent(t, socket, "parent")
	sendTracked(t, parent, SendRequest{To: "parent", Text: "pending after crash", MessageID: "crashed"})
	assertStage(t, nextFrame(t, parent), "crashed", protocol.StageAccepted)
	stop()
	parent.Close()
	startCrashBroker(t, socket)
	out := brokerStatus(t, socket)
	assertEqual(t, "no live session", *out.ConnectedSessions, 0)
	assertEqual(t, "retained history", len(out.Sessions), 1)
	assertEqual(t, "unclean disconnect", out.Sessions[0].State, "stale")
	assertEqual(t, "pending survived", out.DurablePending.Total, 1)
	recovered := connectAgent(t, socket, "parent")
	assertEqual(t, "inspection did not claim replay", nextFrame(t, recovered).Replayed, true)
}
