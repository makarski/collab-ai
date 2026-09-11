// Package store persists conversation state (agents and messages) in SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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
