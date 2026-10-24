package chat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// ConversationAll names the common chat in the state view and in /chat. In
// the model the common chat is the empty key.
const ConversationAll = "all"

// savedViewsMax bounds the saved conversation state, All included; the live
// directory of agents is not bounded here.
const savedViewsMax = 64

// SourceConversation is the audience a 1:1 view gives a draft by itself.
const SourceConversation = "conversation"

// view is one conversation's saved state: where it was scrolled, what it
// hides and searches for, and its own draft with the draft's reply target
// and explicit audience. The selected conversation's state lives in the
// model's fields; it is saved here when another one is selected.
type view struct {
	port    viewport
	cutoff  uint64
	find    string
	found   uint64
	compose composer
	used    int // selection order, for eviction
}

// conversations is the directory of agents by first appearance, the
// selected conversation and the saved views.
type conversations struct {
	order    []string // agent IDs by first appearance in this session
	selected string   // "" is All
	saved    map[string]*view
	uses     int
}

// ConversationView is one entry of the conversation bar. ID is the raw agent
// ID and empty for the common chat, which All marks, so an agent literally
// named "all" is never confused with it.
type ConversationView struct {
	ID       string
	All      bool
	Position int
	Unread   int
	Selected bool
	Draft    bool
}

// refreshDirectory keeps the directory in step with presence and the loaded
// timeline: agents that vanished from both leave it, new ones append, and
// the selected agent stays while selected. Positions are displayed order,
// so they move only when an agent leaves.
func (m *Model) refreshDirectory() {
	live := m.liveAgents()
	m.conv.order = pruneOrder(m.conv.order, live)
	m.conv.order = appendUnseen(m.conv.order, live)
}

// pruneOrder keeps the directory entries that are still live.
func pruneOrder(order []string, live agentSet) []string {
	kept := order[:0]
	for _, id := range order {
		if live.has[id] {
			kept = append(kept, id)
		}
	}
	return kept
}

// appendUnseen adds live agents the directory doesn't list yet, in order of
// first appearance.
func appendUnseen(order []string, live agentSet) []string {
	for _, id := range live.seen {
		if !slices.Contains(order, id) {
			order = append(order, id)
		}
	}
	return order
}

// agentSet is an ordered set of agent IDs.
type agentSet struct {
	has  map[string]bool
	seen []string
}

func (s *agentSet) add(id string) {
	if reservedID(id) || s.has[id] {
		return
	}
	s.has[id] = true
	s.seen = append(s.seen, id)
}

// reservedID is an ID that never names a conversation.
func reservedID(id string) bool {
	return id == "" || id == protocol.OperatorID || id == protocol.Broadcast
}

// liveAgents are the agents the directory lists: in the loaded timeline, in
// presence, selected, or holding a protected view, in that order of first
// appearance. A view with an unsent draft or a reply binding stays reachable
// even when its agent left presence and the timeline.
func (m *Model) liveAgents() agentSet {
	live := agentSet{has: map[string]bool{}}
	for _, e := range m.timeline.order {
		for _, id := range entryAgents(e) {
			live.add(id)
		}
	}
	for _, a := range m.agents.list {
		live.add(a.AgentID)
	}
	live.add(m.conv.selected)
	for key, v := range m.conv.saved {
		if v.protected() {
			live.add(key)
		}
	}
	return live
}

// entryAgents lists the agents an entry belongs to: its sender, or its
// accepted recipients.
func entryAgents(e *entry) []string {
	if e.direction == "received" {
		return []string{e.msg.From}
	}
	ids := append([]string(nil), e.msg.Group...)
	if e.msg.To != "" && e.msg.To != protocol.Broadcast {
		ids = append(ids, e.msg.To)
	}
	for id := range e.stages {
		ids = append(ids, id)
	}
	return ids
}

// inView reports whether an entry belongs to a conversation: All holds
// everything; a 1:1 with X holds what X sent and what X is an accepted
// recipient of. Presence never implies membership; a truncated stage list
// means "not yet known".
func (m *Model) inView(e *entry, key string) bool {
	if key == "" {
		return true
	}
	if e.direction == "received" {
		return e.msg.From == key
	}
	if e.msg.To == key || slices.Contains(e.msg.Group, key) {
		return true
	}
	_, ok := e.stages[key]
	return ok
}

// selectView switches to a conversation, saving the current one. A new view
// is allocated only when opened; beyond the bound, an unselected view with
// no draft and no reply binding is evicted, and when none is, the switch is
// refused and nothing changes.
func (m *Model) selectView(key string) bool {
	if key == m.conv.selected {
		return true
	}
	target := m.conv.saved[key]
	if key != "" && target == nil && !slices.Contains(m.conv.order, key) {
		m.status = "no conversation with " + safe(key)
		return false
	}
	if target == nil && !m.allocateView() {
		m.status = "conversation state limit reached; send or clear a saved draft first"
		return false
	}
	m.conv.uses++
	m.conv.saved[m.conv.selected] = &view{port: m.view, cutoff: m.cutoff, find: m.find, found: m.found, compose: m.compose, used: m.conv.uses}
	m.conv.selected = key
	if target == nil {
		target = &view{port: viewport{following: true}, compose: m.freshComposer()}
	}
	delete(m.conv.saved, key)
	m.view, m.cutoff, m.find, m.found, m.compose = target.port, target.cutoff, target.find, target.found, target.compose
	m.compose.picker = nil
	m.history.run = nil // a filtered older-history run belongs to the view that started it
	m.markShown()
	return true
}

// allocateView makes room for one more saved view.
func (m *Model) allocateView() bool {
	if len(m.conv.saved)+1 < savedViewsMax { // +1: the selected view counts
		return true
	}
	victim, best, found := "", 0, false
	for key, v := range m.conv.saved {
		if key == "" || v.protected() {
			continue // All is permanent; a draft or reply binding is never discarded
		}
		if !found || v.used < best {
			victim, best, found = key, v.used, true
		}
	}
	if !found {
		return false
	}
	delete(m.conv.saved, victim)
	return true
}

// protected views are never evicted: a nonempty draft or a reply binding.
func (v *view) protected() bool {
	return !v.compose.draftEmpty() || v.compose.reply != nil || v.compose.to != nil
}

// freshComposer is a new draft sized to the window, with the view's placeholder.
func (m *Model) freshComposer() composer {
	c := newComposer()
	c.editor.SetWidth(max(10, m.width))
	c.editor.Placeholder = m.placeholder()
	return c
}

func (m *Model) placeholder() string {
	if m.conv.selected == "" {
		return "Message everyone · @agent for one · / for commands"
	}
	return "Message " + safe(m.conv.selected) + " · / for commands"
}

// savedDraft reports whether a conversation holds an unsent draft or a reply
// binding, selected or saved.
func (m *Model) savedDraft(key string) bool {
	if key == m.conv.selected {
		return m.compose.protectedDraft()
	}
	if v := m.conv.saved[key]; v != nil {
		return v.protected()
	}
	return false
}

func (c *composer) protectedDraft() bool {
	return !c.draftEmpty() || c.reply != nil || c.to != nil
}

// cycleView moves the selection along the bar.
func (m *Model) cycleView(step int) {
	positions := append([]string{""}, m.conv.order...)
	i := slices.Index(positions, m.conv.selected)
	if i < 0 {
		i = 0
	}
	m.selectView(positions[(i+step+len(positions))%len(positions)])
}

// jumpView selects a displayed position, 1 being All.
func (m *Model) jumpView(position int) {
	if position == 1 {
		m.selectView("")
		return
	}
	if position-2 < len(m.conv.order) {
		m.selectView(m.conv.order[position-2])
	}
}

// chatCommand: /chat all, /chat <id> for the mention grammar, /chat "id" (a
// JSON string) for any other ID, including an agent literally named all.
func (m *Model) chatCommand(arg argument) tea.Cmd {
	key, err := chatTarget(string(arg))
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.refreshDirectory()
	m.selectView(key)
	return nil
}

func chatTarget(arg string) (string, error) {
	switch {
	case arg == ConversationAll:
		return "", nil
	case strings.HasPrefix(arg, `"`):
		var id string
		if err := json.Unmarshal([]byte(arg), &id); err != nil {
			return "", fmt.Errorf("/chat takes all, an agent mention, or a JSON string: %w", err)
		}
		return id, protocol.ValidateAgentID(id)
	case mentionID.MatchString(arg):
		return arg, nil
	}
	return "", fmt.Errorf("/chat takes all, an agent mention, or a JSON string")
}

func chatArg(a argument) error {
	if a.empty() {
		return fmt.Errorf("takes all, an agent mention, or a JSON string")
	}
	_, err := chatTarget(string(a))
	return err
}

// unreadCount is the number of distinct unread entries, or of those in a
// conversation when key is given with inView.
func (m *Model) unreadCount(key string, filtered bool) int {
	n := 0
	for _, e := range m.timeline.order {
		if e.unread && (!filtered || m.inView(e, key)) {
			n++
		}
	}
	return n
}

// markShown clears the unread flag of every entry with a line rendered while
// the selected conversation follows the bottom. Entries above the window or
// hidden by /clear stay unread.
func (m *Model) markShown() {
	if !m.view.following || m.tooSmall() || m.panel != nil {
		return
	}
	height := m.layout().transcript
	if height == 0 {
		return
	}
	lines := m.transcript()
	start := max(0, len(lines)-height)
	for _, l := range lines[start:] {
		if e := m.timeline.entries[l.id]; e != nil {
			e.unread = false
		}
	}
}

// conversationViews lists the bar for the state view.
func (m *Model) conversationViews() []ConversationView {
	m.refreshDirectory()
	out := []ConversationView{{All: true, Position: 1, Unread: m.unreadCount("", false), Selected: m.conv.selected == "", Draft: m.savedDraft("")}}
	for i, id := range m.conv.order {
		out = append(out, ConversationView{ID: id, Position: i + 2, Unread: m.unreadCount(id, true), Selected: m.conv.selected == id, Draft: m.savedDraft(id)})
	}
	return out
}

// barRow renders the conversation bar: positions, labels, unread badges, the
// selected one reversed in its colour, scrolled so the selected one shows.
func (m *Model) barRow() string {
	var items []string
	widths := []int{}
	selected := 0
	for _, c := range m.conversationViews() {
		name := "All"
		style := titleStyle
		if !c.All {
			name, style = safe(c.ID), agentStyle(c.ID)
		}
		label := strconv.Itoa(c.Position) + " " + name
		if c.Unread > 0 {
			label += " " + everyoneStyle.Render("•"+strconv.Itoa(c.Unread))
		}
		if c.Selected {
			selected = len(items)
			label = style.Reverse(true).Render(" " + label + " ")
		} else {
			label = " " + style.Render(label) + " "
		}
		items = append(items, label)
		widths = append(widths, len(name)+len(strconv.Itoa(c.Position))+3+badgeWidth(c.Unread))
	}
	return fitBar(items, widths, selected, m.width)
}

func badgeWidth(n int) int {
	if n == 0 {
		return 0
	}
	return 2 + len(strconv.Itoa(n))
}

// fitBar joins the items from the first one that still shows the selected
// item within the width, marking an overflow on the left with "…".
func fitBar(items []string, widths []int, selected, width int) string {
	start := 0
	for {
		used := 0
		for i := start; i <= selected; i++ {
			used += widths[i]
		}
		if used <= width-1 || start == selected {
			break
		}
		start++
	}
	row := ""
	if start > 0 {
		row = ruleStyle.Render("…")
	}
	return row + strings.Join(items[start:], "")
}
