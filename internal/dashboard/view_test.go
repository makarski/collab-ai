package dashboard

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestViewFitsResizedTerminalsAndEscapesPeerText(t *testing.T) {
	m := fixture(t, nil)
	out := ready()
	hostile := "agent\x1b[2J\n\r\t\u202e"
	out.Sessions[0].AgentID = hostile
	reason := "reason\x1b]52;c;malicious\a"
	out.Sessions[0].DisconnectReason = &reason
	out.Error = "error\x1b[?1049l"
	out.SessionsTruncated = true
	out.DurablePending.RecipientsTruncated = true
	apply(m, out, nil)
	if strings.ContainsAny(m.details(), "\x1b\r\u202e\a") {
		t.Fatal("peer-controlled terminal escape reached details")
	}
	requireText(t, m.details(), `agent\x1b[2J\n\r\t\u202e`, "truncated: true")
	for _, size := range [][2]int{{1, 1}, {20, 6}, {35, 12}, {60, 18}, {120, 35}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, mode := range []string{"roster", "details", "help"} {
			m.detail, m.help = mode == "details", mode == "help"
			assertFits(t, m.View().Content, size[0], size[1])
		}
	}
}
func assertFits(t *testing.T, text string, width, height int) {
	t.Helper()
	lines := strings.Split(text, "\n")
	if len(lines) > height {
		t.Fatalf("view has %d lines, max %d", len(lines), height)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > width {
			t.Fatalf("line exceeds %d: %q", width, line)
		}
	}
	if strings.ContainsRune(text, '\x1b') {
		t.Fatal("NO_COLOR view contains an escape")
	}
}
func TestLongPeerErrorIsBoundedAndDetailsScroll(t *testing.T) {
	m := fixture(t, nil)
	out := ready()
	out.Error = strings.Repeat("界", 3000)
	apply(m, out, nil)
	requireText(t, m.brokerDetails(), "[truncated]")
	m.detail = true
	before := m.View().Content
	for range 12 {
		press(m, "j")
	}
	if m.View().Content == before {
		t.Fatal("details did not scroll")
	}
}

func TestDetailsScrollStopsAtBottomAndQuitHintSurvivesNarrowView(t *testing.T) {
	m := fixture(t, nil)
	apply(m, ready(), nil)
	m.detail = true
	for range 100 {
		press(m, "j")
	}
	bottom := m.scroll
	if bottom == 0 {
		t.Fatal("fixture should scroll")
	}
	press(m, "k")
	if m.scroll != bottom-1 {
		t.Fatal("scroll overshot the bottom")
	}
	for _, width := range []int{35, 44, 45, 64, 65, 80, 100} {
		if ansi.StringWidth((viewport{width: width}).controls()) > width {
			t.Fatalf("controls exceed %d columns", width)
		}
		requireText(t, (viewport{width: width}).controls(), "q quit")
	}
}
