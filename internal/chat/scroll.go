package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// viewport is the transcript's scroll position. While following, the newest
// line is at the bottom. Otherwise the view is anchored to an entry and a line
// within it, so arrivals below and history above never move what you read.
type viewport struct {
	following bool
	anchorID  string
	anchorRow int
}

// top is the index of the first visible line for a transcript of lines and a
// window of height rows.
func (m *Model) top(lines []renderLine, height int) int {
	bottom := max(0, len(lines)-height)
	if m.view.following {
		return bottom
	}
	for i, l := range lines {
		if l.id == m.view.anchorID {
			return max(0, min(i+m.view.anchorRow, bottom))
		}
	}
	return bottom
}

// transcriptWindow is the visible part of the transcript.
func (m *Model) transcriptWindow(height int) []string {
	lines := m.transcript()
	start := m.top(lines, height)
	end := min(len(lines), start+height)
	window := make([]string, 0, end-start)
	for _, l := range lines[start:end] {
		window = append(window, l.text)
	}
	return window
}

// scrollPage moves a page up (-1) or down (1). Paging up past the oldest
// loaded message loads older history.
func (m *Model) scrollPage(direction int) tea.Cmd {
	if m.panel != nil {
		return nil
	}
	height := max(1, m.layout().transcript)
	lines := m.transcript()
	start := m.top(lines, height)
	bottom := max(0, len(lines)-height)
	if direction < 0 && start == 0 {
		m.anchor(lines, 0)
		return m.loadOlder(nil)
	}
	next := start + direction*max(1, height-1)
	if next >= bottom {
		m.follow()
		return nil
	}
	m.anchor(lines, max(0, next))
	return nil
}

// anchor pins the view to the line at index.
func (m *Model) anchor(lines []renderLine, index int) {
	if len(lines) == 0 {
		return
	}
	index = min(index, len(lines)-1)
	id := lines[index].id
	first := index
	for first > 0 && lines[first-1].id == id {
		first--
	}
	m.view = viewport{anchorID: id, anchorRow: index - first}
}

// follow returns to the newest message and clears the unread count.
func (m *Model) follow() {
	m.view = viewport{following: true}
	m.timeline.unread = 0
}

// scrollOffset is how many transcript lines are below the visible window.
func (m *Model) scrollOffset() int {
	if m.view.following {
		return 0
	}
	height := max(1, m.layout().transcript)
	lines := m.transcript()
	return max(0, len(lines)-(m.top(lines, height)+height))
}

// findCommand moves to the next older message containing text, starting
// above the top of the view; /find alone continues the last search.
func (m *Model) findCommand(arg string) tea.Cmd {
	if arg != "" {
		m.find = arg
	}
	if m.find == "" {
		m.status = "/find needs text"
		return nil
	}
	lines := m.transcript()
	height := max(1, m.layout().transcript)
	start := m.top(lines, height)
	want := strings.ToLower(m.find)
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		e := m.timeline.order[i]
		at := firstLine(lines, e.msg.MessageID)
		if at < 0 || at >= start || e.omitted || !strings.Contains(strings.ToLower(payloadText(e.msg.Payload)), want) {
			continue
		}
		m.anchor(lines, at)
		m.status = fmt.Sprintf("found #%d · /find continues", e.msg.Seq)
		return nil
	}
	m.status = "no older message contains " + safe(m.find)
	return nil
}

func firstLine(lines []renderLine, id string) int {
	for i, l := range lines {
		if l.id == id {
			return i
		}
	}
	return -1
}

// Panels replace the transcript until Esc.
const (
	panelHelp   = "help"
	panelAgents = "agents"
	panelStages = "stages"
)

type panel struct {
	kind string
	seq  uint64
	id   string
}

func (m *Model) panelRows(height int) []string {
	var rows []string
	switch m.panel.kind {
	case panelHelp:
		rows = m.helpRows()
	case panelAgents:
		rows = append([]string{titleStyle.Render("Agents") + mutedStyle.Render("  (Esc closes)")}, m.agentRows()...)
	case panelStages:
		rows = m.stageRows()
	}
	var out []string
	for _, row := range rows {
		out = append(out, wrap(row, max(minWidth, m.width))...)
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

func (m *Model) stageRows() []string {
	rows := []string{titleStyle.Render(fmt.Sprintf("Delivery of #%d", m.panel.seq)) + mutedStyle.Render("  (Esc closes)")}
	e := m.timeline.entries[m.panel.id]
	if e == nil {
		return append(rows, "that message is no longer loaded")
	}
	for _, st := range e.view().Stages {
		rows = append(rows, fmt.Sprintf("  %s  %s", safe(st.AgentID), stageLabel(st.Stage)))
	}
	if e.stagesTruncated {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  more recipients: /stages #%d", e.msg.Seq)))
	}
	if len(rows) == 1 {
		rows = append(rows, "  no recipient stages known yet")
	}
	return rows
}

func (m *Model) helpRows() []string {
	support := "not reported by this terminal: Shift+Enter may send; use Alt+Enter, Ctrl+J or \\ then Enter"
	if m.keyboard {
		support = "reported by this terminal"
	}
	rows := []string{
		titleStyle.Render("collab operator chat") + mutedStyle.Render("  (F1 or Esc closes)"),
		"",
		"Sending   no mention → everyone · @codex-1 @claude-1 text → just them · @all → everyone",
		"          mentions count only at the start; later @ is plain text · \\@ starts with a literal @",
		"          pasted text is literal (mentions ignored) · /to [\"id\"] for IDs that aren't @-able",
		"Keys      Enter send · Shift+Enter, Alt+Enter, Ctrl+J, \\ then Enter: new line",
		"          PgUp/PgDn scroll · Ctrl+End newest · Ctrl+R reply to newest · Ctrl+K commands · Ctrl+Q quit",
		"Commands",
	}
	for _, c := range commands {
		rows = append(rows, fmt.Sprintf("  /%-8s %-14s %s", c.name, c.usage, c.help))
	}
	return append(rows, "", "Shift+Enter detection: "+support)
}
