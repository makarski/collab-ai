package chat

import (
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"collab-ai/internal/protocol"
)

var errTooLarge = errors.New("message too large: operator messages are limited to 512 KiB")

// frame handles one frame and keeps exactly one listen command pending.
func (m *Model) frame(msg FrameMsg) tea.Cmd {
	if msg.Gen != m.gen || m.transport == nil {
		return nil
	}
	next := listen(m.gen, m.transport)
	if msg.Frame.Type == protocol.TypeEvent {
		return tea.Batch(next, m.event(msg.Frame.Event))
	}
	return tea.Batch(next, m.response(msg.Frame))
}

// event applies notifications in revision order; a gap, a new epoch or
// resync_required means a new snapshot.
func (m *Model) event(ev *protocol.SessionEvent) tea.Cmd {
	if ev == nil {
		return m.malformed("event without a body")
	}
	if !m.baseline {
		return nil
	}
	if ev.Watermark.Epoch == m.epoch && ev.Watermark.Revision <= m.revision {
		return nil
	}
	if ev.Kind == protocol.EventResyncRequired || ev.Watermark.Epoch != m.epoch || ev.Watermark.Revision != m.revision+1 {
		if ev.Watermark.Epoch != m.epoch {
			m.newEpoch(ev.Watermark.Epoch)
		}
		return m.resync()
	}
	m.revision = ev.Watermark.Revision
	return m.applyEvent(ev)
}

func (m *Model) applyEvent(ev *protocol.SessionEvent) tea.Cmd {
	switch ev.Kind {
	case protocol.EventConversationChanged:
		return m.conversationChanged(ev)
	case protocol.EventInboxChanged:
		return m.inboxChanged(ev)
	case protocol.EventStageChanged:
		m.stageChanged(ev)
	case protocol.EventPresenceChanged:
		m.agents.apply(ev)
	}
	return nil
}

func (m *Model) conversationChanged(ev *protocol.SessionEvent) tea.Cmd {
	if ev.Direction == "received" {
		return m.startDrain()
	}
	return m.startCatchup()
}

func (m *Model) inboxChanged(ev *protocol.SessionEvent) tea.Cmd {
	if ev.Pending != nil {
		m.inbox.pending = *ev.Pending
	}
	grew := ev.HeadSeq > m.inbox.headSeq
	m.inbox.headSeq = ev.HeadSeq
	if grew {
		return m.startDrain()
	}
	return nil
}

func (m *Model) stageChanged(ev *protocol.SessionEvent) {
	e := m.timeline.entries[ev.MessageID]
	if e == nil || e.direction != "sent" || !validStage(ev.Stage) {
		return
	}
	e.applyStage(protocol.RecipientStage{AgentID: ev.AgentID, SessionID: ev.SessionID, Stage: ev.Stage}, ev.Watermark)
}

// response routes a reply to its request. Unknown IDs are ignored: they
// belong to an older connection.
func (m *Model) response(resp protocol.Message) tea.Cmd {
	if resp.RequestID == "" {
		return m.malformed(fmt.Sprintf("response without request_id (%q)", resp.Type))
	}
	req, next := m.release(resp.RequestID)
	if req.kind == "" {
		return next
	}
	return tea.Batch(next, m.handle(req, resp))
}

func (m *Model) handle(req pendingRequest, resp protocol.Message) tea.Cmd {
	switch req.kind {
	case kindSnapshot:
		return m.snapshotLoaded(resp)
	case kindReceive:
		return m.pageOrError(resp, func() tea.Cmd { return m.receivedPage(req.page, resp.Page) }, &m.inbox.draining)
	case kindReceived:
		return m.receiptsConfirmed(req, resp)
	case kindAck:
		m.acknowledged(req, resp)
	case kindCatchup:
		return m.historyOrError(resp, func() tea.Cmd { return m.catchupPage(req.page, resp.History) }, &m.history.catchup)
	case kindOlder:
		m.historyOrError(resp, func() tea.Cmd { m.olderPage(req.page, resp.History); return nil }, &m.history.loadingOlder)
	case kindStages:
		m.stagesPage(req, resp)
	case kindSend:
		return m.sendResult(req, resp)
	}
	return nil
}

func (m *Model) pageOrError(resp protocol.Message, ok func() tea.Cmd, busy *bool) tea.Cmd {
	if resp.Type == protocol.TypeOperatorPage {
		return ok()
	}
	*busy = false
	m.status = "inbox not loaded: " + brokerReason(resp)
	return nil
}

func (m *Model) historyOrError(resp protocol.Message, ok func() tea.Cmd, busy *bool) tea.Cmd {
	if resp.Type == protocol.TypeOperatorHistory {
		return ok()
	}
	*busy = false
	m.status = "history not loaded: " + brokerReason(resp)
	return nil
}

// snapshotLoaded installs the baseline: bodies stay, metadata resets, then
// the inbox, history and any unknown send are reconciled.
func (m *Model) snapshotLoaded(resp protocol.Message) tea.Cmd {
	snap := resp.Snapshot
	if resp.Type != protocol.TypeOperatorSnapshot || snap == nil || snap.Watermark.Epoch == "" {
		return m.malformed("snapshot: " + brokerReason(resp))
	}
	if snap.Watermark.Epoch != m.epoch {
		m.newEpoch(snap.Watermark.Epoch)
	}
	m.revision, m.baseline = snap.Watermark.Revision, true
	m.agents = agentView{list: snap.Agents, total: snap.AgentsTotal, partial: snap.AgentsTruncated}
	m.inbox.pending, m.inbox.headSeq = snap.InboxPending, snap.InboxHeadSeq
	m.timeline.resetStages()
	for _, s := range snap.RecentSent {
		if e := m.timeline.entries[s.MessageID]; e != nil {
			for _, st := range s.Recipients {
				e.applyStage(st, snap.Watermark)
			}
			e.stagesTruncated = s.ReceiptsTruncated
		}
	}
	return tea.Batch(m.startDrain(), m.startCatchup(), m.resendUnknown())
}

// malformed reports a response the chat can't accept; it's not retried as if
// the transport had failed.
func (m *Model) malformed(detail string) tea.Cmd {
	m.status = ErrMalformed.Error() + ": " + detail
	return nil
}

func brokerReason(resp protocol.Message) string {
	if resp.Code != "" {
		return fmt.Sprintf("%q: %q", resp.Code, resp.Detail)
	}
	return fmt.Sprintf("unexpected %q response", resp.Type)
}

func isMalformed(err error) bool {
	return errors.Is(err, ErrMalformed)
}

func validStage(stage string) bool {
	switch stage {
	case protocol.StageAccepted, protocol.StageAdapterReceived, protocol.StageAgentAcknowledged:
		return true
	}
	return false
}

func validStages(stages []protocol.RecipientStage) bool {
	for _, st := range stages {
		if !validStage(st.Stage) {
			return false
		}
	}
	return true
}

// validateReceivePage checks a whole inbox page before anything is adopted.
func validateReceivePage(req protocol.PageRequest, page *protocol.OperatorPage) error {
	if page == nil {
		return fmt.Errorf("%w: missing inbox page", ErrMalformed)
	}
	if req.UpperSeq != 0 && page.UpperSeq != req.UpperSeq {
		return fmt.Errorf("%w: inbox page upper_seq changed", ErrMalformed)
	}
	seen := map[string]bool{}
	last := req.AfterSeq
	for _, msg := range page.Messages {
		if !validInboxMessage(msg) || seen[msg.MessageID] || msg.Seq <= last || msg.Seq > page.UpperSeq {
			return fmt.Errorf("%w: inbox page holds an invalid or out-of-order message", ErrMalformed)
		}
		seen[msg.MessageID], last = true, msg.Seq
	}
	if page.NextAfterSeq != last || (len(page.Messages) == 0 && page.More) {
		return fmt.Errorf("%w: inbox page cursor is inconsistent", ErrMalformed)
	}
	return nil
}

// validInboxMessage requires a complete message addressed to the operator;
// the broker sends a payload-less message as JSON null.
func validInboxMessage(msg protocol.Message) bool {
	return msg.Type == protocol.TypeMsg && msg.To == protocol.OperatorID && msg.MessageID != "" && len(msg.Payload) > 0
}

// validateHistory checks a whole history page before anything is inserted.
func validateHistory(req protocol.PageRequest, page *protocol.HistoryPage) error {
	if page == nil {
		return fmt.Errorf("%w: missing history page", ErrMalformed)
	}
	if req.UpperSeq != 0 && page.UpperSeq != req.UpperSeq {
		return fmt.Errorf("%w: history upper_seq changed", ErrMalformed)
	}
	seen := map[string]bool{}
	var last uint64
	for i, h := range page.Entries {
		if !validHistoryEntry(h) || seen[h.Message.MessageID] || !inWindow(h.Message.Seq, req.BeforeSeq, page.UpperSeq) || (i > 0 && h.Message.Seq >= last) {
			return fmt.Errorf("%w: history holds an invalid or out-of-order entry", ErrMalformed)
		}
		seen[h.Message.MessageID], last = true, h.Message.Seq
	}
	want := req.BeforeSeq
	if len(page.Entries) > 0 {
		want = last
	}
	if page.NextBeforeSeq != want || (len(page.Entries) == 0 && page.More) {
		return fmt.Errorf("%w: history cursor is inconsistent", ErrMalformed)
	}
	return nil
}

func inWindow(seq, before, upper uint64) bool {
	return seq > 0 && seq <= upper && (before == 0 || seq < before)
}

func validHistoryEntry(h protocol.HistoryEntry) bool {
	if h.Message.Type != protocol.TypeMsg || h.Message.MessageID == "" || h.BodyOmitted != (len(h.Message.Payload) == 0) {
		return false
	}
	switch h.Direction {
	case "sent":
		return h.Message.Origin == protocol.OriginOperator && validStages(h.Recipients)
	case "received":
		return h.Message.To == protocol.OperatorID && validStage(h.OperatorStage)
	}
	return false
}
