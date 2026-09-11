// Package hub implements the central routing goroutine: it owns the agent
// registry, assigns the global message sequence, persists, and fans out.
package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

// Client represents one connected agent.
type Client struct {
	ID         string
	SessionID  string
	Harness    string
	Model      string
	Send       chan protocol.Message // buffered; sent to and closed only by the hub
	Disconnect func()                // closes the transport; must return promptly
}

// Inbound is a message received from a client, tagged with its sender.
type Inbound struct {
	From *Client
	Msg  protocol.Message
}

// Hub routes messages between connected clients. All state mutations happen
// in the single Run goroutine, so no locking is required.
type Hub struct {
	register   chan *Client
	unregister chan *Client
	inbound    chan Inbound
	store      *store.Store
	seq        uint64
	log        *slog.Logger
	done       chan struct{}
}

// New creates a Hub. startSeq is the last persisted sequence number
// (from store.LastSeq), so the global order survives broker restarts.
func New(st *store.Store, startSeq uint64, log *slog.Logger) *Hub {
	return &Hub{
		register:   make(chan *Client),
		unregister: make(chan *Client),
		inbound:    make(chan Inbound, 64),
		store:      st,
		seq:        startSeq,
		log:        log,
		done:       make(chan struct{}),
	}
}

// Register adds a client to the hub. Must be called before sending Inbound.
func (h *Hub) Register(ctx context.Context, c *Client) bool {
	select {
	case <-h.done:
		return false
	case <-ctx.Done():
		return false
	case h.register <- c:
		return true
	}
}

// Unregister removes a client and closes its Send channel.
func (h *Hub) Unregister(c *Client) {
	select {
	case <-h.done:
	case h.unregister <- c:
	}
}

// Submit hands a received message to the hub for routing.
func (h *Hub) Submit(ctx context.Context, in Inbound) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-h.done:
		return false
	default:
	}
	select {
	case <-h.done:
		return false
	case <-ctx.Done():
		return false
	case h.inbound <- in:
		return true
	}
}

// Done closes after all clients have been disconnected and their sessions recorded.
func (h *Hub) Done() <-chan struct{} { return h.done }

// Run processes hub events until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	clients := make(map[string]*Client)
	defer close(h.done)
	defer func() {
		for _, c := range clients {
			h.disconnect(clients, c)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case c := <-h.register:
			if existing, ok := clients[c.ID]; ok {
				select {
				case existing.Send <- protocol.Message{
					Type:   protocol.TypeError,
					Code:   protocol.ErrDuplicateID,
					Detail: "agent id " + c.ID + " taken over by new connection",
				}:
				default:
				}
				h.disconnect(clients, existing)
			}
			clients[c.ID] = c
			h.seq++
			h.recordConnect(c)
			h.enqueue(clients, c, protocol.Message{Type: protocol.TypeWelcome, AgentID: c.ID, Seq: h.seq})
			h.log.Info("agent connected",
				"agent_id", c.ID, "harness", c.Harness, "model", c.Model, "online", len(clients))

		case c := <-h.unregister:
			h.disconnect(clients, c)

		case in := <-h.inbound:
			h.route(ctx, clients, in)
		}
	}
}

// route assigns the global sequence, persists, then delivers the message.
func (h *Hub) route(ctx context.Context, clients map[string]*Client, in Inbound) {
	// A replaced connection may already have submissions in the inbound queue.
	if clients[in.From.ID] != in.From {
		return
	}
	msg := in.Msg
	if msg.Type != protocol.TypeMsg {
		h.sendError(clients, in.From, protocol.ErrMalformedFrame, "expected type \"msg\"")
		return
	}
	if msg.To == "" {
		h.sendError(clients, in.From, protocol.ErrMissingRecipient, "field \"to\" is required")
		return
	}

	h.seq++
	ts := time.Now().UTC()
	out := protocol.Message{
		Type:    protocol.TypeMsg,
		Seq:     h.seq,
		From:    in.From.ID,
		To:      msg.To,
		Payload: msg.Payload,
		TS:      &ts,
	}
	// Broker metadata can make a valid incoming frame too large for a receiver.
	// Reject it before persistence instead of disconnecting the recipient on write.
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded)+1 > protocol.MaxFrameBytes {
		h.sendError(clients, in.From, protocol.ErrMalformedFrame, "routed message exceeds the 1 MiB frame limit")
		return
	}

	if err := h.store.InsertMessage(ctx, out.Seq, *out.TS, out.From, out.To, out.Payload); err != nil {
		h.log.Error("persist message failed", "seq", out.Seq, "error", err)
		h.sendError(clients, in.From, protocol.ErrInternal, "message not persisted")
		return
	}

	if msg.To == protocol.Broadcast {
		delivered := 0
		for id, c := range clients {
			if id == in.From.ID {
				continue // sender already knows its own message
			}
			if h.enqueue(clients, c, out) {
				delivered++
			}
		}
		h.log.Debug("broadcast", "seq", out.Seq, "from", out.From, "delivered", delivered)
		return
	}

	rcpt, ok := clients[msg.To]
	if !ok {
		h.sendError(clients, in.From, protocol.ErrUnknownRecipient, "no connected agent with id "+msg.To)
		return
	}
	if !h.enqueue(clients, rcpt, out) {
		h.sendError(clients, in.From, protocol.ErrRecipientUnavailable, "recipient "+msg.To+" disconnected: outgoing queue full")
		return
	}
	h.log.Debug("direct message", "seq", out.Seq, "from", out.From, "to", out.To)
}

func (h *Hub) sendError(clients map[string]*Client, c *Client, code, detail string) {
	h.enqueue(clients, c, protocol.Message{Type: protocol.TypeError, Code: code, Detail: detail})
}

// enqueue evicts slow clients instead of blocking routing for every agent.
func (h *Hub) enqueue(clients map[string]*Client, c *Client, msg protocol.Message) bool {
	if clients[c.ID] != c {
		return false
	}
	select {
	case c.Send <- msg:
		return true
	default:
		h.log.Warn("disconnecting slow reader", "agent_id", c.ID)
		h.disconnect(clients, c)
		return false
	}
}

func (h *Hub) disconnect(clients map[string]*Client, c *Client) {
	if clients[c.ID] != c {
		return
	}
	delete(clients, c.ID)
	if c.Disconnect != nil {
		c.Disconnect()
	}
	close(c.Send)
	h.recordDisconnect(c)
	h.log.Info("agent disconnected", "agent_id", c.ID, "online", len(clients))
}

func (h *Hub) recordConnect(c *Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.store.RecordConnect(ctx, c.SessionID, c.ID, c.Harness, c.Model, time.Now()); err != nil {
		h.log.Error("record connect failed", "agent_id", c.ID, "error", err)
	}
}

func (h *Hub) recordDisconnect(c *Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.store.RecordDisconnect(ctx, c.SessionID, time.Now()); err != nil {
		h.log.Error("record disconnect failed", "agent_id", c.ID, "error", err)
	}
}
