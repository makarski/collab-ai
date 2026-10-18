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
			b.WriteString(`\t`) // identities must stay distinguishable
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

// safeText is safe for message text: tabs indent instead of showing as \t.
func safeText(s string) string {
	return safe(strings.ReplaceAll(s, "\t", "    "))
}

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
	for i := start; i < min(start+n, len(p.items)); i++ {
		label := pickerLabel(p.kind, p.items[i])
		if i == p.selected {
			label = pickStyle.Render(label)
		}
		rows = append(rows, label)
	}
	return rows
}

func pickerLabel(kind, item string) string {
	if kind == PickerAgent {
		return "  @" + safe(item)
	}
	c := byName(item)
	return fmt.Sprintf("  /%s %s  %s", c.name, c.usage, mutedStyle.Render(c.help))
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
	if m.compose.cmd != nil {
		return "command · Enter runs · Tab completes · Esc back to your draft"
	}
	a := m.resolveAudience()
	if a.err != "" {
		return "✗ " + a.err
	}
	text := m.audienceText(a)
	if len(a.notSeen) > 0 {
		text += " (not seen: " + safe(strings.Join(a.notSeen, ", ")) + ")"
	}
	if m.compose.pasted {
		text += " · pasted text, mentions ignored"
	}
	return text
}

func (m *Model) audienceText(a audience) string {
	switch {
	case a.source == SourceReply:
		r := m.compose.reply
		return fmt.Sprintf("↳ reply to #%d → %s", r.seq, safe(r.sender))
	case a.everyone:
		return fmt.Sprintf("→ EVERYONE (%d online, advisory%s)", len(m.agents.list), partialNote(m.agents.partial))
	}
	ids := make([]string, len(a.ids))
	for i, id := range a.ids {
		ids[i] = safe(id)
	}
	return "→ " + strings.Join(ids, ", ")
}

func partialNote(partial bool) string {
	if partial {
		return ", partial list"
	}
	return ""
}

func (m *Model) hintLine() string {
	text := m.hintText()
	if strings.HasPrefix(text, "✗") {
		return errStyle.Render(text)
	}
	return warnStyle.Render(text) + mutedStyle.Render("   Enter send · / commands · F1 help")
}

// renderLine is one visual line of the transcript: the entry it belongs to,
// the logical line within the entry (src) and the cell offset of this wrapped
// segment within that logical line (col). Anchoring by src and col keeps the
// same text in view when a resize re-wraps it.
type renderLine struct {
	id       string
	src, col int
	text     string
}

// transcript lays out every visible entry at the current width.
func (m *Model) transcript() []renderLine {
	width := max(minWidth, m.width)
	var lines []renderLine
	for _, e := range m.timeline.order {
		if e.msg.Seq <= m.cutoff {
			continue
		}
		lines = append(lines, m.entryLines(e, width)...)
	}
	return lines
}

// entryLines lays out one entry as logical lines, each wrapped to width.
func (m *Model) entryLines(e *entry, width int) []renderLine {
	b := lineBuilder{id: e.msg.MessageID}
	b.add([]string{""}, "")
	b.add(m.entryHeader(e, width), "")
	if e.msg.InReplyTo != "" {
		b.add([]string{mutedStyle.Render("↳ " + m.replyLabel(e.msg.InReplyTo))}, "  ")
	}
	if e.omitted {
		b.add([]string{mutedStyle.Render("[text not available: this message exceeds the history page size]")}, "  ")
	} else {
		for _, line := range strings.Split(payloadText(e.msg.Payload), "\n") {
			text := safeText(line)
			b.addText(text, wrap(text, width-2), "  ")
		}
	}
	b.add(styled(wrap(m.entryStatus(e), width-4), mutedStyle), "  · ")
	if e.gapBelow {
		b.add([]string{warnStyle.Render("── older messages may be missing here · /history loads them ──")}, "  ")
	}
	return b.lines
}

// lineBuilder numbers logical lines and the offsets of their wrapped parts.
type lineBuilder struct {
	id    string
	src   int
	lines []renderLine
}

func (b *lineBuilder) add(parts []string, indent string) {
	col := 0
	for _, part := range parts {
		b.lines = append(b.lines, renderLine{id: b.id, src: b.src, col: col, text: indent + part})
		col += ansi.StringWidth(ansi.Strip(part))
	}
	b.src++
}

// addText adds a wrapped plain-text line; col is each part's byte offset in
// the unwrapped text, which doesn't depend on where the wrap broke it.
func (b *lineBuilder) addText(text string, parts []string, indent string) {
	pos := 0
	for _, part := range parts {
		if i := strings.Index(text[pos:], part); i >= 0 {
			pos += i
		}
		b.lines = append(b.lines, renderLine{id: b.id, src: b.src, col: pos, text: indent + part})
		pos += len(part)
	}
	b.src++
}

func styled(parts []string, style lipgloss.Style) []string {
	for i, p := range parts {
		parts[i] = style.Render(p)
	}
	return parts
}

// wrap breaks text at word boundaries, and inside words longer than width.
func wrap(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	return strings.Split(ansi.Hardwrap(ansi.Wordwrap(text, width, ""), width, true), "\n")
}

func (m *Model) entryHeader(e *entry, width int) []string {
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
	if gap >= 2 {
		return []string{left + strings.Repeat(" ", gap) + seq}
	}
	// Narrow: the number leads, and a long sender wraps instead of being cut.
	return wrap(seq+"  "+left, width)
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
