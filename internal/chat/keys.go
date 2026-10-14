package chat

import (
	tea "charm.land/bubbletea/v2"
)

var focusOrder = []string{FocusRecipients, FocusEditor, FocusTimeline}

// key routes a key press: global keys first, then the confirmation prompt,
// then the focused area.
func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	if cmd, handled := m.globalKey(msg.String()); handled {
		return cmd
	}
	if m.send.confirm != nil {
		return m.confirmKey(msg.String())
	}
	switch m.focus {
	case FocusEditor:
		return m.editorKey(msg)
	case FocusRecipients:
		return m.recipientKey(msg)
	}
	return m.timelineKey(msg.String())
}

func (m *Model) globalKey(k string) (tea.Cmd, bool) {
	switch k {
	case "ctrl+q", "ctrl+c":
		return m.quit(), true
	case "f1":
		m.help = !m.help
		return nil, true
	case "tab":
		m.cycleFocus(1)
		return nil, true
	case "shift+tab":
		m.cycleFocus(-1)
		return nil, true
	case "ctrl+r":
		return m.manualRetry(), true
	}
	return nil, false
}

func (m *Model) confirmKey(k string) tea.Cmd {
	switch k {
	case "y", "Y":
		return m.confirmAnswer(true)
	case "n", "N", "esc":
		return m.confirmAnswer(false)
	}
	return nil
}

// editorKey: Enter sends; the textarea's newline binding is shift+enter,
// alt+enter and ctrl+j.
func (m *Model) editorKey(msg tea.KeyPressMsg) tea.Cmd {
	return m.fieldKey(msg, m.submit, m.updateEditor)
}

// recipientKey: Enter moves on to the editor.
func (m *Model) recipientKey(msg tea.KeyPressMsg) tea.Cmd {
	return m.fieldKey(msg, m.toEditor, m.updateRecipients)
}

// fieldKey runs enter, returns to the timeline on Esc, and passes every other
// key to the focused field.
func (m *Model) fieldKey(msg tea.KeyPressMsg, enter func() tea.Cmd, update func(tea.Msg) tea.Cmd) tea.Cmd {
	switch msg.String() {
	case "enter":
		return enter()
	case "esc":
		m.setFocus(FocusTimeline)
		return nil
	}
	return update(msg)
}

func (m *Model) toEditor() tea.Cmd {
	m.setFocus(FocusEditor)
	return nil
}

func (m *Model) updateEditor(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return cmd
}

func (m *Model) updateRecipients(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.recipients, cmd = m.recipients.Update(msg)
	return cmd
}

func (m *Model) timelineKey(k string) tea.Cmd {
	switch k {
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		return m.pageUp()
	case "pgdown":
		m.move(10)
	case "a":
		return m.onSelected(m.acknowledge)
	case "r":
		return m.onSelected(func(e *entry) tea.Cmd { m.reply(e); return nil })
	case "s":
		return m.onSelected(m.loadStages)
	}
	return nil
}

func (m *Model) onSelected(f func(*entry) tea.Cmd) tea.Cmd {
	e := m.timeline.selectedEntry()
	if e == nil {
		m.status = "select a message first (↑/↓)"
		return nil
	}
	return f(e)
}

// move changes the selection; reaching the newest message clears unread.
func (m *Model) move(delta int) {
	n := len(m.timeline.order)
	if n == 0 {
		return
	}
	if m.timeline.selected < 0 {
		m.timeline.selected = n - 1
	}
	m.timeline.selected = max(0, min(n-1, m.timeline.selected+delta))
	if m.timeline.atBottom() {
		m.timeline.unread = 0
	}
}

// pageUp fills the gap below a selected gap marker, scrolls, or loads older
// history at the top.
func (m *Model) pageUp() tea.Cmd {
	if e := m.timeline.selectedEntry(); e != nil && e.gapBelow {
		return m.loadOlder()
	}
	if m.timeline.selected > 0 {
		m.move(-10)
		return nil
	}
	return m.loadOlder()
}

// paste inserts pasted text as one insertion into the focused field; an
// embedded newline never sends.
func (m *Model) paste(msg tea.PasteMsg) tea.Cmd {
	switch m.focus {
	case FocusEditor:
		return m.updateEditor(msg)
	case FocusRecipients:
		return m.updateRecipients(msg)
	}
	return nil
}

func (m *Model) cycleFocus(step int) {
	i := 0
	for j, f := range focusOrder {
		if f == m.focus {
			i = j
		}
	}
	m.setFocus(focusOrder[(i+step+len(focusOrder))%len(focusOrder)])
}

func (m *Model) setFocus(focus string) {
	m.focus = focus
	m.editor.Blur()
	m.recipients.Blur()
	switch focus {
	case FocusEditor:
		m.editor.Focus()
	case FocusRecipients:
		m.recipients.Focus()
	}
}

// manualRetry reconnects after owner_busy on reconnect, on request only.
func (m *Model) manualRetry() tea.Cmd {
	if m.conn != OwnerBusy && m.conn != Stopped {
		return nil
	}
	m.conn = Reconnecting
	m.status = "retrying"
	return m.dial()
}

func (m *Model) resize(width, height int) {
	m.width, m.height = width, height
	m.editor.SetWidth(max(10, width-4))
	m.editor.SetHeight(3)
	m.recipients.SetWidth(max(10, width-16))
}
