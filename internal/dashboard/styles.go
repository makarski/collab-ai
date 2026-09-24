package dashboard

import "charm.land/lipgloss/v2"

type palette struct{ title, accent, muted, selected lipgloss.Style }

func newPalette(color bool) palette {
	p := palette{}
	if color {
		p.title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C4B5FD"))
		p.accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#67E8F9"))
		p.muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
		p.selected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#4338CA"))
	}
	return p
}
