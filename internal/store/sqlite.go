// Package store persists conversation state (agents and messages) in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"collab-ai/internal/protocol"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS agents (
    agent_id        TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    harness         TEXT,
    model           TEXT,
    connected_at    TIMESTAMP NOT NULL,
    disconnected_at TIMESTAMP,
    PRIMARY KEY (agent_id, session_id)
);

CREATE TABLE IF NOT EXISTS messages (
    seq       INTEGER PRIMARY KEY,
    ts        TIMESTAMP NOT NULL,
    sender    TEXT NOT NULL,
    recipient TEXT NOT NULL,
    payload   TEXT NOT NULL
);

-- Additive migration: existing history remains readable and unchanged.
CREATE TABLE IF NOT EXISTS message_metadata (
    message_id TEXT PRIMARY KEY,
    seq INTEGER NOT NULL UNIQUE,
    sender_session TEXT NOT NULL,
    in_reply_to TEXT NOT NULL,
    ack_requested INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS message_receipts (
    message_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    adapter_received_at TIMESTAMP,
    agent_acknowledged_at TIMESTAMP,
    PRIMARY KEY (message_id, agent_id)
);
CREATE INDEX IF NOT EXISTS receipts_session ON message_receipts(session_id);
`

// Store wraps the SQLite database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single writer keeps things simple and avoids SQLITE_BUSY under the hub's
	// serialized write pattern.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// LastSeq returns the highest message sequence number persisted so far,
// or 0 if no messages exist. Used to resume the monotonic sequence across restarts.
func (s *Store) LastSeq(ctx context.Context) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM messages`).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("query last seq: %w", err)
	}
	return seq, nil
}

// RecordConnect inserts an agent session row.
func (s *Store) RecordConnect(ctx context.Context, sessionID, agentID, harness, model string, ts time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (agent_id, session_id, harness, model, connected_at) VALUES (?, ?, ?, ?, ?)`,
		agentID, sessionID, harness, model, ts.UTC())
	if err != nil {
		return fmt.Errorf("record connect: %w", err)
	}
	return nil
}

// RecordDisconnect stamps the session's disconnected_at.
func (s *Store) RecordDisconnect(ctx context.Context, sessionID string, ts time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET disconnected_at = ? WHERE session_id = ?`,
		ts.UTC(), sessionID)
	if err != nil {
		return fmt.Errorf("record disconnect: %w", err)
	}
	return nil
}

// InsertMessage persists a routed message. seq must be unique and monotonically increasing.
func (s *Store) InsertMessage(ctx context.Context, seq uint64, ts time.Time, from, to string, payload []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (seq, ts, sender, recipient, payload) VALUES (?, ?, ?, ?, ?)`,
		seq, ts.UTC(), from, to, string(payload))
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

var ErrDuplicateMessage = errors.New("message ID already exists; this send was not routed again")
var ErrInvalidAck = errors.New("message is not assigned to this recipient session, or adapter receipt is missing")

// PersistMessage commits history, correlation and recipient membership atomically.
// This is the acceptance boundary, not an offline delivery queue.
func (s *Store) PersistMessage(ctx context.Context, msg protocol.Message, senderSession string, recipients []protocol.Recipient) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_metadata WHERE message_id = ?`, msg.MessageID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return ErrDuplicateMessage
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages (seq, ts, sender, recipient, payload) VALUES (?, ?, ?, ?, ?)`, msg.Seq, msg.TS.UTC(), msg.From, msg.To, string(msg.Payload)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_metadata (message_id, seq, sender_session, in_reply_to, ack_requested) VALUES (?, ?, ?, ?, ?)`, msg.MessageID, msg.Seq, senderSession, msg.InReplyTo, msg.AckRequested); err != nil {
		return err
	}
	for _, r := range recipients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_receipts (message_id, agent_id, session_id) VALUES (?, ?, ?)`, msg.MessageID, r.AgentID, r.SessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Acknowledge authenticates receipt against the session that owned the inbox at
// acceptance. Stages are monotonic and retries are idempotent.
func (s *Store) Acknowledge(ctx context.Context, id, agent, session, stage string) (sender, senderSession string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var received sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT m.sender, mm.sender_session, r.adapter_received_at FROM message_receipts r JOIN message_metadata mm ON mm.message_id = r.message_id JOIN messages m ON m.seq = mm.seq WHERE r.message_id = ? AND r.agent_id = ? AND r.session_id = ? AND mm.ack_requested = 1`, id, agent, session).Scan(&sender, &senderSession, &received)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrInvalidAck
	}
	if err != nil {
		return "", "", err
	}
	var query string
	switch stage {
	case protocol.StageAdapterReceived:
		query = `UPDATE message_receipts SET adapter_received_at = COALESCE(adapter_received_at, ?) WHERE message_id = ? AND agent_id = ? AND session_id = ?`
	case protocol.StageAgentAcknowledged:
		if !received.Valid {
			return "", "", ErrInvalidAck
		}
		query = `UPDATE message_receipts SET agent_acknowledged_at = COALESCE(agent_acknowledged_at, ?) WHERE message_id = ? AND agent_id = ? AND session_id = ?`
	default:
		return "", "", ErrInvalidAck
	}
	if _, err := tx.ExecContext(ctx, query, time.Now().UTC(), id, agent, session); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return sender, senderSession, nil
}

type Unacknowledged struct {
	Seq           uint64
	MessageID     string
	Sender        string
	SenderSession string
}

// UnacknowledgedForSession pages retained receipts without loading an unbounded
// history or holding a database cursor while the hub notifies other sessions.
func (s *Store) UnacknowledgedForSession(ctx context.Context, session string, after uint64) ([]Unacknowledged, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mm.seq, mm.message_id, m.sender, mm.sender_session FROM message_receipts r JOIN message_metadata mm ON mm.message_id = r.message_id JOIN messages m ON m.seq = mm.seq WHERE r.session_id = ? AND r.agent_acknowledged_at IS NULL AND mm.ack_requested = 1 AND mm.seq > ? ORDER BY mm.seq LIMIT 64`, session, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []Unacknowledged
	for rows.Next() {
		var p Unacknowledged
		if err := rows.Scan(&p.Seq, &p.MessageID, &p.Sender, &p.SenderSession); err != nil {
			return nil, err
		}
		pending = append(pending, p)
	}
	return pending, rows.Err()
}
