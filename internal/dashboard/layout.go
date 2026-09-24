package dashboard

import (
	"github.com/charmbracelet/x/ansi"
	"strconv"
	"strings"
)

// Escape peer-controlled text before any layout or terminal styling. Bound error
// text as well as identifiers; truncation is explicit, never a terminal control.
func safe(s string) string {
	runes := []rune(s)
	suffix := ""
	if len(runes) > 2048 {
		runes, suffix = runes[:2048], "… [truncated]"
	}
	quoted := strconv.Quote(string(runes))
	return quoted[1:len(quoted)-1] + suffix
}

type viewport struct{ width, height int }

func (area viewport) fit(lines []string) []string {
	out := make([]string, min(len(lines), area.height))
	for i := range out {
		out[i] = ansi.Truncate(lines[i], area.width, "…")
	}
	return out
}
func pad(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}
func at(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}
