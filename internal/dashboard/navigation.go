package dashboard

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/protocol"
)

type identity struct{ agent, session string }
type row struct {
	id      identity
	session *protocol.SessionStatus
	pending int
}

var states = []string{"all", "transport_connected", "disconnected", "stale"}

func (m *model) rows() []row {
	query := strings.ToLower(m.filter)
	if m.tab == 1 {
		return m.inboxRows(query)
	}
	return m.sessionRows(query)
}

func (m *model) inboxRows(query string) []row {
	var rows []row
	if m.snapshot.DurablePending == nil {
		return rows
	}
	for _, recipient := range m.snapshot.DurablePending.Recipients {
		if strings.Contains(strings.ToLower(recipient.AgentID), query) {
			rows = append(rows, row{id: identity{agent: recipient.AgentID}, pending: recipient.Pending})
		}
	}
	return rows
}

func (m *model) sessionRows(query string) []row {
	var rows []row
	for i := range m.snapshot.Sessions {
		session := &m.snapshot.Sessions[i]
		if m.state != 0 && session.State != states[m.state] {
			continue
		}
		if !strings.Contains(strings.ToLower(session.AgentID), query) {
			continue
		}
		rows = append(rows, row{id: identity{session.AgentID, session.SessionID}, session: session})
	}
	return rows
}

func (m *model) syncSelection() {
	rows := m.rows()
	for i, row := range rows {
		if row.id == m.selected {
			m.cursor = i
			return
		}
	}
	m.cursor = min(m.cursor, max(0, len(rows)-1))
	m.selectRow(rows)
}

func (m *model) selectRow(rows []row) {
	m.selected = identity{}
	if len(rows) > 0 {
		m.selected = rows[m.cursor].id
	}
	m.scroll = 0
}

func (m *model) move(delta int) {
	if m.detail || m.help {
		m.scroll = max(0, min(m.scroll+delta, m.scrollLimit()))
		return
	}
	rows := m.rows()
	m.cursor = max(0, min(m.cursor+delta, len(rows)-1))
	m.selectRow(rows)
}

func (m *model) key(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return m.quit()
	}
	if m.editing {
		m.editFilter(msg)
		return nil
	}
	if key == "q" {
		return m.quit()
	}
	if delta, ok := movement(key, max(1, m.height-10)); ok {
		m.move(delta)
		return nil
	}
	if key == "r" {
		return m.refresh()
	}
	m.changeMode(key)
	return nil
}

func movement(key string, page int) (int, bool) {
	delta, ok := map[string]int{"up": -1, "k": -1, "down": 1, "j": 1, "pgup": -page, "pgdown": page}[key]
	return delta, ok
}

func (m *model) changeMode(key string) {
	switch key {
	case "enter":
		m.detail, m.help, m.scroll = !m.detail, false, 0
	case "?":
		m.help, m.detail, m.scroll = !m.help, false, 0
	case "esc":
		m.help, m.detail, m.filter, m.scroll = false, false, "", 0
	case "/":
		m.editing, m.detail, m.help = true, false, false
	case "s":
		m.state = (m.state + 1) % len(states)
	case "tab":
		m.tab = 1 - m.tab
		m.detail, m.scroll, m.cursor = false, 0, 0
		m.selected = identity{}
	}
	m.syncSelection()
}

func (m *model) editFilter(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "enter", "esc":
		m.editing = false
	case "backspace":
		_, size := utf8.DecodeLastRuneInString(m.filter)
		m.filter = m.filter[:len(m.filter)-size]
	default:
		if msg.Text != "" {
			runes := []rune(m.filter + msg.Text)
			m.filter = string(runes[:min(80, len(runes))])
		}
	}
	m.syncSelection()
}

func (m *model) quit() tea.Cmd {
	m.quitting = true
	m.cancel()
	return tea.Quit
}
