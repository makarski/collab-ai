package chat

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	mutedStyle = lipgloss.NewStyle().Faint(true)
	warnStyle  = lipgloss.NewStyle().Bold(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	youStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	pickStyle  = lipgloss.NewStyle().Reverse(true)
	needStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	queueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	doneStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	agentHues  = []string{"2", "3", "4", "5", "9", "10", "11", "12", "13", "14"}
	// The header is a colour band; the audience divider names where the draft
	// goes, everyone in the warning colour so a broadcast is never a surprise.
	bandStyle     = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("15"))
	bandTitle     = bandStyle.Bold(true)
	bandWarn      = bandStyle.Foreground(lipgloss.Color("3")).Bold(true)
	everyoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	ruleStyle     = lipgloss.NewStyle().Faint(true)
)

const (
	chatTitle   = "pairtask operator chat"
	keyReminder = "Enter send · / commands · F1 help"
	rule        = "─"
)

// Minimum terminal size; below it the chat shows a notice and won't send.
const (
	minWidth  = 20
	minHeight = 4
	pickerMax = 6
)

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
	m.markShown()
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
	if l.bar {
		rows = append(rows, m.barRow())
	}
	if l.divider {
		rows = append(rows, m.rule())
	}
	rows = append(rows, m.mainArea(l)...)
	rows = append(rows, m.pickerRows(l)...)
	if l.status {
		rows = append(rows, m.statusLine())
	}
	rows = append(rows, m.audienceDivider())
	rows = append(rows, m.composerRows(l)...)
	return fitLines(strings.Join(rows, "\n"), m.width)
}

// frameLayout is how many rows each part gets. The audience divider and one
// composer row come first, then the header, the status line, the divider
// under the header (only while the transcript keeps a row), the transcript,
// more composer rows and the picker.
type frameLayout struct {
	header, bar, divider, status bool
	transcript                   int
	picker, composer             int
}

func (m *Model) layout() frameLayout {
	l := frameLayout{composer: 1}
	rest := m.height - 2 // audience divider + one composer row
	rest = m.layoutTop(&l, rest)
	rest = m.layoutBottom(&l, rest)
	l.transcript = max(0, rest)
	return l
}

// layoutTop allocates the header, the conversation bar, the status line and
// the rule under the bar. The bar and the rule are decoration: with a status
// line to show, they leave it and one transcript row their space.
func (m *Model) layoutTop(l *frameLayout, rest int) int {
	if rest > 0 {
		l.header, rest = true, rest-1
	}
	status := m.statusLine() != ""
	need := 1
	if status {
		need = 2
	}
	if rest > need {
		l.bar, rest = true, rest-1
	}
	if rest > 0 && status {
		l.status, rest = true, rest-1
	}
	if rest > 1 {
		l.divider, rest = true, rest-1
	}
	return rest
}

// layoutBottom allocates the extra composer rows and the picker.
func (m *Model) layoutBottom(l *frameLayout, rest int) int {
	want := m.compose.editor.Height() - 1
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
	return rest
}

// header is one colour band across the width: the title and connection
// facts on the left, the key reminder on the right when it fits.
func (m *Model) header() string {
	conn := m.conn
	if m.conn == Connected && !m.baseline {
		conn = "syncing"
	}
	online := len(m.agents.list)
	var h headerText
	switch {
	case m.width >= 76:
		h = headerText{chatTitle, fmt.Sprintf("  %s · %d online · unread %d · inbox %d", conn, online, m.unreadIn(allKey), m.inbox.pending)}
	case m.width >= 52:
		h = headerText{"operator chat", fmt.Sprintf("  %s · %d online · %d unread", conn, online, m.unreadIn(allKey))}
	default:
		h = headerText{"", fmt.Sprintf("%s · %d on · %d new", conn, online, m.unreadIn(allKey))}
	}
	return m.band(h)
}

// headerText is the header's title and the facts beside it.
type headerText struct{ title, facts string }

// band lays the header out on a background that spans the whole width.
func (m *Model) band(h headerText) string {
	left := bandTitle.Render(h.title) + bandStyle.Render(h.facts)
	used := ansi.StringWidth(h.title) + ansi.StringWidth(h.facts)
	if reminder := m.ackReminder(); reminder != "" && m.width >= 76 {
		left += bandStyle.Render(" · ") + reminder
		used += 3 + ansi.StringWidth(reminder)
	}
	right := ""
	if gap := m.width - used - ansi.StringWidth(keyReminder); gap >= 2 {
		right = bandStyle.Render(strings.Repeat(" ", gap) + keyReminder)
		used = m.width
	}
	return left + right + bandStyle.Render(strings.Repeat(" ", max(0, m.width-used)))
}

// rule is the divider between the header and the transcript.
func (m *Model) rule() string {
	return ruleStyle.Render(strings.Repeat(rule, max(0, m.width)))
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

func (m *Model) pickerRows(l frameLayout) []string {
	p, n := m.compose.picker, l.picker
	if p == nil || n == 0 {
		return nil
	}
	start := max(0, min(p.selected-n+1, len(p.items)-n))
	var rows []string
	for i := start; i < min(start+n, len(p.items)); i++ {
		label := m.pickerLabel(p, p.items[i])
		if i == p.selected {
			label = pickStyle.Render(label)
		}
		rows = append(rows, label)
	}
	return rows
}

func (m *Model) pickerLabel(p *picker, item string) string {
	switch p.kind {
	case PickerAgent:
		return "  @" + safe(item)
	case PickerAck:
		return m.ackLabel(item)
	}
	c := byName(item)
	return fmt.Sprintf("  /%s %s  %s", c.name, c.usage, mutedStyle.Render(c.help))
}

// composerRows renders the editor at the rows the layout gave it; on a small
// terminal that is fewer than it sized itself to.
func (m *Model) composerRows(l frameLayout) []string {
	c := &m.compose
	if c.cmd != nil {
		return []string{c.cmd.input.View()}
	}
	if c.editor.Height() != l.composer {
		c.editor.SetHeight(l.composer)
	}
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
		return fmt.Sprintf("reply to #%d → %s", r.seq, safe(r.sender))
	case a.everyone:
		return fmt.Sprintf("to everyone (%d online%s)", len(m.agents.list), m.partialNote())
	}
	ids := make([]string, len(a.ids))
	for i, id := range a.ids {
		ids[i] = safe(id)
	}
	return "to " + strings.Join(ids, ", ")
}

func (m *Model) partialNote() string {
	if m.agents.partial {
		return ", partial list"
	}
	return ""
}

// audienceDivider is the rule above the composer that names where the draft
// goes: "── to codex-1 ────" in that agent's colour, everyone in the warning
// colour, a blocking reason in the error colour.
func (m *Model) audienceDivider() string {
	text := m.hintText()
	label := m.audienceStyle(text).Render(text)
	fill := max(0, m.width-ansi.StringWidth(text)-4)
	return ruleStyle.Render(rule+rule+" ") + label + ruleStyle.Render(" "+strings.Repeat(rule, fill))
}

// audienceStyle colours the audience label by what the draft would do.
func (m *Model) audienceStyle(text string) lipgloss.Style {
	if strings.HasPrefix(text, "✗") {
		return errStyle
	}
	if m.compose.cmd != nil {
		return titleStyle
	}
	a := m.resolveAudience()
	switch {
	case a.source == SourceReply:
		return agentStyle(m.compose.reply.sender)
	case a.everyone:
		return everyoneStyle
	case len(a.ids) > 0:
		return agentStyle(a.ids[0])
	}
	return titleStyle
}
