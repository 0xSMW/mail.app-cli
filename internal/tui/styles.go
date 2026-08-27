package tui

import (
	"charm.land/lipgloss/v2"
)

// The TUI styles with the terminal's own 16 ANSI slots so a terminal theme
// change restyles it for free. With color off every style is plain text.
type styles struct {
	title      lipgloss.Style // headings, the cursor row
	muted      lipgloss.Style // secondary text, quotes
	unread     lipgloss.Style
	flagged    lipgloss.Style
	active     lipgloss.Style // the selected mailbox, selected rows, focused field
	chrome     lipgloss.Style // rules, help descriptions
	helpKey    lipgloss.Style
	error      lipgloss.Style
	positive   lipgloss.Style
	frame      lipgloss.Style
	errorFrame lipgloss.Style
}

func newStyles(color bool) styles {
	if !color {
		plain := lipgloss.NewStyle()
		frame := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
		return styles{
			title: plain, muted: plain, unread: plain, flagged: plain, active: plain, chrome: plain,
			helpKey: plain, error: plain, positive: plain, frame: frame, errorFrame: frame,
		}
	}
	frame := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Blue).Padding(0, 1)
	return styles{
		title:      lipgloss.NewStyle().Foreground(lipgloss.BrightBlue).Bold(true),
		muted:      lipgloss.NewStyle().Faint(true),
		unread:     lipgloss.NewStyle().Foreground(lipgloss.BrightWhite).Bold(true),
		flagged:    lipgloss.NewStyle().Foreground(lipgloss.Yellow),
		active:     lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true),
		chrome:     lipgloss.NewStyle().Foreground(lipgloss.Blue),
		helpKey:    lipgloss.NewStyle().Foreground(lipgloss.Blue).Bold(true),
		error:      lipgloss.NewStyle().Foreground(lipgloss.Red),
		positive:   lipgloss.NewStyle().Foreground(lipgloss.Green),
		frame:      frame,
		errorFrame: frame.BorderForeground(lipgloss.Red),
	}
}
