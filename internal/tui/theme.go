package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// The dashboard's look. Colours are the terminal's 16 ANSI colours, so
// they follow the user's own theme, light or dark; Bubble Tea drops them
// under NO_COLOR or a terminal without colour. Every state also has its
// own glyph, so colour is never the only signal.

var (
	styleHead   = lipgloss.NewStyle().Bold(true)
	styleSel    = lipgloss.NewStyle().Reverse(true)
	styleFaint  = lipgloss.NewStyle().Faint(true)
	styleAccent = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	styleTitle  = styleAccent.Bold(true)
	styleBad    = lipgloss.NewStyle().Foreground(lipgloss.Red)
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	styleGood   = lipgloss.NewStyle().Foreground(lipgloss.Green)
	styleInfo   = lipgloss.NewStyle().Foreground(lipgloss.Blue)
	stylePlain  = lipgloss.NewStyle()
)

// stateLook is how a state word is drawn: its glyph and colour.
func stateLook(state string) (string, lipgloss.Style) {
	word, _, _ := strings.Cut(state, " ")
	switch word {
	case "working", "started":
		return "●", styleGood
	case "blocked":
		return "▲", styleBad.Bold(true)
	case "idle", "open", "ready":
		return "○", stylePlain
	case "starting":
		return "◌", styleInfo
	case "running":
		return "●", stylePlain
	case "review":
		return "◆", styleWarn
	case "done":
		return "✓", styleGood
	case "—", "":
		return "", styleFaint
	}
	return "·", styleFaint // stopped, exited, resolved and the like
}

// stateText is a state with its glyph, as a column shows it.
func stateText(state string) string {
	if g, _ := stateLook(state); g != "" {
		return g + " " + state
	}
	return state
}

// markStyle colours a row's marker: ! blocked, ? needs you.
func markStyle(mark string) lipgloss.Style {
	switch strings.TrimSpace(mark) {
	case "!":
		return styleBad.Bold(true)
	case "?":
		return styleWarn.Bold(true)
	}
	return stylePlain
}

// bar is a five-cell progress bar: "▰▰▱▱▱".
func bar(pct int) string {
	n := min(max((pct+10)/20, 0), 5)
	return strings.Repeat("▰", n) + strings.Repeat("▱", 5-n)
}

// pctOf is done out of total as a percent, -1 without a total.
func pctOf(done, total int) int {
	if total <= 0 {
		return -1
	}
	return done * 100 / total
}

// keysLine styles a footer's "key label · key label" list: keys in the
// accent colour, labels faint.
func keysLine(keys string) string {
	parts := strings.Split(keys, " · ")
	for i, p := range parts {
		k, label, ok := strings.Cut(p, " ")
		if !ok {
			parts[i] = styleAccent.Render(p)
			continue
		}
		parts[i] = styleAccent.Render(k) + " " + styleFaint.Render(label)
	}
	return strings.Join(parts, styleFaint.Render(" · "))
}

// todoGlyph is a todo or step's box: ✓ done, ◐ in progress, ○ to do.
func todoGlyph(status string) string {
	switch status {
	case "completed", "done":
		return styleGood.Render("✓")
	case "in_progress":
		return styleWarn.Render("◐")
	}
	return styleFaint.Render("○")
}

// countLabel is a section header with its count: "NEEDS YOU 2".
func countLabel(title string, n int) string {
	if n <= 0 {
		return title
	}
	return fmt.Sprintf("%s %d", title, n)
}
