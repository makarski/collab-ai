package chat

import (
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// composerMaxLines bounds the composer's height on screen.
const composerMaxLines = 6

// composer is the single input: the draft, its explicit audience and reply,
// the command line and the open picker.
type composer struct {
	editor textarea.Model
	pasted bool         // the draft began with pasted text: literal, never parsed
	to     []string     // /to audience
	reply  *replyTarget // frozen reply target
	cmd    *commandLine
	picker *picker
}

// replyTarget is frozen from a received entry when /reply runs.
type replyTarget struct {
	messageID string
	sender    string
	seq       uint64
}

// commandLine is the separate input for commands; it never touches the draft.
type commandLine struct {
	input  textinput.Model
	pasted bool // pasted text is inert: Enter refuses it
}

// Picker kinds.
const (
	PickerAgent   = "agent"
	PickerCommand = "command"
)

type picker struct {
	kind     string
	items    []string
	selected int
}

func newComposer() composer {
	editor := textarea.New()
	editor.ShowLineNumbers = false
	editor.Prompt = "› "
	editor.Placeholder = "Message everyone · @agent for one · / for commands"
	editor.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "alt+enter", "ctrl+j"))
	editor.SetHeight(1)
	editor.Focus()
	return composer{editor: editor}
}

// draftEmpty reports an empty draft (whitespace included).
func (c *composer) draftEmpty() bool {
	return c.editor.Value() == ""
}

// resetDraft clears the draft and the metadata that belongs to it.
func (c *composer) resetDraft() {
	c.editor.Reset()
	c.pasted, c.to, c.reply, c.picker = false, nil, nil, nil
}

// editorKey handles a key while the draft has the input.
func (m *Model) editorKey(msg tea.KeyPressMsg) tea.Cmd {
	c := &m.compose
	if c.picker != nil {
		if cmd, handled := m.pickerKey(msg); handled {
			return cmd
		}
	}
	switch msg.String() {
	case "enter":
		return m.enter()
	case "esc":
		return nil // a reply is cancelled only by /reply cancel
	}
	if msg.Text == "/" && c.draftEmpty() {
		m.openCommandLine()
		return nil
	}
	cmd := m.updateEditor(msg)
	m.afterEdit()
	return cmd
}

// enter sends, or turns a trailing backslash into a line break.
func (m *Model) enter() tea.Cmd {
	c := &m.compose
	value := c.editor.Value()
	if strings.HasSuffix(value, `\`) && c.editor.Line() == c.editor.LineCount()-1 {
		c.editor.SetValue(strings.TrimSuffix(value, `\`) + "\n")
		m.afterEdit()
		return nil
	}
	return m.submit()
}

func (m *Model) updateEditor(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.compose.editor, cmd = m.compose.editor.Update(msg)
	return cmd
}

// afterEdit refreshes the draft's derived state: the paste flag resets on an
// empty draft, the mention picker follows the cursor's token, and the
// composer grows with the text.
func (m *Model) afterEdit() {
	c := &m.compose
	if c.draftEmpty() {
		c.pasted = false
	}
	m.refreshAgentPicker()
	m.sizeComposer()
}

// sizeComposer grows the composer with its lines, up to composerMaxLines.
func (m *Model) sizeComposer() {
	lines := strings.Count(m.compose.editor.Value(), "\n") + 1
	m.compose.editor.SetHeight(max(1, min(lines, composerMaxLines)))
}

// paste inserts pasted text as one insertion; it never sends, runs a
// command or picks anything.
func (m *Model) paste(msg tea.PasteMsg) tea.Cmd {
	c := &m.compose
	if c.cmd != nil {
		c.cmd.pasted = true
		var cmd tea.Cmd
		c.cmd.input, cmd = c.cmd.input.Update(msg)
		return cmd
	}
	if c.draftEmpty() {
		c.pasted = true
	}
	c.picker = nil
	cmd := m.updateEditor(msg)
	m.sizeComposer()
	return cmd
}

// refreshAgentPicker opens the @ picker while the cursor is in an unfinished
// leading mention, and closes it otherwise.
func (m *Model) refreshAgentPicker() {
	c := &m.compose
	fragment, ok := m.mentionFragment()
	if !ok {
		c.closePicker(PickerAgent)
		return
	}
	items := m.agentCandidates(fragment)
	if len(items) == 0 {
		c.picker = nil
		return
	}
	selected := 0
	if p := c.openPicker(PickerAgent); p != nil && p.selected < len(items) {
		selected = p.selected
	}
	c.picker = &picker{kind: PickerAgent, items: items, selected: selected}
}

// openPicker is the open picker of kind, or nil.
func (c *composer) openPicker(kind string) *picker {
	if c.picker != nil && c.picker.kind == kind {
		return c.picker
	}
	return nil
}

func (c *composer) closePicker(kind string) {
	if c.openPicker(kind) != nil {
		c.picker = nil
	}
}

// mentionFragment is the "@…" token being typed at the end of the leading
// prefix, without the "@".
func (m *Model) mentionFragment() (string, bool) {
	c := &m.compose
	if c.pasted || c.editor.Line() != 0 {
		return "", false
	}
	return lastMentionToken(c.editor.Value())
}

func lastMentionToken(value string) (string, bool) {
	if !openToken(value) {
		return "", false
	}
	fields := strings.Fields(value)
	for _, f := range fields {
		if !strings.HasPrefix(f, "@") {
			return "", false // the prefix already ended
		}
	}
	return strings.TrimPrefix(fields[len(fields)-1], "@"), true
}

// openToken reports a single-line, unescaped draft whose cursor (at the end)
// is still inside a word.
func openToken(value string) bool {
	if value == "" || strings.Contains(value, "\n") {
		return false
	}
	if strings.HasPrefix(strings.TrimLeft(value, " \t"), `\@`) {
		return false
	}
	last := value[len(value)-1]
	return last != ' ' && last != '\t'
}

// agentCandidates lists agent IDs matching a fragment: online first, then
// agents seen in the conversation. The @all keyword is typed, never picked,
// so an agent named "all" stays distinct from it.
func (m *Model) agentCandidates(fragment string) []string {
	return append(m.onlineIDs(fragment), m.conversationIDs(fragment)...)
}

func (m *Model) onlineIDs(fragment string) []string {
	var ids []string
	for _, a := range m.agents.list {
		ids = append(ids, a.AgentID)
	}
	return pickable(ids, fragment, nil)
}

// conversationIDs lists agents seen in the loaded conversation that aren't online.
func (m *Model) conversationIDs(fragment string) []string {
	online := map[string]bool{}
	for _, a := range m.agents.list {
		online[a.AgentID] = true
	}
	var ids []string
	for _, e := range m.timeline.order {
		ids = append(append(ids, e.msg.From, e.msg.To), e.msg.Group...)
	}
	return pickable(ids, fragment, online)
}

// pickable keeps agent IDs that start with fragment, once each, sorted,
// leaving out the broker's own names and anything in skip.
func pickable(ids []string, fragment string, skip map[string]bool) []string {
	seen := map[string]bool{"": true, "*": true, "operator": true}
	var out []string
	for _, id := range ids {
		if seen[id] || skip[id] || !strings.HasPrefix(id, fragment) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// pickerKey handles keys while a picker is open. Tab and Enter only complete.
func (m *Model) pickerKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	p := m.compose.picker
	switch msg.String() {
	case "up":
		p.selected = (p.selected - 1 + len(p.items)) % len(p.items)
	case "down":
		p.selected = (p.selected + 1) % len(p.items)
	case "tab", "enter":
		m.complete(p.items[p.selected])
	case "esc":
		m.compose.picker = nil
	default:
		return nil, false
	}
	return nil, true
}

// complete replaces the typed "@fragment" with the picked ID. An ID that
// can't be written as a mention becomes the /to audience instead.
func (m *Model) complete(id string) {
	c := &m.compose
	value := c.editor.Value()
	cut := strings.LastIndex(value, "@")
	// An agent named "all" must never become the everyone keyword.
	if id == "all" || !mentionID.MatchString(id) {
		m.pickLiteral(id, value[:cut])
		return
	}
	c.editor.SetValue(value[:cut] + "@" + id + " ")
	c.editor.CursorEnd()
	c.picker = nil
	m.afterEdit()
}

// pickLiteral sets a /to audience for an ID that isn't a valid mention.
func (m *Model) pickLiteral(id, before string) {
	c := &m.compose
	c.picker = nil
	if strings.TrimSpace(before) != "" {
		m.status = "remove the other mentions to address " + safe(id)
		return
	}
	if c.reply != nil {
		m.status = "use /reply cancel first"
		return
	}
	c.editor.SetValue(before)
	c.editor.CursorEnd()
	c.to = []string{id}
	m.afterEdit()
}

// openCommandLine starts a command; the draft is kept as it is.
func (m *Model) openCommandLine() {
	input := textinput.New()
	input.Prompt = "/"
	input.Focus()
	m.compose.cmd = &commandLine{input: input}
	m.compose.picker = nil
	m.refreshCommandPicker()
}

func (m *Model) closeCommandLine() {
	m.compose.cmd = nil
	m.compose.picker = nil
}

// commandKey handles a key on the command line.
func (m *Model) commandKey(msg tea.KeyPressMsg) tea.Cmd {
	if cmd, handled := m.commandControl(msg); handled {
		return cmd
	}
	line := m.compose.cmd
	if msg.Text == "/" && line.input.Value() == "" {
		m.slashDraft()
		return nil
	}
	var cmd tea.Cmd
	line.input, cmd = line.input.Update(msg)
	m.refreshCommandPicker()
	return cmd
}

func (m *Model) commandControl(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.closeCommandLine()
	case "enter":
		return m.runCommandLine(), true
	case "tab":
		m.completeCommand()
	case "up", "down":
		if m.compose.picker != nil {
			m.pickerKey(msg)
		}
	default:
		return nil, false
	}
	return nil, true
}

// slashDraft handles "//": it leaves command mode and starts a draft
// beginning with "/".
func (m *Model) slashDraft() {
	c := &m.compose
	m.closeCommandLine()
	if c.draftEmpty() {
		c.editor.SetValue("/")
		c.editor.CursorEnd()
	}
}

func (m *Model) refreshCommandPicker() {
	c := &m.compose
	name, _, hasArgs := strings.Cut(c.cmd.input.Value(), " ")
	if hasArgs {
		c.picker = nil
		return
	}
	items := m.commandCandidates(argument(name))
	if len(items) == 0 {
		c.picker = nil
		return
	}
	c.picker = &picker{kind: PickerCommand, items: items}
}

// completeCommand inserts the selected command name; it never runs it.
func (m *Model) completeCommand() {
	c := &m.compose
	if c.picker == nil || c.picker.kind != PickerCommand {
		return
	}
	c.cmd.input.SetValue(c.picker.items[c.picker.selected] + " ")
	c.cmd.input.CursorEnd()
	c.picker = nil
}

// key routes a key press: global keys, then the help panel, then the
// command line, then the draft.
func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	if cmd, handled := m.globalKey(msg); handled {
		return cmd
	}
	if m.compose.cmd != nil {
		return m.commandKey(msg)
	}
	if m.panel != nil {
		if msg.String() == "esc" {
			m.panel = nil
		}
		return nil
	}
	return m.editorKey(msg)
}

func (m *Model) globalKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+q", "ctrl+c":
		return m.quit(), true
	case "f1":
		m.toggleHelp()
	case "ctrl+k":
		m.ensureCommandLine()
	case "ctrl+r":
		return m.ctrlR(), true
	case "pgup":
		return m.page(-1), true
	case "pgdown":
		return m.page(1), true
	case "ctrl+end":
		m.follow()
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) ensureCommandLine() {
	if m.compose.cmd == nil {
		m.openCommandLine()
	}
}

// page scrolls an open panel, or the transcript.
func (m *Model) page(direction int) tea.Cmd {
	if m.panel != nil {
		m.panel.scroll(direction, m.layout().transcript)
		return nil
	}
	return m.scrollPage(direction)
}

// ctrlR retries a stopped connection, otherwise replies to the newest
// message to the operator.
func (m *Model) ctrlR() tea.Cmd {
	if m.conn == OwnerBusy || m.conn == Stopped {
		return m.manualRetry()
	}
	m.replyCommand("")
	return nil
}

// manualRetry reconnects after owner_busy on reconnect, on request only.
func (m *Model) manualRetry() tea.Cmd {
	m.conn = Reconnecting
	m.status = "retrying"
	return m.dial()
}

func (m *Model) resize(width, height int) {
	m.width, m.height = width, height
	m.compose.editor.SetWidth(max(10, width))
}
