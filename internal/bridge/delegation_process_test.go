package bridge

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"collab-ai/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Re-exec the test binary as the real stdio adapter used by separate hosts.
func TestDelegatedAdapterProcess(t *testing.T) {
	socket := os.Getenv("COLLAB_DELEGATED_TEST_BROKER")
	if socket == "" {
		return
	}
	c, err := NewLazyClient(ClientConfig{SocketPath: socket, AgentID: "parent"})
	if err != nil {
		os.Exit(1)
	}
	NewMCP(c).Run(context.Background(), &mcp.StdioTransport{})
	c.Close()
	os.Exit(0) // Do not emit the testing package's PASS line on MCP stdout.
}

func startDelegatedAdapter(t *testing.T, socket string) *mcp.ClientSession {
	t.Helper()
	exe, err := os.Executable()
	requireNoError(t, err)
	cmd := exec.Command(exe, "-test.run=^TestDelegatedAdapterProcess$")
	cmd.Env = append(os.Environ(), "COLLAB_DELEGATED_TEST_BROKER="+socket)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "delegation-test"}, nil).Connect(ctx,
		&mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, nil)
	requireNoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestDelegatedSeparateStdioProcesses(t *testing.T) {
	socket, _ := startBroker(t)
	parent := startDelegatedAdapter(t, socket)
	child := startDelegatedAdapter(t, socket)
	initial := inboxResult(t, call(t, parent, "receive", map[string]any{}))
	grant := structuredResult[ListenerGrant](t, call(t, parent, "delegate_listener", map[string]any{}))
	assertEqual(t, "retained parent", grant.SessionID, initial.SessionID)
	peer := connectAgent(t, socket, "peer")
	sendTracked(t, peer, SendRequest{To: "parent", Text: "cross-process", MessageID: "cross-process"})
	assertStage(t, nextFrame(t, peer), "cross-process", protocol.StageAccepted)
	assertStage(t, nextFrame(t, peer), "cross-process", protocol.StageAdapterReceived)
	first := structuredResult[DelegatedInbox](t, call(t, child, "wait_delegated", delegatedArgs(grant, 0)))
	assertEqual(t, "cross-process delivery", len(first.Inbox.Messages), 1)
	child.Close()
	restarted := startDelegatedAdapter(t, socket)
	retry := structuredResult[DelegatedInbox](t, call(t, restarted, "wait_delegated", delegatedArgs(grant, 0)))
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("child exit lost the parent's frames")
	}
	frames := inboxResult(t, call(t, parent, "receive", map[string]any{}))
	if !reflect.DeepEqual(frames.Messages, first.Inbox.Messages) {
		t.Fatal("child consumed parent frames")
	}
	call(t, parent, "acknowledge", map[string]any{"message_id": "cross-process"})
	assertStage(t, nextFrame(t, peer), "cross-process", protocol.StageAgentAcknowledged)
	parent.Close()
	if _, err := os.Stat(grant.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("stdio owner exit did not clean socket: %v", err)
	}
}

func TestDelegationExpiresAndLeavesOwnerConnected(t *testing.T) {
	socket, _ := startBroker(t)
	owner := connectAgent(t, socket, "owner")
	d, err := newListenerDelegation(owner, 100*time.Millisecond)
	requireNoError(t, err)
	t.Cleanup(d.Close)
	_, err = WaitDelegated(context.Background(), d.grant.SocketPath, d.grant.Token, delegatedRead{})
	if err == nil {
		t.Fatal("expired wait succeeded")
	}
	select {
	case <-d.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("grant did not expire")
	}
	d.Close() // Wait for expiry cleanup to finish before inspecting its path.
	if _, err := os.Stat(d.grant.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("expired socket remains: %v", err)
	}
	assertEqual(t, "expiry preserves owner", owner.connectionError(), nil)
}
