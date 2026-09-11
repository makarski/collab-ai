// Package bridge connects an MCP process to the broker as one persistent agent.
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"collab-ai/internal/protocol"
)

const (
	ioTimeout        = 5 * time.Second
	maxInboxMessages = 256
	maxInboxBytes    = 4 << 20
)

type queuedMessage struct {
	message protocol.Message
	size    int
}

// Client reads the socket continuously, independently of MCP tool calls.
// A broken connection is terminal: reconnecting without replay would hide gaps.
type Client struct {
	conn       net.Conn
	writeGate  chan struct{}
	notify     chan struct{}
	done       chan struct{}
	readDone   chan struct{}
	mu         sync.Mutex
	inbox      []queuedMessage
	inboxBytes int
	err        error
}

type Inbox struct {
	Messages  []protocol.Message `json:"messages"`
	Connected bool               `json:"connected"`
	Error     string             `json:"error,omitempty"`
	TimedOut  bool               `json:"timed_out,omitempty"`
}

func validateAgentID(agentID string) error {
	if agentID == "" || agentID == protocol.Broadcast {
		return errors.New("agent ID must be nonempty and cannot be *")
	}
	return nil
}

// Dial registers the agent before returning. The caller must call Close.
func Dial(ctx context.Context, socketPath, agentID, harness, model string) (*Client, error) {
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: ioTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to broker: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(ioTimeout))
	if err := protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeHello, AgentID: agentID, Harness: harness, Model: model}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}
	r := bufio.NewReader(conn)
	var welcome protocol.Message
	if err := protocol.ReadFrame(r, &welcome); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read welcome: %w", err)
	}
	if welcome.Type != protocol.TypeWelcome || welcome.AgentID != agentID {
		conn.Close()
		return nil, fmt.Errorf("unexpected welcome: type=%s code=%s detail=%s", welcome.Type, welcome.Code, welcome.Detail)
	}
	conn.SetDeadline(time.Time{})
	c := &Client{conn: conn, writeGate: make(chan struct{}, 1), notify: make(chan struct{}, 1), done: make(chan struct{}), readDone: make(chan struct{})}
	go c.readLoop(r)
	return c, nil
}

func (c *Client) readLoop(r *bufio.Reader) {
	defer close(c.readDone)
	for {
		var msg protocol.Message
		if err := protocol.ReadFrame(r, &msg); err != nil {
			c.fail(fmt.Errorf("broker connection closed: %w; restart the MCP server to reconnect (missed messages are not replayed)", err))
			return
		}
		data, _ := json.Marshal(msg)
		c.mu.Lock()
		if len(c.inbox) >= maxInboxMessages || c.inboxBytes+len(data) > maxInboxBytes {
			c.mu.Unlock()
			c.fail(errors.New("inbox overflow; connection closed and messages may be missing; drain the inbox and restart the MCP server"))
			return
		}
		c.inbox = append(c.inbox, queuedMessage{message: msg, size: len(data)})
		c.inboxBytes += len(data)
		c.mu.Unlock()
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	close(c.done)
	c.mu.Unlock()
	c.conn.Close()
}

func (c *Client) Close() {
	c.fail(errors.New("MCP connection closed"))
	<-c.readDone
}

// Send reports only successful socket writes. Broker errors arrive in the inbox;
// the current wire protocol has no delivery acknowledgement.
func (c *Client) Send(ctx context.Context, to, text string) error {
	if to == "" {
		return errors.New("to is required (agent ID or *)")
	}
	if text == "" {
		return errors.New("text must not be empty")
	}
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{text})
	if err != nil {
		return err
	}
	msg := protocol.Message{Type: protocol.TypeMsg, To: to, Payload: payload}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(data)+1 > protocol.MaxFrameBytes {
		return errors.New("message exceeds the 1 MiB frame limit")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.connectionError()
	case c.writeGate <- struct{}{}:
	}
	defer func() { <-c.writeGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.connectionError(); err != nil {
		return err
	}
	deadline := time.Now().Add(ioTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		c.fail(err)
		return err
	}
	// Cancellation during a write closes the stream: a partial frame cannot be retried safely.
	stop := context.AfterFunc(ctx, func() { c.fail(ctx.Err()) })
	defer stop()
	if err := protocol.WriteFrame(c.conn, msg); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

func (c *Client) connectionError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Receive consumes up to limit queued frames, waiting only when the inbox is empty.
func (c *Client) Receive(ctx context.Context, limit int, wait time.Duration) (Inbox, error) {
	if limit < 1 || limit > 100 {
		return Inbox{}, errors.New("limit must be between 1 and 100")
	}
	if wait < 0 || wait > 30*time.Second {
		return Inbox{}, errors.New("wait must be between 0 and 30 seconds")
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return Inbox{}, err
		}
		c.mu.Lock()
		out := Inbox{Messages: make([]protocol.Message, 0), Connected: c.err == nil}
		if c.err != nil {
			out.Error = c.err.Error()
		}
		n := min(limit, len(c.inbox))
		for _, q := range c.inbox[:n] {
			out.Messages = append(out.Messages, q.message)
			c.inboxBytes -= q.size
		}
		clear(c.inbox[:n])
		c.inbox = c.inbox[n:]
		c.mu.Unlock()
		if len(out.Messages) > 0 || !out.Connected || wait == 0 {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return Inbox{}, ctx.Err()
		case <-c.done:
		case <-c.notify:
		case <-timer.C:
			// Check the queue once more so a simultaneous delivery is not left behind.
			out, err := c.Receive(ctx, limit, 0)
			out.TimedOut = len(out.Messages) == 0 && out.Connected
			return out, err
		}
	}
}
