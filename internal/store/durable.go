package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"collab-ai/internal/protocol"
)

// The per-inbox limits fit a full reconnect replay in the existing 64-frame
// writer queue and 4 MiB adapter buffer, without a new paging protocol.
const (
	MaxPendingMessages    = 32
	MaxPendingBytes       = 2 << 20
	MaxGlobalPending      = 4096
	MaxGlobalPendingBytes = 64 << 20
	MaxRetainedMessages   = 100000
	MaxRetainedBytes      = 256 << 20
)

var ErrInboxFull = errors.New("durable storage limit reached; message not accepted; acknowledge pending work or archive the database explicitly")

const durableSchema = `
CREATE TABLE IF NOT EXISTS durable_agents (agent_id TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS durable_messages (
 message_id TEXT PRIMARY KEY,
 wire_bytes INTEGER NOT NULL,
 storage_bytes INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS durable_inbox (
 message_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 received_at TIMESTAMP,
 acknowledged_at TIMESTAMP,
 PRIMARY KEY (message_id, agent_id)
);
CREATE INDEX IF NOT EXISTS durable_pending ON durable_inbox(agent_id) WHERE acknowledged_at IS NULL;
`

func (s *Store) KnownDurableAgent(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_agents WHERE agent_id = ?`, id).Scan(&count)
	return count != 0, err
}

func persistDurable(ctx context.Context, tx *sql.Tx, msg protocol.Message, recipients []protocol.Recipient) error {
	if !msg.Durable {
		return nil
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	// Include replay metadata and a conservative allowance for receipt rows and
	// IDs. This bounds logical durable storage, not SQLite file/journal overhead.
	bytes := len(data) + 64
	if err := checkDurableCapacity(ctx, tx, durableSize{bytes, recipients}); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO durable_messages VALUES (?, ?, ?)`, msg.MessageID, bytes, bytes+len(recipients)*1024)
	if err != nil {
		return err
	}
	for _, r := range recipients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO durable_inbox (message_id, agent_id, session_id) VALUES (?, ?, ?)`, msg.MessageID, r.AgentID, r.SessionID); err != nil {
			return err
		}
	}
	return nil
}

type durableSize struct {
	bytes      int
	recipients []protocol.Recipient
}

func checkDurableCapacity(ctx context.Context, tx *sql.Tx, size durableSize) error {
	var count, bytes int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(storage_bytes),0) FROM durable_messages`).Scan(&count, &bytes); err != nil {
		return err
	}
	if count >= MaxRetainedMessages {
		return ErrInboxFull
	}
	if bytes+size.bytes+len(size.recipients)*1024 > MaxRetainedBytes {
		return ErrInboxFull
	}
	query := `SELECT COUNT(*), COALESCE(SUM(d.wire_bytes),0) FROM durable_inbox i JOIN durable_messages d USING(message_id) WHERE i.acknowledged_at IS NULL`
	if err := tx.QueryRowContext(ctx, query).Scan(&count, &bytes); err != nil {
		return err
	}
	if count+len(size.recipients) > MaxGlobalPending {
		return ErrInboxFull
	}
	if bytes+size.bytes*len(size.recipients) > MaxGlobalPendingBytes {
		return ErrInboxFull
	}
	return checkRecipientCapacity(ctx, tx, size)
}

func checkRecipientCapacity(ctx context.Context, tx *sql.Tx, size durableSize) error {
	for _, recipient := range size.recipients {
		var count, bytes int
		query := `SELECT COUNT(*), COALESCE(SUM(d.wire_bytes),0) FROM durable_inbox i JOIN durable_messages d USING(message_id) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL`
		if err := tx.QueryRowContext(ctx, query, recipient.AgentID).Scan(&count, &bytes); err != nil {
			return err
		}
		if count >= MaxPendingMessages {
			return ErrInboxFull
		}
		if bytes+size.bytes > MaxPendingBytes {
			return ErrInboxFull
		}
	}
	return nil
}

// ClaimPending commits the new delivery owner before exposing pending frames.
// Only v3 hub registration calls this; legacy history is never enrolled.
func (s *Store) ClaimPending(ctx context.Context, recipient protocol.Recipient) ([]protocol.Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE durable_inbox SET session_id = ?, received_at = NULL WHERE agent_id = ? AND acknowledged_at IS NULL`, recipient.SessionID, recipient.AgentID)
	if err != nil {
		return nil, err
	}
	messages, err := readPending(ctx, tx, recipient.AgentID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return messages, nil
}

func readPending(ctx context.Context, tx *sql.Tx, id string) ([]protocol.Message, error) {
	query := `SELECT mm.message_id, m.seq, m.ts, m.sender, mm.sender_session, m.recipient, m.payload, mm.in_reply_to FROM durable_inbox i JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL ORDER BY m.seq LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, id, MaxPendingMessages+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []protocol.Message
	for rows.Next() {
		msg, err := scanDurableMessage(rows)
		if err != nil {
			return nil, err
		}
		msg.Replayed = true
		messages = append(messages, msg)
	}
	if len(messages) > MaxPendingMessages {
		return nil, ErrInboxFull
	}
	return messages, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanDurableMessage(row scanner) (protocol.Message, error) {
	msg := protocol.Message{Type: protocol.TypeMsg, Durable: true, AckRequested: true}
	var payload string
	err := row.Scan(&msg.MessageID, &msg.Seq, &msg.TS, &msg.From, &msg.SessionID, &msg.To, &payload, &msg.InReplyTo)
	msg.Payload = json.RawMessage(payload)
	return msg, err
}
