package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectAutoChannel(t *testing.T, l *Listener) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewAutoChannelMCP(l).Connect(ctx, &ChannelTransport{Transport: st}, nil)
	requireNoError(t, err)
	t.Cleanup(func() { ss.Close() })
	assertEqual(t, "not active before initialized", l.Status().State, "inactive")
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "managed-host"}, nil).Connect(ctx, ct, nil)
	requireNoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestAutoChannelStartsWithoutListenOrReceive(t *testing.T) {
	path, _ := startBroker(t)
	p := &testPublisher{messages: make(chan protocol.Message, 1)}
	l := NewListener(context.Background(), lazyAgent(t, path, "worker"), p)
	t.Cleanup(l.Close)
	connectAutoChannel(t, l)
	awaitListener(t, l, func() bool { return l.started })
	peer := connectAgent(t, path, "peer")
	sendTracked(t, peer, SendRequest{To: "worker", Text: "no registration prompt", MessageID: "auto"})
	assertEqual(t, "automatic submission", awaitPublished(t, p).MessageID, "auto")
	assertEqual(t, "still not proof of host attention", l.Status().ManualCheckRequired, true)
}

func TestAutoChannelDuplicateIdentityIsVisibleAndOwnerSurvives(t *testing.T) {
	path, _ := startBroker(t)
	owner := connectAgent(t, path, "worker")
	l := NewListener(context.Background(), lazyAgent(t, path, "worker"), nil)
	t.Cleanup(l.Close)
	connectAutoChannel(t, l)
	awaitListener(t, l, func() bool { return strings.Contains(l.status.Error, protocol.ErrDuplicateID) })
	assertEqual(t, "failed activation inactive", l.Status().State, "inactive")
	assertEqual(t, "owner preserved", owner.connectionError(), nil)
}
