package output

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Printer renders the human view. Every method records the first write
// error so callers can check once.
type Printer struct {
	Out   io.Writer
	Color bool
	err   error
}

func (p *Printer) write(s string) {
	if p.err != nil {
		return
	}
	_, p.err = io.WriteString(p.Out, s)
}

// Line prints a formatted line.
func (p *Printer) Line(format string, args ...any) {
	p.write(fmt.Sprintf(format, args...) + "\n")
}

// Blank prints an empty line.
func (p *Printer) Blank() { p.write("\n") }

var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

// visibleWidth counts runes after stripping ANSI codes.
func visibleWidth(s string) int {
	return utf8.RuneCountInString(ansiPattern.ReplaceAllString(s, ""))
}

// Table prints aligned columns, measuring width without ANSI codes so
// styled cells line up. Headers are bold when color is on.
func (p *Printer) Table(headers []string, rows [][]string) {
	if p.err != nil {
		return
	}
	all := make([][]string, 0, len(rows)+1)
	if len(headers) > 0 {
		styled := make([]string, len(headers))
		for i, h := range headers {
			styled[i] = p.Bold(h)
		}
		all = append(all, styled)
	}
	all = append(all, rows...)
	var widths []int
	for _, row := range all {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			if w := visibleWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	var b strings.Builder
	for _, row := range all {
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-visibleWidth(cell)+2))
			}
		}
		b.WriteString("\n")
	}
	p.write(b.String())
}

// KeyValues prints a two-column block with aligned keys.
func (p *Printer) KeyValues(pairs [][2]string) {
	width := 0
	for _, pair := range pairs {
		if len(pair[0]) > width {
			width = len(pair[0])
		}
	}
	for _, pair := range pairs {
		p.write(fmt.Sprintf("%s  %s\n", p.Dim(pad(pair[0]+":", width+1)), pair[1]))
	}
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func (p *Printer) style(code, s string) string {
	if !p.Color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Bold, Dim, Red, Green, Yellow, Cyan wrap s in ANSI-16 codes when color is on.
func (p *Printer) Bold(s string) string   { return p.style("1", s) }
func (p *Printer) Dim(s string) string    { return p.style("2", s) }
func (p *Printer) Red(s string) string    { return p.style("31", s) }
func (p *Printer) Green(s string) string  { return p.style("32", s) }
func (p *Printer) Yellow(s string) string { return p.style("33", s) }
func (p *Printer) Cyan(s string) string   { return p.style("36", s) }

// Truncate cuts s to width runes, adding an ellipsis when it was longer.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", ""))
	if len(runes) <= width {
		return string(runes)
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
