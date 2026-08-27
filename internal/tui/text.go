package tui

import (
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// sanitizeLine strips control characters and escape sequences from text
// that came from a message, so a hostile subject cannot drive the terminal.
func sanitizeLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			continue
		case unicode.Is(unicode.Cf, r) && r != '‍':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// sanitizeBody keeps line structure but drops control characters.
func sanitizeBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(sanitizeLine(strings.ReplaceAll(line, "\t", "    ")), " ")
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

func formatDate(value string) string {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, value); err == nil {
			local := t.Local()
			now := time.Now()
			switch {
			case local.YearDay() == now.YearDay() && local.Year() == now.Year():
				return local.Format("15:04")
			case local.Year() == now.Year():
				return local.Format("Jan 02")
			default:
				return local.Format("2006-01-02")
			}
		}
	}
	if len(value) >= 10 {
		return value[:10]
	}
	return value
}

func formatLongDate(value string) string {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.Local().Format("Mon, 02 Jan 2006 15:04")
		}
	}
	return value
}

func plural(n int, singular string) string {
	if n == 1 {
		return "1 " + singular
	}
	suffix := "s"
	if strings.HasSuffix(singular, "x") || strings.HasSuffix(singular, "ch") || strings.HasSuffix(singular, "s") {
		suffix = "es"
	}
	return itoa(n) + " " + singular + suffix
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
