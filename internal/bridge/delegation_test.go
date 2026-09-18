package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func structuredResult[T any](t *testing.T, r *mcp.CallToolResult) T {
	t.Helper()
	data, err := json.Marshal(r.StructuredContent)
	requireNoError(t, err)
	var out T
	requireNoError(t, json.Unmarshal(data, &out))
	return out
}

func delegatedArgs(g ListenerGrant, after uint64) map[string]any {
	return map[string]any{"socket_path": g.SocketPath, "token": g.Token, "after_cursor": after, "timeout_seconds": 1}
}

func readGrant(t *testing.T, grant ListenerGrant, after uint64, limit int) DelegatedInbox {
	t.Helper()
	out, err := WaitDelegated(context.Background(), grant.SocketPath, grant.Token,
		delegatedRead{AfterCursor: after, Limit: limit, TimeoutSeconds: 1})
	requireNoError(t, err)
	return out
}

func TestDelegatedMCPListenerKeepsParentOwnershipAndBatch(t *testing.T) {
	path, _ := startBroker(t)
	parent := lazyAgent(t, path, "codex-etl")
	parentMCP := connectMCP(t, parent)
	initial := inboxResult(t, call(t, parentMCP, "receive", map[string]any{}))
	grant := structuredResult[ListenerGrant](t, call(t, parentMCP, "delegate_listener", map[string]any{}))
	assertEqual(t, "unchanged owner", grant.SessionID, initial.SessionID)
	// The child really has a separate adapter with the same inherited config.
	child := lazyAgent(t, path, "codex-etl")
	childMCP := connectMCP(t, child)
	peer := connectAgent(t, path, "claude")
	for i := range 3 {
		id := fmt.Sprintf("review-%d", i)
		sendTracked(t, peer, SendRequest{To: "codex-etl", Text: id, MessageID: id, InReplyTo: "context"})
		assertStage(t, nextFrame(t, peer), id, protocol.StageAccepted)
		assertStage(t, nextFrame(t, peer), id, protocol.StageAdapterReceived)
	}
	batch := structuredResult[DelegatedInbox](t, call(t, childMCP, "wait_delegated", delegatedArgs(grant, 0)))
	assertEqual(t, "all batch frames", len(batch.Inbox.Messages), 3)
	assertEqual(t, "owner session", batch.Inbox.SessionID, initial.SessionID)
	if child.client != nil {
		t.Fatal("delegated wait registered a second broker connection")
	}
	// Repeating after a lost tool response or child restart replays the same copy.
	childMCP.Close()
	child.Close()
	restarted := connectMCP(t, lazyAgent(t, path, "codex-etl"))
	repeat := structuredResult[DelegatedInbox](t, call(t, restarted, "wait_delegated", delegatedArgs(grant, 0)))
	if !reflect.DeepEqual(repeat, batch) {
		t.Fatalf("listener restart changed batch: %+v != %+v", repeat, batch)
	}
	// Neither reading nor retrying has acknowledged anything at the broker.
	assertInboxTimeout(t, peer)
	parentBatch := inboxResult(t, call(t, parentMCP, "receive", map[string]any{}))
	if !reflect.DeepEqual(parentBatch.Messages, batch.Inbox.Messages) {
		t.Fatal("listener consumed or changed the parent's frames")
	}
	for _, msg := range parentBatch.Messages {
		assertEqual(t, "reply reference", msg.InReplyTo, "context")
		assertEqual(t, "sender session", msg.SessionID, peer.sessionID)
		if msg.Seq == 0 || msg.TS == nil {
			t.Fatal("missing broker provenance")
		}
		call(t, parentMCP, "acknowledge", map[string]any{"message_id": msg.MessageID})
		assertStage(t, nextFrame(t, peer), msg.MessageID, protocol.StageAgentAcknowledged)
	}
	assertOwnerRejected(t, lazyAgent(t, path, "codex-etl"), parent.client)
	call(t, parentMCP, "revoke_listener", map[string]any{})
	assertEqual(t, "parent still connected", parent.client.connectionError(), nil)
}

func TestDelegatedReadCannotAcknowledgeOrAccessAnotherInbox(t *testing.T) {
	path, _ := startBroker(t)
	owner := lazyAgent(t, path, "owner")
	grant, err := owner.DelegateListener(context.Background())
	requireNoError(t, err)
	other := lazyAgent(t, path, "other")
	otherGrant, err := other.DelegateListener(context.Background())
	requireNoError(t, err)
	for _, token := range []string{strings.Repeat("0", 64), otherGrant.Token} {
		_, err := WaitDelegated(context.Background(), grant.SocketPath, token, delegatedRead{})
		assertErrorContains(t, err, "403")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", grant.SocketPath)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	for _, request := range []struct {
		path, body string
		status     int
	}{
		{"/acknowledge", `{"message_id":"private"}`, 404},
		{"/send", `{"to":"other","text":"no"}`, 404},
		{"/wait", `{"agent_id":"other"}`, 400},
		{"/wait", `{"stage":"agent_acknowledged"}`, 400},
		{"/wait", `{} {}`, 400},
		{"/wait", `{"limit":101}`, 400},
		{"/wait", `{"timeout_seconds":31}`, 400},
		{"/wait", strings.Repeat(" ", 1025) + `{}`, 400},
	} {
		r, err := http.NewRequest(http.MethodPost, "http://listener"+request.path, strings.NewReader(request.body))
		requireNoError(t, err)
		r.Header.Set("Authorization", "Bearer "+grant.Token)
		resp, err := (&http.Client{Transport: transport, Timeout: time.Second}).Do(r)
		requireNoError(t, err)
		resp.Body.Close()
		assertEqual(t, "request rejected", resp.StatusCode, request.status)
	}
	for _, path := range []string{grant.SocketPath, filepath.Dir(grant.SocketPath)} {
		info, err := os.Stat(path)
		requireNoError(t, err)
		assertEqual(t, "no access for other users", info.Mode().Perm()&0077, 0)
	}
}

func TestDelegationConcurrentWaitCancellationAndRotation(t *testing.T) {
	path, _ := startBroker(t)
	owner := lazyAgent(t, path, "owner")
	grant, err := owner.DelegateListener(context.Background())
	requireNoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := WaitDelegated(ctx, grant.SocketPath, grant.Token, delegatedRead{})
		done <- err
	}()
	awaitDelegationBusy(t, owner.delegation, true)
	_, err = WaitDelegated(context.Background(), grant.SocketPath, grant.Token, delegatedRead{})
	assertErrorContains(t, err, "409")
	cancel()
	assertErrorContains(t, <-done, "context canceled")
	awaitDelegationBusy(t, owner.delegation, false)
	// Rotation also cancels an outstanding wait, while leaving the owner live.
	go func() {
		_, err := WaitDelegated(context.Background(), grant.SocketPath, grant.Token, delegatedRead{})
		done <- err
	}()
	awaitDelegationBusy(t, owner.delegation, true)
	next, err := owner.DelegateListener(context.Background())
	requireNoError(t, err)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked wait succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("revocation did not wake the listener")
	}
	_, err = WaitDelegated(context.Background(), next.SocketPath, grant.Token, delegatedRead{})
	assertErrorContains(t, err, "403")
	_, err = os.Stat(grant.SocketPath)
	if !os.IsNotExist(err) {
		t.Fatalf("revoked socket still exists: %v", err)
	}
	assertEqual(t, "same broker session", next.SessionID, grant.SessionID)
	owner.Close()
	_, err = os.Stat(next.SocketPath)
	if !os.IsNotExist(err) {
		t.Fatalf("closed owner left listener socket: %v", err)
	}
}

func awaitDelegationBusy(t *testing.T, d *listenerDelegation, want bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for (len(d.busy) != 0) != want {
		if time.Now().After(deadline) {
			t.Fatal("delegation wait did not change state")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDelegatedCursorParentConsumptionAndDisconnect(t *testing.T) {
	path, stop := startBroker(t)
	owner := lazyAgent(t, path, "owner")
	grant, err := owner.DelegateListener(context.Background())
	requireNoError(t, err)
	peer := connectAgent(t, path, "peer")
	for i := range 3 {
		id := fmt.Sprintf("cursor-%d", i)
		sendTracked(t, peer, SendRequest{To: "owner", Text: id, MessageID: id})
		nextFrame(t, peer)
		nextFrame(t, peer)
	}
	first := readGrant(t, grant, 0, 1)
	assertEqual(t, "first ID", first.Inbox.Messages[0].MessageID, "cursor-0")
	second := readGrant(t, grant, first.NextCursor, 1)
	assertEqual(t, "second ID", second.Inbox.Messages[0].MessageID, "cursor-1")
	parent, err := owner.Receive(context.Background(), 3, 0)
	requireNoError(t, err)
	assertEqual(t, "parent retains all", len(parent.Messages), 3)
	// A consumed parent frame need not also be relayed by a child.
	empty := readGrant(t, grant, second.NextCursor, 20)
	assertEqual(t, "parent consumed", len(empty.Inbox.Messages), 0)
	assertEqual(t, "empty wait", empty.Inbox.TimedOut, true)
	_, err = WaitDelegated(context.Background(), grant.SocketPath, grant.Token, delegatedRead{AfterCursor: empty.NextCursor + 1})
	assertErrorContains(t, err, "cursor is ahead")
	stop()
	dead := readGrant(t, grant, empty.NextCursor, 20)
	assertEqual(t, "disconnect reported", dead.Inbox.Connected, false)
	if dead.Inbox.Error == "" {
		t.Fatal("missing disconnect reason")
	}
}

func TestDelegatedFramesReplayAfterOwnerCrash(t *testing.T) {
	socket := crashSocket(t)
	stop := startCrashBroker(t, socket)
	parent := lazyAgent(t, socket, "parent")
	grant, err := parent.DelegateListener(context.Background())
	requireNoError(t, err)
	peer := connectAgent(t, socket, "peer")
	sendTracked(t, peer, SendRequest{To: "parent", Text: "retained", MessageID: "retained"})
	assertStage(t, nextFrame(t, peer), "retained", protocol.StageAccepted)
	assertStage(t, nextFrame(t, peer), "retained", protocol.StageAdapterReceived)
	first := readGrant(t, grant, 0, 20)
	assertEqual(t, "observed ID", first.Inbox.Messages[0].MessageID, "retained")
	stop()
	parent.Close()
	peer.Close()
	startCrashBroker(t, socket)
	restarted := lazyAgent(t, socket, "parent")
	recovered, err := restarted.DelegateListener(context.Background())
	requireNoError(t, err)
	replay := readGrant(t, recovered, 0, 20)
	assertEqual(t, "durable replay", replay.Inbox.Messages[0].Replayed, true)
	assertEqual(t, "same ID", replay.Inbox.Messages[0].MessageID, "retained")
	assertEqual(t, "same sequence", replay.Inbox.Messages[0].Seq, first.Inbox.Messages[0].Seq)
	assertEqual(t, "same timestamp", replay.Inbox.Messages[0].TS.Equal(*first.Inbox.Messages[0].TS), true)
	requireNoError(t, restarted.Acknowledge(context.Background(), "retained"))
	inbox, err := restarted.Receive(context.Background(), 1, 0)
	requireNoError(t, err)
	assertEqual(t, "parent still owns replay", inbox.Messages[0].MessageID, "retained")
	assertStage(t, nextFrame(t, restarted.client), "retained", protocol.StageAgentAcknowledged)
}

func TestDelegatedWaitWakesOnErrorsAndRetainsBoundedInbox(t *testing.T) {
	path, _ := startBroker(t)
	owner := lazyAgent(t, path, "owner")
	grant, err := owner.DelegateListener(context.Background())
	requireNoError(t, err)
	// The endpoint must wake on asynchronous errors, not only peer messages.
	ready := make(chan DelegatedInbox, 1)
	failure := make(chan error, 1)
	go func() {
		out, err := WaitDelegated(context.Background(), grant.SocketPath, grant.Token, delegatedRead{})
		ready <- out
		failure <- err
	}()
	awaitDelegationBusy(t, owner.delegation, true)
	_, err = owner.SendMessage(context.Background(), SendRequest{To: "unknown", Text: "missing", MessageID: "missing"})
	requireNoError(t, err)
	select {
	case out := <-ready:
		requireNoError(t, <-failure)
		assertEqual(t, "error batch size", len(out.Inbox.Messages), 1)
		assertEqual(t, "broker error", out.Inbox.Messages[0].Code, protocol.ErrUnknownRecipient)
		assertEqual(t, "correlated error", out.Inbox.Messages[0].MessageID, "missing")
	case <-time.After(time.Second):
		t.Fatal("delegated wait did not wake on a broker error")
	}
	inbox, err := owner.Receive(context.Background(), 1, 0)
	requireNoError(t, err)
	assertEqual(t, "parent retains error", inbox.Messages[0].MessageID, "missing")
	peer := connectAgent(t, path, "peer")
	// Non-durable frames can fill the adapter beyond the durable broker quota.
	for range maxInboxMessages + 1 {
		if err := peer.Send(context.Background(), "owner", "bounded"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-owner.client.done:
	case <-time.After(3 * time.Second):
		t.Fatal("parent inbox did not report overflow")
	}
	out := readGrant(t, grant, 0, 100)
	assertEqual(t, "overflow disconnected", out.Inbox.Connected, false)
	if !strings.Contains(out.Inbox.Error, "overflow") {
		t.Fatalf("missing overflow: %+v", out.Inbox)
	}
	owner.client.mu.Lock()
	retained := len(owner.client.inbox)
	owner.client.mu.Unlock()
	assertEqual(t, "bounded retained queue", retained, maxInboxMessages)
}
