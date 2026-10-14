package chat

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"collab-ai/internal/protocol"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	mutedStyle  = lipgloss.NewStyle().Faint(true)
	selectStyle = lipgloss.NewStyle().Reverse(true)
	warnStyle   = lipgloss.NewStyle().Bold(true)
)

// safe quotes peer-controlled text so terminal controls stay inert; it is
// applied before any styling.
func safe(s string) string {
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}

func (m *Model) View() tea.View {
	if m.help {
		return tea.NewView(m.helpView())
	}
	left := m.agentsView()
	right := m.timelineView(max(10, m.height-9))
	body := lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(24).Render(left), " ", right)
	lines := []string{m.header(), body, m.composeView(), m.statusLine()}
	return tea.NewView(fitLines(strings.Join(lines, "\n"), m.width))
}

// fitLines cuts every line to the terminal width; before the first size
// report it assumes 80 columns.
func fitLines(s string, width int) string {
	if width <= 0 {
		width = 80
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) header() string {
	conn := m.conn
	if m.conn == Connected && !m.baseline {
		conn = "syncing"
	}
	return titleStyle.Render("collab operator chat") + mutedStyle.Render(fmt.Sprintf("  %s · inbox %d · unread %d · F1 help", conn, m.inbox.pending, m.timeline.unread))
}

func (m *Model) agentsView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Agents") + "\n")
	for _, a := range m.agents.list {
		fmt.Fprintf(&b, "● %s v%d\n", safe(a.AgentID), a.ProtocolVersion)
	}
	if m.agents.partial {
		fmt.Fprintf(&b, mutedStyle.Render("… %d total (partial list)")+"\n", m.agents.total)
	}
	return b.String()
}

// timelineView shows the newest messages that fit, keeping the selection visible.
func (m *Model) timelineView(height int) string {
	var blocks []string
	for i, e := range m.timeline.order {
		block := m.entryView(e)
		if i == m.timeline.selected {
			block = selectStyle.Render(block)
		}
		blocks = append(blocks, block)
	}
	end := len(blocks)
	if m.timeline.selected >= 0 {
		end = min(len(blocks), m.timeline.selected+1+height/3)
	}
	var lines []string
	for i := end - 1; i >= 0 && len(lines) < height; i-- {
		lines = append(strings.Split(blocks[i], "\n"), lines...)
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}

func (m *Model) entryView(e *entry) string {
	var b strings.Builder
	header := fmt.Sprintf("#%d %s → %s [%s]", e.msg.Seq, safe(e.msg.From), safe(e.msg.To), protocol.DisplayOrigin(e.msg.Origin))
	if e.msg.InReplyTo != "" {
		header += fmt.Sprintf(" re %q (claim)", e.msg.InReplyTo)
	}
	b.WriteString(header + "\n")
	switch {
	case e.omitted:
		b.WriteString(mutedStyle.Render("  [text not available: this message exceeds the history page size]") + "\n")
	default:
		for _, line := range strings.Split(payloadText(e.msg.Payload), "\n") {
			b.WriteString("  " + safe(line) + "\n")
		}
	}
	b.WriteString(mutedStyle.Render("  "+m.entryStatus(e)) + "\n")
	if e.gapBelow {
		b.WriteString(warnStyle.Render("  ── possible gap below: select and press PgUp to load ──") + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (m *Model) entryStatus(e *entry) string {
	var parts []string
	if e.conflict {
		parts = append(parts, "CONFLICT: content changed under this ID")
	}
	if e.direction == "received" {
		parts = append(parts, "you: "+stageLabel(e.operatorStage), "receipt: "+orDash(e.receipt))
		if q := m.inbox.queuedAcks[e.msg.MessageID]; q != nil {
			parts = append(parts, "ack queued"+suspendedNote(q.suspended))
		}
		return strings.Join(parts, " · ")
	}
	for _, st := range sortedStages(e) {
		parts = append(parts, safe(st.AgentID)+": "+stageLabel(st.Stage))
	}
	if e.stagesTruncated {
		parts = append(parts, "more (press s)")
	}
	return strings.Join(parts, " · ")
}

func sortedStages(e *entry) []protocol.RecipientStage {
	return e.view().Stages
}

func stageLabel(stage string) string {
	switch stage {
	case protocol.StageAgentAcknowledged:
		return "acknowledged"
	case protocol.StageAdapterReceived:
		return "received"
	case protocol.StageAccepted:
		return "accepted"
	}
	return safe(orDash(stage))
}

func suspendedNote(reason string) string {
	if reason == "" {
		return ""
	}
	return " (suspended: " + reason + ")"
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func (m *Model) composeView() string {
	var b strings.Builder
	b.WriteString("To: " + m.recipients.View() + "\n")
	b.WriteString(mutedStyle.Render("   "+m.preview()) + "\n")
	if m.send.inReplyTo != "" {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("   replying to %q", m.send.inReplyTo)) + "\n")
	}
	b.WriteString(m.editor.View())
	return b.String()
}

// preview is advisory: the broker decides recipients when it accepts a send.
func (m *Model) preview() string {
	scope, err := ParseRecipients(m.recipients.Value())
	if err != nil {
		return "preview: " + safe(err.Error())
	}
	if isBroadcast(scope) {
		note := ""
		if m.agents.partial {
			note = " (partial list)"
		}
		return fmt.Sprintf("preview: everyone eligible, %d connected now%s", len(m.agents.list), note)
	}
	ids := scope.Group
	if scope.To != "" {
		ids = []string{scope.To}
	}
	for i := range ids {
		ids[i] = safe(ids[i])
	}
	return "preview: " + strings.Join(ids, ", ")
}

func (m *Model) statusLine() string {
	if c := m.send.confirm; c != nil {
		return warnStyle.Render(fmt.Sprintf("Send to %d agents (current preview)? y/n", c.preview))
	}
	var parts []string
	if in := m.send.intent; in != nil {
		parts = append(parts, intentLine(in))
	}
	if m.timeline.paused {
		parts = append(parts, "capacity reached: receiving paused")
	}
	if m.status != "" {
		parts = append(parts, safe(m.status))
	}
	return strings.Join(parts, " · ")
}

func intentLine(in *intent) string {
	line := fmt.Sprintf("send %q: %s", in.ID, in.State)
	if len(in.Excluded) > 0 {
		quoted := make([]string, len(in.Excluded))
		for i, id := range in.Excluded {
			quoted[i] = strconv.Quote(id)
		}
		line += " (excluded: " + strings.Join(quoted, ", ") + ")"
	}
	if in.Detail != "" {
		line += " " + safe(in.Detail)
	}
	return line
}

func (m *Model) helpView() string {
	support := "not reported by this terminal; Shift+Enter may arrive as Enter and send"
	if m.keyboard {
		support = "reported by this terminal"
	}
	return strings.Join([]string{
		titleStyle.Render("collab operator chat — help (F1 closes)"),
		"",
		"Editor:     Enter sends · Shift+Enter, Alt+Enter, Ctrl+J new line · Esc to timeline",
		"Recipients: agent | agent1, agent2 | @all (asks first) | [\"literal,id\"]",
		"Timeline:   ↑/↓ select · PgUp older history or fill a gap · a acknowledge (received, not approval)",
		"            r reply to a received message · s more recipient stages",
		"Anywhere:   Tab/Shift+Tab focus · F1 help · Ctrl+Q quit · Ctrl+R retry after owner_busy or a malformed frame",
		"",
		"Key disambiguation: " + support,
		"Alt+Enter and Ctrl+J are fallbacks; terminals and multiplexers can remap them.",
	}, "\n")
}
