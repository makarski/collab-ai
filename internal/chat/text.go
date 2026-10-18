package chat

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"collab-ai/internal/protocol"
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

// wrap breaks text at word boundaries, and inside words longer than width.
func wrap(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	return strings.Split(ansi.Hardwrap(ansi.Wordwrap(text, width, ""), width, true), "\n")
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

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
