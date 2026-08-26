package output

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
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

// Table prints aligned columns. Headers are bold when color is on.
func (p *Printer) Table(headers []string, rows [][]string) {
	if p.err != nil {
		return
	}
	tw := tabwriter.NewWriter(p.Out, 0, 0, 2, ' ', 0)
	if len(headers) > 0 {
		styled := make([]string, len(headers))
		for i, h := range headers {
			styled[i] = p.Bold(h)
		}
		fmt.Fprintln(tw, strings.Join(styled, "\t"))
	}
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	p.err = tw.Flush()
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
