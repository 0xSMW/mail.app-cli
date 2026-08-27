package tui

import (
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

func unsafeRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || (unicode.Is(unicode.Cf, r) && r != '‍')
}

// sanitizeLine strips control characters and escape sequences from a
// single-line field such as a subject, so a hostile header cannot drive
// the terminal, and trims it.
func sanitizeLine(s string) string {
	return strings.TrimSpace(stripControls(s))
}

// stripControls removes control and escape characters, turning tabs and
// line breaks into spaces, and keeps everything else, indentation included.
func stripControls(s string) string {
	if strings.IndexFunc(s, unsafeRune) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unsafeRune(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sanitizeBody keeps line structure and indentation but drops control
// characters. Tabs become four spaces; trailing spaces are dropped.
func sanitizeBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(stripControls(strings.ReplaceAll(line, "\t", "    ")), " ")
	}
	return strings.Join(lines, "\n")
}

// truncate and pad measure width without counting ANSI escapes, so styled
// and plain strings line up.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

func pad(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func fit(s string, width int) string {
	return pad(truncate(s, width), width)
}

// block pads lines to a width-by-height rectangle so panes join cleanly.
func block(lines []string, width, height int) string {
	out := make([]string, 0, height)
	for i := 0; i < height; i++ {
		if i < len(lines) {
			out = append(out, fit(lines[i], width))
		} else {
			out = append(out, strings.Repeat(" ", width))
		}
	}
	return strings.Join(out, "\n")
}

func formatDate(value string, now time.Time) string {
	t, ok := mail.ParseMessageTime(value)
	if !ok {
		if len(value) >= 10 {
			return value[:10]
		}
		return value
	}
	local := t.Local()
	switch {
	case local.YearDay() == now.YearDay() && local.Year() == now.Year():
		return local.Format("15:04")
	case local.Year() == now.Year():
		return local.Format("Jan 02")
	default:
		return local.Format("2006-01-02")
	}
}

func formatLongDate(value string) string {
	if t, ok := mail.ParseMessageTime(value); ok {
		return t.Local().Format("Mon, 02 Jan 2006 15:04")
	}
	return value
}

func plural(n int, singular string) string { return output.Plural(n, singular) }
