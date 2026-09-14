// Package hub implements the central routing goroutine: it owns the agent
// registry, assigns the global message sequence, persists, and fans out.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"time"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"

	"github.com/google/uuid"
)

// Client represents one connected agent.
type Client struct {
	ID              string
	SessionID       string
	Harness         string
	Model           string
	ProtocolVersion int
	Send            chan protocol.Message // buffered; sent to and closed only by the hub
	Disconnect      func()                // closes the transport; must return promptly
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
				// The rejected connection's writer drains this error before closing
				// its transport. Never close the owner's connection or inbox.
				c.Send <- protocol.Message{
					Type:           protocol.TypeError,
					Code:           protocol.ErrDuplicateID,
					AgentID:        c.ID,
					SessionID:      c.SessionID,
					OwnerSessionID: existing.SessionID,
					Detail:         "agent id " + c.ID + " is owned by session " + existing.SessionID + "; disconnect that owner before retrying, or use a distinct agent_id",
				}
				close(c.Send)
				continue
			}
			clients[c.ID] = c
			h.seq++
			h.recordConnect(c)
			h.enqueue(clients, c, protocol.Message{Type: protocol.TypeWelcome, AgentID: c.ID, SessionID: c.SessionID, Seq: h.seq, ProtocolVersion: protocol.Version})
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
	if clients[in.From.ID] != in.From {
		return
	}
	msg := in.Msg
	if msg.Type == protocol.TypeAck {
		h.acknowledge(ctx, clients, in)
		return
	}
	fail := func(code, detail string) { h.sendError(clients, in.From, code, detail, msg.MessageID) }
	if msg.Type != protocol.TypeMsg {
		fail(protocol.ErrMalformedFrame, "expected type msg or ack")
		return
	}
	if msg.MessageID == "" {
		msg.MessageID = uuid.NewString()
	}
	if len(msg.MessageID) > 128 || len(msg.InReplyTo) > 128 {
		fail(protocol.ErrMalformedFrame, "message_id and in_reply_to must be at most 128 bytes")
		return
	}
	if msg.AckRequested && in.From.ProtocolVersion < protocol.Version {
		fail(protocol.ErrMalformedFrame, "ack_requested requires protocol_version 2 in hello")
		return
	}
	if msg.To == "" {
		fail(protocol.ErrMissingRecipient, "field to is required")
		return
	}
	var recipients []protocol.Recipient
	if msg.To == protocol.Broadcast {
		for id, c := range clients {
			if id != in.From.ID {
				recipients = append(recipients, protocol.Recipient{AgentID: id, SessionID: c.SessionID})
			}
		}
		if len(recipients) > 256 {
			fail(protocol.ErrRecipientUnavailable, "broadcast exceeds 256 connected recipients; send to smaller groups directly")
			return
		}
		sort.Slice(recipients, func(i, j int) bool { return recipients[i].AgentID < recipients[j].AgentID })
	} else {
		c := clients[msg.To]
		if c == nil {
			fail(protocol.ErrUnknownRecipient, "no connected agent with id "+msg.To)
			return
		}
		recipients = append(recipients, protocol.Recipient{AgentID: c.ID, SessionID: c.SessionID})
	}
	h.seq++
	ts := time.Now().UTC()
	out := protocol.Message{
		Type: protocol.TypeMsg, Seq: h.seq, TS: &ts,
		From: in.From.ID, SessionID: in.From.SessionID, To: msg.To,
		Payload: msg.Payload, MessageID: msg.MessageID,
		InReplyTo: msg.InReplyTo, AckRequested: msg.AckRequested,
	}
	// Added broker metadata must still fit the receiver's frame limit.
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded)+1 > protocol.MaxFrameBytes {
		fail(protocol.ErrMalformedFrame, "routed message exceeds the 1 MiB frame limit")
		return
	}
	if err := h.store.PersistMessage(ctx, out, in.From.SessionID, recipients); err != nil {
		if errors.Is(err, store.ErrDuplicateMessage) {
			fail(protocol.ErrDuplicateMessage, err.Error())
			return
		}
		h.log.Error("persist message failed", "message_id", msg.MessageID, "error", err)
		fail(protocol.ErrInternal, "message not persisted")
		return
	}
	if msg.AckRequested {
		h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: protocol.StageAccepted, Seq: out.Seq, Recipients: recipients})
	}
	for _, r := range recipients {
		c := clients[r.AgentID]
		if c == nil || c.SessionID != r.SessionID || !h.enqueue(clients, c, out) {
			h.sendError(clients, in.From, protocol.ErrRecipientUnavailable, "recipient "+r.AgentID+" session "+r.SessionID+" disconnected before enqueue", msg.MessageID)
		}
	}
}

func (h *Hub) acknowledge(ctx context.Context, clients map[string]*Client, in Inbound) {
	msg := in.Msg
	sender, session, err := h.store.Acknowledge(ctx, msg.MessageID, in.From.ID, in.From.SessionID, msg.Stage)
	if err != nil {
		code, detail := protocol.ErrInternal, "acknowledgment not persisted"
		if errors.Is(err, store.ErrInvalidAck) {
			code, detail = protocol.ErrInvalidAck, err.Error()
		}
		h.sendError(clients, in.From, code, detail, msg.MessageID)
		return
	}
	out := protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: msg.Stage, AgentID: in.From.ID, SessionID: in.From.SessionID}
	// Adapter receipts are confirmed to the sender. Explicit agent acknowledgments
	// are also confirmed to the acknowledging agent, only after the database commit.
	if msg.Stage == protocol.StageAgentAcknowledged {
		h.enqueue(clients, in.From, out)
	}
	if c := clients[sender]; c != nil && c.SessionID == session && (c != in.From || msg.Stage != protocol.StageAgentAcknowledged) {
		h.enqueue(clients, c, out)
	}
}

func (h *Hub) sendError(clients map[string]*Client, c *Client, code, detail, id string) {
	h.enqueue(clients, c, protocol.Message{Type: protocol.TypeError, Code: code, Detail: detail, MessageID: id})
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
	h.notifyDisconnect(clients, c)
	h.log.Info("agent disconnected", "agent_id", c.ID, "online", len(clients))
}

func (h *Hub) notifyDisconnect(clients map[string]*Client, c *Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var after uint64
	for len(clients) > 0 {
		pending, err := h.store.UnacknowledgedForSession(ctx, c.SessionID, after)
		if err != nil {
			h.log.Error("query unacknowledged receipts failed", "session_id", c.SessionID, "error", err)
			// A session-level error makes failure to enumerate affected messages
			// visible without inventing a delivery outcome.
			for _, sender := range clients {
				h.enqueue(clients, sender, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, AgentID: c.ID, SessionID: c.SessionID, Detail: "recipient disconnected; could not determine unacknowledged messages"})
			}
			return
		}
		for _, p := range pending {
			after = p.Seq
			if sender := clients[p.Sender]; sender != nil && sender.SessionID == p.SenderSession {
				h.enqueue(clients, sender, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrRecipientDisconnected, MessageID: p.MessageID, AgentID: c.ID, SessionID: c.SessionID, Detail: "recipient session disconnected before agent acknowledgment; no replay is available"})
			}
		}
		if len(pending) < 64 {
			return
		}
	}
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
