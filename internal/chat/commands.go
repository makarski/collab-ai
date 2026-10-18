package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"collab-ai/internal/protocol"
)

// command is one slash command. args validates the argument before anything
// runs; available hides commands that can't apply now.
type command struct {
	name      string
	aliases   []string
	usage     string
	help      string
	args      func(arg string) error
	run       func(m *Model, arg string) tea.Cmd
	available func(m *Model) bool
}

// Argument rules, checked before a command runs so a malformed command never
// acts partly.
func noArgs(arg string) error {
	if arg != "" {
		return errors.New("takes no arguments")
	}
	return nil
}

func targetArg(arg string) error {
	if _, err := parseTarget(arg); err != nil {
		return err
	}
	return nil
}

func optionalTarget(arg string) error {
	if arg == "" {
		return nil
	}
	return targetArg(arg)
}

func replyArg(arg string) error {
	if arg == "cancel" {
		return nil
	}
	return optionalTarget(arg)
}

func toArg(arg string) error {
	if arg == "" {
		return nil
	}
	var ids []string
	decoder := json.NewDecoder(strings.NewReader(arg))
	if err := decoder.Decode(&ids); err != nil || decoder.More() || !onlyJSON(arg) {
		return errors.New(`expects a JSON array such as ["codex-1"], or nothing to clear`)
	}
	return nil
}

// onlyJSON reports that arg is exactly one JSON array with nothing after it.
func onlyJSON(arg string) bool {
	var raw json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(arg))
	if decoder.Decode(&raw) != nil {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func anyArg(string) error { return nil }

// parseTarget reads "#n".
func parseTarget(arg string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimPrefix(arg, "#"), 10, 64)
	if err != nil || !strings.HasPrefix(arg, "#") || n == 0 {
		return 0, errors.New("expects #n, a message number")
	}
	return n, nil
}

var commands = []command{
	{name: "agents", aliases: []string{"who"}, help: "list agents", args: noArgs, run: (*Model).agentsCommand},
	{name: "reply", aliases: []string{"r"}, usage: "[#n|cancel]", help: "reply to a received message", args: replyArg, run: (*Model).replyRun, available: (*Model).hasReceived},
	{name: "ack", usage: "[#n]", help: "acknowledge a received message (received, not approval)", args: optionalTarget, run: (*Model).ackCommand, available: (*Model).hasReceived},
	{name: "history", help: "load older messages", args: noArgs, run: (*Model).historyCommand},
	{name: "stages", usage: "#n", help: "delivery stages of a sent message", args: targetArg, run: (*Model).stagesCommand, available: (*Model).hasSent},
	{name: "to", usage: `["id", …]`, help: "address IDs literally; /to alone clears", args: toArg, run: (*Model).toCommand},
	{name: "find", usage: "[text]", help: "find a message; /find again continues", args: anyArg, run: (*Model).findCommand},
	{name: "copy", usage: "#n", help: "ask the terminal to copy a message's text", args: targetArg, run: (*Model).copyCommand},
	{name: "clear", help: "hide everything above this point (nothing is deleted)", args: noArgs, run: (*Model).clearCommand},
	{name: "help", help: "keys, commands and addressing", args: noArgs, run: func(m *Model, _ string) tea.Cmd { m.toggleHelp(); return nil }},
	{name: "quit", aliases: []string{"exit"}, help: "quit", args: noArgs, run: func(m *Model, _ string) tea.Cmd { return m.quit() }},
}

func (c command) offered(m *Model) bool { return c.available == nil || c.available(m) }

// commandCandidates lists the offered command names starting with prefix.
func (m *Model) commandCandidates(prefix string) []string {
	var out []string
	for _, c := range commands {
		if c.offered(m) && strings.HasPrefix(c.name, prefix) {
			out = append(out, c.name)
		}
	}
	return out
}

// lookupCommand resolves an exact name or alias, then a unique prefix.
func lookupCommand(name string) (command, []string) {
	var matches []command
	for _, c := range commands {
		if c.name == name || containsID(c.aliases, name) {
			return c, nil
		}
		if strings.HasPrefix(c.name, name) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	names := make([]string, len(matches))
	for i, c := range matches {
		names[i] = c.name
	}
	return command{}, names
}

// runCommandLine runs the typed command. Pasted command text is inert.
func (m *Model) runCommandLine() tea.Cmd {
	line := m.compose.cmd
	if line.pasted {
		m.status = "type the command; pasted text doesn't run"
		return nil
	}
	text := strings.TrimSpace(line.input.Value())
	if text == "" {
		m.closeCommandLine()
		return nil
	}
	name, arg, _ := strings.Cut(text, " ")
	arg = strings.TrimSpace(arg)
	c, candidates := lookupCommand(name)
	switch {
	case c.run != nil:
		if err := c.args(arg); err != nil {
			m.status = "/" + c.name + " " + err.Error()
			return nil
		}
		m.closeCommandLine()
		m.status = ""
		return c.run(m, arg)
	case len(candidates) > 1:
		m.status = "/" + safe(name) + " could be: /" + strings.Join(candidates, ", /")
	default:
		m.status = "unknown command /" + safe(name) + " (/help lists commands)"
	}
	return nil
}

// target resolves "#n" to a loaded entry, or the default when arg is empty.
func (m *Model) target(arg string, fallback func() *entry) (*entry, string) {
	if arg == "" {
		if e := fallback(); e != nil {
			return e, ""
		}
		return nil, "no message to use; give #n"
	}
	n, err := parseTarget(arg)
	if err != nil {
		return nil, err.Error()
	}
	for _, e := range m.timeline.order {
		if e.msg.Seq != n {
			continue
		}
		if e.epoch != m.epoch {
			return nil, fmt.Sprintf("#%d is from before the broker restarted; /history reloads it", n)
		}
		return e, ""
	}
	return nil, fmt.Sprintf("#%d isn't loaded; /history loads older messages", n)
}

func (m *Model) newestReceived(want func(*entry) bool) func() *entry {
	return func() *entry {
		for i := len(m.timeline.order) - 1; i >= 0; i-- {
			if e := m.timeline.order[i]; e.direction == "received" && e.epoch == m.epoch && want(e) {
				return e
			}
		}
		return nil
	}
}

func (m *Model) hasReceived() bool {
	return m.newestReceived(func(*entry) bool { return true })() != nil
}

func (m *Model) hasSent() bool {
	for _, e := range m.timeline.order {
		if e.direction == "sent" {
			return true
		}
	}
	return false
}

func (m *Model) ackCommand(arg string) tea.Cmd {
	e, reason := m.target(arg, m.newestReceived(func(e *entry) bool {
		return e.operatorStage != protocol.StageAgentAcknowledged && m.inbox.queuedAcks[e.msg.MessageID] == nil
	}))
	if e == nil {
		m.status = reason
		return nil
	}
	return m.acknowledge(e)
}

func (m *Model) replyRun(arg string) tea.Cmd {
	m.replyCommand(arg)
	return nil
}

// replyCommand freezes a reply to a received message as the draft's
// audience; it never widens an already addressed draft.
func (m *Model) replyCommand(arg string) {
	c := &m.compose
	if arg == "cancel" {
		c.reply = nil
		m.status = "reply cancelled"
		return
	}
	e, reason := m.target(arg, m.newestReceived(func(*entry) bool { return true }))
	switch {
	case e == nil:
		m.status = reason
	case e.direction != "received":
		m.status = "replies go to received messages; mention agents for a new message"
	case c.to != nil || (!c.pasted && len(parsePrefix(c.editor.Value()).tokens) > 0):
		m.status = "draft is already addressed; remove the mentions or /to first"
	default:
		c.reply = &replyTarget{messageID: e.msg.MessageID, sender: e.msg.From, seq: e.msg.Seq}
		m.status = ""
	}
}

func (m *Model) historyCommand(string) tea.Cmd {
	if m.cutoff != 0 {
		// /clear only hid rows; /history shows them again before loading more.
		m.cutoff = 0
		m.status = "cleared messages shown again"
		return nil
	}
	for i := len(m.timeline.order) - 1; i >= 0; i-- {
		if e := m.timeline.order[i]; e.gapBelow {
			return m.loadOlder(e)
		}
	}
	return m.loadOlder(nil)
}

func (m *Model) stagesCommand(arg string) tea.Cmd {
	e, reason := m.target(arg, func() *entry { return nil })
	if e == nil {
		m.status = reason
		return nil
	}
	if e.direction != "sent" {
		m.status = "stages exist only for sent messages"
		return nil
	}
	m.panel = &panel{kind: panelStages, seq: e.msg.Seq, id: e.msg.MessageID}
	return m.loadStages(e)
}

func (m *Model) toCommand(arg string) tea.Cmd {
	c := &m.compose
	switch {
	case c.reply != nil:
		m.status = "use /reply cancel first"
	case arg == "":
		c.to = nil
		m.status = "audience cleared"
	case !c.pasted && len(parsePrefix(c.editor.Value()).tokens) > 0:
		m.status = "use either /to or @mentions, not both"
	default:
		m.setTo(arg)
	}
	return nil
}

func (m *Model) setTo(arg string) {
	scope, err := ParseRecipients(arg) // toArg already required a JSON array
	switch {
	case err != nil:
		m.status = err.Error()
	case isBroadcast(scope):
		m.status = "a message without mentions already goes to everyone"
	case scope.To != "":
		m.compose.to = []string{scope.To}
	default:
		m.compose.to = scope.Group
	}
}

func (m *Model) clearCommand(string) tea.Cmd {
	if n := len(m.timeline.order); n > 0 {
		m.cutoff = m.timeline.order[n-1].msg.Seq
	}
	m.follow()
	return nil
}

func (m *Model) copyCommand(arg string) tea.Cmd {
	e, reason := m.target(arg, func() *entry { return nil })
	switch {
	case e == nil:
		m.status = reason
		return nil
	case e.omitted:
		m.status = "that message's text isn't available"
		return nil
	}
	m.status = fmt.Sprintf("copy of #%d requested (the terminal may not support it)", e.msg.Seq)
	// OSC 52 carries the text base64-encoded; peer bytes never reach the
	// terminal raw.
	return tea.SetClipboard(payloadText(e.msg.Payload))
}

func (m *Model) agentsCommand(string) tea.Cmd {
	m.panel = &panel{kind: panelAgents}
	return nil
}

func (m *Model) toggleHelp() {
	if m.panel != nil && m.panel.kind == panelHelp {
		m.panel = nil
		return
	}
	m.panel = &panel{kind: panelHelp}
}

// agentRows describes every known agent: online first, then seen.
func (m *Model) agentRows() []string {
	var rows []string
	online := map[string]bool{}
	for _, a := range m.agents.list {
		online[a.AgentID] = true
		rows = append(rows, fmt.Sprintf("● %s  online · protocol v%d", safe(a.AgentID), a.ProtocolVersion))
	}
	sort.Strings(rows)
	var seen []string
	for _, id := range m.agentCandidates("") {
		if !online[id] && id != "all" {
			seen = append(seen, "○ "+safe(id)+"  seen in this conversation")
		}
	}
	rows = append(rows, seen...)
	if m.agents.partial {
		rows = append(rows, fmt.Sprintf("(presence list is partial: %d connected in total)", m.agents.total))
	}
	if len(rows) == 0 {
		rows = []string{"no agents online or seen yet"}
	}
	return rows
}
