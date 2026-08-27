package tui

import (
	"image/color"
	"os"

	"charm.land/lipgloss/v2"
)

// The TUI styles with the terminal's own 16 ANSI slots so a terminal theme
// change restyles it for free. NO_COLOR turns every color off.
type palette struct {
	accent   color.Color
	chrome   color.Color
	active   color.Color
	bright   color.Color
	alert    color.Color
	positive color.Color
	noColor  bool
}

func resolvePalette() palette {
	if os.Getenv("NO_COLOR") != "" {
		nc := lipgloss.NoColor{}
		return palette{accent: nc, chrome: nc, active: nc, bright: nc, alert: nc, positive: nc, noColor: true}
	}
	return palette{
		accent:   lipgloss.BrightBlue,
		chrome:   lipgloss.Blue,
		active:   lipgloss.Yellow,
		bright:   lipgloss.BrightWhite,
		alert:    lipgloss.Red,
		positive: lipgloss.Green,
	}
}

type styles struct {
	palette   palette
	title     lipgloss.Style
	muted     lipgloss.Style
	cursor    lipgloss.Style
	unread    lipgloss.Style
	flagged   lipgloss.Style
	selected  lipgloss.Style
	chrome    lipgloss.Style
	helpKey   lipgloss.Style
	helpDesc  lipgloss.Style
	error     lipgloss.Style
	positive  lipgloss.Style
	active    lipgloss.Style
	frame     lipgloss.Style
	bodyQuote lipgloss.Style
}

func newStyles() styles {
	p := resolvePalette()
	muted := lipgloss.NewStyle().Faint(true)
	if p.noColor {
		muted = lipgloss.NewStyle()
	}
	return styles{
		palette:   p,
		title:     lipgloss.NewStyle().Foreground(p.accent).Bold(true),
		muted:     muted,
		cursor:    lipgloss.NewStyle().Foreground(p.accent).Bold(true),
		unread:    lipgloss.NewStyle().Foreground(p.bright).Bold(true),
		flagged:   lipgloss.NewStyle().Foreground(p.active),
		selected:  lipgloss.NewStyle().Foreground(p.active).Bold(true),
		chrome:    lipgloss.NewStyle().Foreground(p.chrome),
		helpKey:   lipgloss.NewStyle().Foreground(p.chrome).Bold(true),
		helpDesc:  lipgloss.NewStyle().Foreground(p.chrome),
		error:     lipgloss.NewStyle().Foreground(p.alert),
		positive:  lipgloss.NewStyle().Foreground(p.positive),
		active:    lipgloss.NewStyle().Foreground(p.active).Bold(true),
		frame:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(p.chrome).Padding(0, 1),
		bodyQuote: muted,
	}
}
