package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// InboxFullError names the bound that refused a durable send. It matches
// ErrInboxFull for callers that only care that something was full.
type InboxFullError struct {
	Bound string // "sender messages", "sender bytes", "inbox messages", "inbox bytes", "global pending", "global pending bytes", "retained messages", "retained bytes"
	Count int
	Limit int
	Unit  string
}

func (e *InboxFullError) Error() string {
	return fmt.Sprintf("durable storage limit reached (%s); message not accepted; acknowledge pending work or archive the database explicitly", e.Detail())
}

// Detail is the bound in the form "<bound>: <count>/<limit> <unit>".
func (e *InboxFullError) Detail() string {
	return fmt.Sprintf("%s: %d/%d %s", e.Bound, e.Count, e.Limit, e.Unit)
}

func (e *InboxFullError) Is(target error) bool { return target == ErrInboxFull }

func inboxFull(bound string, count, limit int, unit string) error {
	return &InboxFullError{Bound: bound, Count: count, Limit: limit, Unit: unit}
}

// OperatorInboxStatus reads the operator inbox's usage against its bounds. It
// changes nothing.
func (s *Store) OperatorInboxStatus(ctx context.Context) (protocol.InboxStatus, error) {
	out := protocol.InboxStatus{
		Limits:   protocol.InboxLimits{SenderMessages: OperatorSenderMessages, SenderBytes: OperatorSenderBytes, InboxMessages: OperatorInboxMessages, InboxBytes: OperatorInboxBytes},
		Retained: protocol.RetainedUsage{LimitMessages: MaxRetainedMessages, LimitBytes: MaxRetainedBytes},
		Senders:  []protocol.SenderUsage{},
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	const pending = `FROM durable_inbox i JOIN durable_messages d USING(message_id) JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL`
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(mm.seq),0), COALESCE(MIN(mm.seq),0), COUNT(*), COALESCE(SUM(i.received_at IS NULL),0), COALESCE(SUM(d.wire_bytes),0) `+pending, protocol.OperatorID).
		Scan(&out.HeadSeq, &out.OldestUnacknowledgedSeq, &out.Unacknowledged, &out.Unreceived, &out.Bytes)
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.sender, COUNT(*), COALESCE(SUM(i.received_at IS NULL),0), COALESCE(SUM(d.wire_bytes),0) `+pending+` GROUP BY m.sender ORDER BY m.sender`, protocol.OperatorID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var u protocol.SenderUsage
		if err := rows.Scan(&u.AgentID, &u.Unacknowledged, &u.Unreceived, &u.Bytes); err != nil {
			return out, err
		}
		out.Senders = append(out.Senders, u)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(storage_bytes),0) FROM durable_messages`).Scan(&out.Retained.Messages, &out.Retained.Bytes)
	return out, err
}

// AckManyReport is what one stable acknowledgment page did.
type AckManyReport struct {
	Transitions []OperatorTransition
	Receipts    []protocol.ReceiptState
	Result      protocol.AckManyResult
}

// AcknowledgeOperatorMany acknowledges one stable page of operator-inbox
// rows: every row in the selection's range, in sequence order, acknowledged
// or not, so a retry re-examines the same rows. Received rows are
// acknowledged with their own payload-free audit row; unreceived rows are
// skipped and counted, never receipted here. Everything commits together
// with the request's audit row.
func (s *Store) AcknowledgeOperatorMany(ctx context.Context, sel protocol.AckSelection, audit OperatorAudit) (AckManyReport, error) {
	limit, err := ackManyLimit(sel)
	if err != nil {
		return AckManyReport{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AckManyReport{}, err
	}
	defer tx.Rollback()
	run := &ackRun{ctx: ctx, tx: tx, now: time.Now().UTC(), uid: audit.PrincipalUID}
	if err := run.apply(sel, limit); err != nil {
		return run.report, err
	}
	audit.Detail = run.detail(sel)
	if err := insertAudit(ctx, tx, audit); err != nil {
		return run.report, err
	}
	return run.report, tx.Commit()
}

// ackRun applies one selection inside one transaction.
type ackRun struct {
	ctx    context.Context
	tx     *sql.Tx
	now    time.Time
	uid    int
	report AckManyReport
}

// apply reads the stable page and acknowledges what it may; the bound must
// be committed history, never a guess.
func (r *ackRun) apply(sel protocol.AckSelection, limit int) error {
	if err := r.checkCommitted(sel.UpperSeq); err != nil {
		return err
	}
	rows, err := selectionRows(r.ctx, r.tx, sel, limit+1)
	if err != nil {
		return err
	}
	r.report.Result.NextAfterSeq = sel.AfterSeq
	if len(rows) > limit {
		rows, r.report.Result.More = rows[:limit], true
	}
	for _, row := range rows {
		r.report.Result.NextAfterSeq = row.seq
		if err := r.ackSelected(row); err != nil {
			return err
		}
	}
	return nil
}

func (r *ackRun) checkCommitted(upper uint64) error {
	var last uint64
	if err := r.tx.QueryRowContext(r.ctx, `SELECT COALESCE(MAX(seq), 0) FROM messages`).Scan(&last); err != nil {
		return err
	}
	if upper > last {
		return ErrInvalidSelection
	}
	return nil
}

func (r *ackRun) detail(sel protocol.AckSelection) string {
	res := r.report.Result
	return fmt.Sprintf("after=%d upper=%d from=%q acknowledged=%d already=%d skipped=%d more=%t", sel.AfterSeq, sel.UpperSeq, sel.From, res.Acknowledged, res.AlreadyAcknowledged, res.Skipped, res.More)
}

// ackSelected applies one row of the selection to the report.
func (r *ackRun) ackSelected(row selectedRow) error {
	switch {
	case row.acknowledged.Valid:
		r.report.Result.AlreadyAcknowledged++
	case !row.received.Valid:
		r.report.Result.Skipped++
	default:
		return r.acknowledge(row)
	}
	return nil
}

// acknowledge records the transition with its own payload-free audit row.
func (r *ackRun) acknowledge(row selectedRow) error {
	if err := updateDurableReceipt(r.ctx, r.tx, operatorStage(row.id, protocol.StageAgentAcknowledged), r.now); err != nil {
		return err
	}
	if err := insertAudit(r.ctx, r.tx, OperatorAudit{Action: "ack", PrincipalUID: r.uid, MessageID: row.id, Result: AuditAccepted, Detail: "bulk"}); err != nil {
		return err
	}
	r.report.Transitions = append(r.report.Transitions, OperatorTransition{row.id, row.sender, protocol.StageAgentAcknowledged})
	r.report.Receipts = append(r.report.Receipts, protocol.ReceiptState{MessageID: row.id, AgentID: protocol.OperatorID, Stage: protocol.StageAgentAcknowledged})
	r.report.Result.Acknowledged++
	return nil
}

func ackManyLimit(sel protocol.AckSelection) (int, error) {
	if sel.UpperSeq == 0 || sel.AfterSeq >= sel.UpperSeq {
		return 0, ErrInvalidSelection
	}
	switch {
	case sel.Limit == 0:
		return protocol.AckManyDefault, nil
	case sel.Limit < 0 || sel.Limit > protocol.AckManyMax:
		return 0, ErrInvalidSelection
	}
	return sel.Limit, nil
}

type selectedRow struct {
	seq                    uint64
	id                     string
	sender                 protocol.Recipient
	received, acknowledged sql.NullTime
}

// selectionRows reads the stable page: rows in range regardless of state.
func selectionRows(ctx context.Context, tx *sql.Tx, sel protocol.AckSelection, n int) ([]selectedRow, error) {
	query := `SELECT mm.seq, i.message_id, m.sender, mm.sender_session, i.received_at, i.acknowledged_at FROM durable_inbox i JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.agent_id = ? AND mm.seq > ? AND mm.seq <= ?`
	args := []any{protocol.OperatorID, sel.AfterSeq, sel.UpperSeq}
	if sel.From != "" {
		query += ` AND m.sender = ?`
		args = append(args, sel.From)
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY mm.seq LIMIT ?`, append(args, n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []selectedRow
	for rows.Next() {
		var r selectedRow
		if err := rows.Scan(&r.seq, &r.id, &r.sender.AgentID, &r.sender.SessionID, &r.received, &r.acknowledged); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
