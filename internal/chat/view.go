package chat

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"collab-ai/internal/protocol"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	mutedStyle = lipgloss.NewStyle().Faint(true)
	warnStyle  = lipgloss.NewStyle().Bold(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	youStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	pickStyle  = lipgloss.NewStyle().Reverse(true)
	agentHues  = []string{"2", "3", "4", "5", "9", "10", "11", "12", "13", "14"}
)

// Minimum terminal size; below it the chat shows a notice and won't send.
const (
	minWidth  = 20
	minHeight = 4
	pickerMax = 6
)

// safe makes peer-controlled text inert for the terminal: control and format
// characters are shown as escapes, everything printable stays readable.
// Applied before any styling.
func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == utf8RuneError:
			b.WriteString(`�`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

const utf8RuneError = '�'

func agentStyle(id string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(id))
	return lipgloss.NewStyle().Foreground(lipgloss.Color(agentHues[h.Sum32()%uint32(len(agentHues))])).Bold(true)
}

func (m *Model) tooSmall() bool {
	return m.width < minWidth || m.height < minHeight
}

// View renders the whole screen on the alternate screen: every view sets it,
// so frames never scroll into the terminal's own history.
func (m *Model) View() tea.View {
	view := tea.NewView(m.screen())
	view.AltScreen = true
	return view
}

func (m *Model) screen() string {
	if m.tooSmall() {
		return fitLines("terminal too small", m.width)
	}
	l := m.layout()
	var rows []string
	if l.header {
		rows = append(rows, m.header())
	}
	rows = append(rows, m.mainArea(l)...)
	rows = append(rows, m.pickerRows(l.picker)...)
	if l.status {
		rows = append(rows, m.statusLine())
	}
	rows = append(rows, m.hintLine())
	rows = append(rows, m.composerRows(l.composer)...)
	return fitLines(strings.Join(rows, "\n"), m.width)
}

// frameLayout is how many rows each part gets. The hint line and one composer
// row come first, then the header, the status line, the transcript, more
// composer rows and the picker.
type frameLayout struct {
	header, status   bool
	transcript       int
	picker, composer int
}

func (m *Model) layout() frameLayout {
	l := frameLayout{composer: 1}
	rest := m.height - 2 // hint + one composer row
	if rest > 0 {
		l.header, rest = true, rest-1
	}
	if rest > 0 && m.statusLine() != "" {
		l.status, rest = true, rest-1
	}
	want := m.composerLines() - 1
	if m.compose.cmd != nil {
		want = 0
	}
	extra := max(0, min(want, rest-3))
	l.composer += extra
	rest -= extra
	if p := m.compose.picker; p != nil {
		l.picker = min(len(p.items), pickerMax, max(0, rest-1))
		rest -= l.picker
	}
	l.transcript = max(0, rest)
	return l
}

func (m *Model) composerLines() int {
	return max(1, min(strings.Count(m.compose.editor.Value(), "\n")+1, composerMaxLines))
}

// fitLines cuts every line to the terminal width.
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
	online := len(m.agents.list)
	switch {
	case m.width >= 76:
		return titleStyle.Render("collab operator chat") + mutedStyle.Render(fmt.Sprintf("  %s · %d online · unread %d · inbox %d · F1 help", conn, online, m.timeline.unread, m.inbox.pending))
	case m.width >= 52:
		return titleStyle.Render("operator chat") + mutedStyle.Render(fmt.Sprintf("  %s · %d online · %d unread", conn, online, m.timeline.unread))
	}
	return mutedStyle.Render(fmt.Sprintf("%s · %d on · %d new", conn, online, m.timeline.unread))
}

// mainArea is the transcript, or an open panel in its place.
func (m *Model) mainArea(l frameLayout) []string {
	if l.transcript == 0 {
		return nil
	}
	lines := m.transcriptWindow(l.transcript)
	if m.panel != nil {
		lines = m.panelRows(l.transcript)
	}
	for len(lines) < l.transcript {
		lines = append([]string{""}, lines...)
	}
	return lines
}

func (m *Model) pickerRows(n int) []string {
	p := m.compose.picker
	if p == nil || n == 0 {
		return nil
	}
	start := max(0, min(p.selected-n+1, len(p.items)-n))
	var rows []string
	for i := start; i < start+n && i < len(p.items); i++ {
		label := "  " + safe(p.items[i])
		if p.kind == PickerAgent {
			label = "  @" + safe(p.items[i])
		} else if c, _ := lookupCommand(p.items[i]); c.name != "" {
			label = fmt.Sprintf("  /%s %s  %s", c.name, c.usage, mutedStyle.Render(c.help))
		}
		if i == p.selected {
			label = pickStyle.Render(label)
		}
		rows = append(rows, label)
	}
	return rows
}

func (m *Model) composerRows(n int) []string {
	c := &m.compose
	if c.cmd != nil {
		return []string{c.cmd.input.View()}
	}
	c.editor.SetHeight(n)
	return strings.Split(c.editor.View(), "\n")
}

func (m *Model) statusLine() string {
	var parts []string
	if in := m.send.intent; in != nil && in.State != IntentAccepted {
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
	line := fmt.Sprintf("send %s: %s", shortID(in.ID), in.State)
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

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// hintText is the audience line as plain text: who the draft goes to, or why
// it can't be sent.
func (m *Model) hintText() string {
	c := &m.compose
	if c.cmd != nil {
		return "command · Enter runs · Tab completes · Esc back to your draft"
	}
	a := m.resolveAudience()
	var text string
	switch {
	case a.err != "":
		return "✗ " + a.err
	case a.source == SourceReply:
		text = fmt.Sprintf("↳ reply to #%d → %s", c.reply.seq, safe(c.reply.sender))
	case a.everyone:
		note := ""
		if m.agents.partial {
			note = ", partial list"
		}
		text = fmt.Sprintf("→ EVERYONE (%d online, advisory%s)", len(m.agents.list), note)
	default:
		ids := make([]string, len(a.ids))
		for i, id := range a.ids {
			ids[i] = safe(id)
		}
		text = "→ " + strings.Join(ids, ", ")
	}
	if len(a.notSeen) > 0 {
		text += " (not seen: " + safe(strings.Join(a.notSeen, ", ")) + ")"
	}
	if c.pasted {
		text += " · pasted text, mentions ignored"
	}
	return text
}

func (m *Model) hintLine() string {
	text := m.hintText()
	if strings.HasPrefix(text, "✗") {
		return errStyle.Render(text)
	}
	return warnStyle.Render(text) + mutedStyle.Render("   Enter send · / commands · F1 help")
}

// renderLine is one visual line of the transcript and the entry it belongs to.
type renderLine struct {
	id   string
	text string
}

// transcript lays out every visible entry at the current width.
func (m *Model) transcript() []renderLine {
	width := max(minWidth, m.width)
	var lines []renderLine
	for _, e := range m.timeline.order {
		if e.msg.Seq <= m.cutoff {
			continue
		}
		for _, text := range m.entryLines(e, width) {
			lines = append(lines, renderLine{id: e.msg.MessageID, text: text})
		}
	}
	return lines
}

func (m *Model) entryLines(e *entry, width int) []string {
	lines := []string{"", m.entryHeader(e, width)}
	if e.msg.InReplyTo != "" {
		lines = append(lines, mutedStyle.Render("  ↳ "+m.replyLabel(e.msg.InReplyTo)))
	}
	if e.omitted {
		lines = append(lines, mutedStyle.Render("  [text not available: this message exceeds the history page size]"))
	} else {
		for _, line := range strings.Split(payloadText(e.msg.Payload), "\n") {
			for _, part := range wrap(safe(line), width-2) {
				lines = append(lines, "  "+part)
			}
		}
	}
	for _, part := range wrap(m.entryStatus(e), width-4) {
		lines = append(lines, mutedStyle.Render("  · "+part))
	}
	if e.gapBelow {
		lines = append(lines, warnStyle.Render("  ── older messages may be missing here · /history loads them ──"))
	}
	return lines
}

// wrap breaks text at word boundaries, and inside words longer than width.
func wrap(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	return strings.Split(ansi.Hardwrap(ansi.Wordwrap(text, width, ""), width, true), "\n")
}

func (m *Model) entryHeader(e *entry, width int) string {
	stamp := ""
	if e.msg.TS != nil {
		stamp = e.msg.TS.Local().Format("15:04") + "  "
	}
	var who string
	if e.direction == "sent" {
		who = youStyle.Render("you") + " → " + recipientsLabel(e.msg)
	} else {
		who = agentStyle(e.msg.From).Render(safe(e.msg.From)) + " → " + youStyle.Render("you") +
			mutedStyle.Render(" ["+protocol.DisplayOrigin(e.msg.Origin)+"]")
	}
	left := mutedStyle.Render(stamp) + who
	seq := mutedStyle.Render(fmt.Sprintf("#%d", e.msg.Seq))
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(seq)
	if gap < 2 {
		return left + "  " + seq
	}
	return left + strings.Repeat(" ", gap) + seq
}

func recipientsLabel(msg protocol.Message) string {
	switch {
	case msg.To == protocol.Broadcast:
		return "everyone"
	case len(msg.Group) > 0:
		ids := make([]string, len(msg.Group))
		for i, id := range msg.Group {
			ids[i] = agentStyle(id).Render(safe(id))
		}
		return strings.Join(ids, ", ")
	}
	return agentStyle(msg.To).Render(safe(msg.To))
}

// replyLabel names the replied-to message by number when it is loaded; the
// ID is a claim by the sender either way.
func (m *Model) replyLabel(id string) string {
	if e := m.timeline.entries[id]; e != nil {
		return fmt.Sprintf("re #%d", e.msg.Seq)
	}
	return "re " + strconv.Quote(safe(id)) + " (claim)"
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
	for _, st := range e.view().Stages {
		parts = append(parts, safe(st.AgentID)+": "+stageLabel(st.Stage))
	}
	if e.stagesTruncated {
		parts = append(parts, fmt.Sprintf("more: /stages #%d", e.msg.Seq))
	}
	if len(parts) == 0 {
		return "sent"
	}
	return strings.Join(parts, " · ")
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
