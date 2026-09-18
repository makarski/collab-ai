package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const delegationLifetime = 15 * time.Minute

// ListenerGrant is a read-only local capability. It must only be passed to the
// selected listener, never sent to peers or committed to configuration.
type ListenerGrant struct {
	SocketPath string    `json:"socket_path"`
	Token      string    `json:"token"`
	ExpiresAt  time.Time `json:"expires_at"`
	SessionID  string    `json:"session_id"`
}

type listenerDelegation struct {
	grant  ListenerGrant
	client *Client
	ctx    context.Context
	cancel context.CancelFunc
	server *http.Server
	ln     net.Listener
	busy   chan struct{}
	once   sync.Once
}

// DelegateListener explicitly registers the owner, then creates one capability.
// Rotation revokes the previous capability and any outstanding wait, not the
// owner's broker connection. Discovery never calls this method.
func (c *LazyClient) DelegateListener(ctx context.Context) (ListenerGrant, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return ListenerGrant{}, err
	}
	select {
	case <-ctx.Done():
		return ListenerGrant{}, ctx.Err()
	case c.gate <- struct{}{}:
	}
	defer func() { <-c.gate }()
	if err := ctx.Err(); err != nil {
		return ListenerGrant{}, err
	}
	if c.closed {
		return ListenerGrant{}, errors.New("MCP connection closed")
	}
	return c.replaceDelegation(client)
}

// replaceDelegation runs while the lazy connection gate is held, so shutdown
// cannot race grant creation and only one returned capability remains active.
func (c *LazyClient) replaceDelegation(client *Client) (ListenerGrant, error) {
	if err := client.connectionError(); err != nil {
		return ListenerGrant{}, err
	}
	next, err := newListenerDelegation(client, delegationLifetime)
	if err != nil {
		return ListenerGrant{}, err
	}
	if c.delegation != nil {
		c.delegation.Close()
	}
	c.delegation = next
	return next.grant, nil
}

func (c *LazyClient) RevokeListener(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.gate <- struct{}{}:
	}
	defer func() { <-c.gate }()
	if c.delegation != nil {
		c.delegation.Close()
		c.delegation = nil
	}
	return nil
}

func newListenerDelegation(client *Client, lifetime time.Duration) (*listenerDelegation, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	ln, path, err := listenPrivateSocket()
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(lifetime)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	d := &listenerDelegation{
		grant:  ListenerGrant{SocketPath: path, Token: hex.EncodeToString(secret[:]), ExpiresAt: expires, SessionID: client.sessionID},
		client: client, ctx: ctx, cancel: cancel, ln: ln, busy: make(chan struct{}, 1),
	}
	d.server = &http.Server{
		Handler: http.HandlerFunc(d.serveWait), ReadHeaderTimeout: ioTimeout,
		ReadTimeout: ioTimeout, WriteTimeout: 35 * time.Second, MaxHeaderBytes: 4096,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	d.server.SetKeepAlivesEnabled(false)
	go func() {
		// Limit unauthenticated local connections as well as active waits.
		d.server.Serve(newBoundedListener(ln, 8))
		d.Close()
	}()
	go func() {
		<-ctx.Done()
		d.Close()
	}()
	return d, nil
}

func listenPrivateSocket() (net.Listener, string, error) {
	// A short path is required on macOS; os.TempDir can exceed the UDS limit.
	dir, err := os.MkdirTemp("/tmp", "collab-listener-")
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, "listen.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		os.Remove(dir)
		return nil, "", err
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		os.Remove(dir)
		return nil, "", err
	}
	return ln, path, nil
}

func (d *listenerDelegation) Close() {
	d.once.Do(func() {
		d.cancel()
		d.server.Close()
		d.ln.Close()
		// The listener removes its own socket; never recursively remove files.
		os.Remove(filepath.Dir(d.grant.SocketPath))
	})
}

type boundedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

func newBoundedListener(ln net.Listener, n int) *boundedListener {
	return &boundedListener{Listener: ln, slots: make(chan struct{}, n), done: make(chan struct{})}
}

func (l *boundedListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case l.slots <- struct{}{}:
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &boundedConn{Conn: c, release: func() { <-l.slots }}, nil
}

func (l *boundedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type boundedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *boundedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
