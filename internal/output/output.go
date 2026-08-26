// Package output is the single place the CLI writes results. A command hands
// the writer one Result; the writer decides between a JSON envelope, bare
// data, an ID list, a count, or a human table based on flags and the
// terminal.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/itchyny/gojq"
)

// Format is the resolved output mode.
type Format string

const (
	FormatPlain Format = "plain"
	FormatJSON  Format = "json"
	FormatQuiet Format = "quiet"
	FormatIDs   Format = "ids"
	FormatCount Format = "count"
)

// Flags mirrors the root persistent flags that pick a format.
type Flags struct {
	JSON    bool
	Plain   bool
	Quiet   bool
	IDsOnly bool
	Count   bool
	Agent   bool
	NoColor bool
	JQ      string
}

// Resolve picks the format. Precedence: --count, --ids-only, --quiet,
// --json/--jq/--agent, --plain, the configured default, then auto (plain on
// a terminal, JSON when piped).
func Resolve(f Flags, configured string, stdoutIsTTY bool) (Format, error) {
	wantsJSON := f.JSON || f.JQ != "" || f.Agent
	if f.Plain && wantsJSON {
		return "", clierr.Usage("--plain cannot be combined with --json, --jq, or --agent")
	}
	if f.JQ != "" && (f.IDsOnly || f.Count) {
		return "", clierr.Usage("--jq cannot be combined with --ids-only or --count")
	}
	switch {
	case f.Count:
		return FormatCount, nil
	case f.IDsOnly:
		return FormatIDs, nil
	case f.Quiet:
		return FormatQuiet, nil
	case wantsJSON:
		return FormatJSON, nil
	case f.Plain:
		return FormatPlain, nil
	}
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "json":
		return FormatJSON, nil
	case "plain":
		return FormatPlain, nil
	}
	if stdoutIsTTY {
		return FormatPlain, nil
	}
	return FormatJSON, nil
}

// IsTerminal reports whether f is a character device.
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// ColorEnabled applies NO_COLOR, TERM=dumb, and the --no-color flag.
func ColorEnabled(format Format, stdoutIsTTY bool, noColorFlag bool, getenv func(string) string) bool {
	if format != FormatPlain || !stdoutIsTTY || noColorFlag {
		return false
	}
	if getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return false
	}
	return true
}

// Writer renders results and errors.
type Writer struct {
	Format        Format
	Stdout        io.Writer
	Stderr        io.Writer
	Color         bool
	JQ            string
	Command       string
	SchemaVersion int
	started       time.Time
	jqCode        *gojq.Code
	pending       []string
}

// New builds a writer. command is the space-joined command path used in
// meta. The jq expression is compiled here so a bad one fails before the
// command runs.
func New(format Format, stdout, stderr io.Writer, color bool, jq, command string, schemaVersion int) (*Writer, error) {
	w := &Writer{
		Format:        format,
		Stdout:        stdout,
		Stderr:        stderr,
		Color:         color,
		JQ:            jq,
		Command:       command,
		SchemaVersion: schemaVersion,
		started:       time.Now(),
	}
	if jq != "" {
		query, err := gojq.Parse(jq)
		if err != nil {
			return nil, clierr.Usagef("invalid --jq expression: %v", err)
		}
		code, err := gojq.Compile(query)
		if err != nil {
			return nil, clierr.Usagef("invalid --jq expression: %v", err)
		}
		w.jqCode = code
	}
	return w, nil
}

// AddNotice queues a warning for the next Write. In plain mode it is
// printed to stderr right away.
func (w *Writer) AddNotice(message string) {
	if w.Format == FormatPlain {
		p := &Printer{Out: w.Stderr, Color: w.Color}
		fmt.Fprintln(w.Stderr, p.Dim("notice: "+message))
		return
	}
	w.pending = append(w.pending, message)
}

// Result is what a command produces.
type Result struct {
	// Data is the structured value. Nil slices are emitted as [].
	Data any
	// Summary is one sentence for humans and the envelope.
	Summary string
	// Notices are things the caller should know that are not errors.
	Notices []string
	// Meta is merged into the envelope's meta object.
	Meta map[string]any
	// Plain renders the human view. When nil, pretty JSON of Data is used.
	Plain func(p *Printer)
	// Err marks the result as a failure that still carries data (a receipt
	// with failed items, an unhealthy doctor). The envelope gets ok:false
	// and the error fields; the caller exits with the error's code.
	Err *clierr.Error
}

// Envelope is the JSON success shape.
type Envelope struct {
	OK            bool           `json:"ok"`
	SchemaVersion int            `json:"schemaVersion"`
	Data          any            `json:"data"`
	Summary       string         `json:"summary,omitempty"`
	Notices       []string       `json:"notices,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
	Error         string         `json:"error,omitempty"`
	Code          string         `json:"code,omitempty"`
	ExitCode      int            `json:"exitCode,omitempty"`
	Hint          string         `json:"hint,omitempty"`
}

// ErrorEnvelope is the JSON failure shape, written to stderr.
type ErrorEnvelope struct {
	OK            bool   `json:"ok"`
	SchemaVersion int    `json:"schemaVersion"`
	Error         string `json:"error"`
	Code          string `json:"code"`
	ExitCode      int    `json:"exitCode"`
	Hint          string `json:"hint,omitempty"`
	Command       string `json:"command,omitempty"`
}

// Write renders one result in the writer's format. When r.Err is set the
// error is marked Reported and returned after the data is written.
func (w *Writer) Write(r Result) error {
	if err := w.write(r); err != nil {
		return err
	}
	if r.Err != nil {
		r.Err.Reported = true
		return r.Err
	}
	return nil
}

func (w *Writer) write(r Result) error {
	data := normalizeData(r.Data)
	r.Notices = append(append([]string(nil), w.pending...), r.Notices...)
	w.pending = nil
	if w.Format != FormatJSON && w.Format != FormatPlain {
		p := &Printer{Out: w.Stderr, Color: false}
		for _, notice := range r.Notices {
			fmt.Fprintln(w.Stderr, p.Dim("notice: "+notice))
		}
		if r.Err != nil {
			w.Error(r.Err)
		}
	}
	switch w.Format {
	case FormatCount:
		n, ok := listLength(data)
		if !ok {
			return clierr.Usage("--count needs a command that returns a list")
		}
		_, err := fmt.Fprintln(w.Stdout, n)
		return err
	case FormatIDs:
		ids, err := listIDs(data)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := fmt.Fprintln(w.Stdout, id); err != nil {
				return err
			}
		}
		return nil
	case FormatQuiet:
		if w.JQ != "" {
			return w.runJQ(data)
		}
		return writeJSON(w.Stdout, data)
	case FormatJSON:
		env := w.envelope(data, r)
		if w.JQ != "" {
			return w.runJQ(env)
		}
		return writeJSON(w.Stdout, env)
	default:
		p := &Printer{Out: w.Stdout, Color: w.Color}
		for _, notice := range r.Notices {
			fmt.Fprintln(w.Stderr, p.Dim("notice: "+notice))
		}
		var err error
		if r.Plain != nil {
			r.Plain(p)
			err = p.err
		} else {
			err = writeJSON(w.Stdout, data)
		}
		if err == nil && r.Err != nil {
			w.Error(r.Err)
		}
		return err
	}
}

func (w *Writer) envelope(data any, r Result) Envelope {
	meta := map[string]any{
		"command":    w.Command,
		"durationMs": time.Since(w.started).Milliseconds(),
	}
	if n, ok := listLength(data); ok {
		meta["count"] = n
	}
	for k, v := range r.Meta {
		meta[k] = v
	}
	env := Envelope{
		OK:            r.Err == nil,
		SchemaVersion: w.SchemaVersion,
		Data:          data,
		Summary:       r.Summary,
		Notices:       r.Notices,
		Meta:          meta,
	}
	if r.Err != nil {
		env.Error = r.Err.Message
		env.Code = string(r.Err.Code)
		env.ExitCode = clierr.ExitCode(r.Err.Code)
		env.Hint = r.Err.Hint
	}
	return env
}

// Error renders a failure. JSON-style formats get an envelope on stderr;
// plain mode gets a message and hint.
func (w *Writer) Error(err *clierr.Error) {
	if err == nil {
		return
	}
	if w.Format == FormatPlain {
		p := &Printer{Out: w.Stderr, Color: w.Color}
		fmt.Fprintln(w.Stderr, p.Red("mail-app-cli: "+err.Message))
		if err.Hint != "" {
			fmt.Fprintln(w.Stderr, p.Dim("  hint: "+err.Hint))
		}
		return
	}
	_ = writeJSON(w.Stderr, ErrorEnvelope{
		OK:            false,
		SchemaVersion: w.SchemaVersion,
		Error:         err.Message,
		Code:          string(err.Code),
		ExitCode:      clierr.ExitCode(err.Code),
		Hint:          err.Hint,
		Command:       w.Command,
	})
}

func (w *Writer) runJQ(input any) error {
	code := w.jqCode
	if code == nil {
		return clierr.Usage("--jq expression was not compiled")
	}
	// gojq wants the generic shapes encoding/json produces.
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return err
	}
	iter := code.Run(generic)
	for {
		v, ok := iter.Next()
		if !ok {
			return nil
		}
		if jqErr, isErr := v.(error); isErr {
			return clierr.Usagef("--jq: %v", jqErr)
		}
		if s, isString := v.(string); isString {
			if _, err := fmt.Fprintln(w.Stdout, s); err != nil {
				return err
			}
			continue
		}
		if err := writeJSON(w.Stdout, v); err != nil {
			return err
		}
	}
}

func writeJSON(out io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

func normalizeData(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		return reflect.MakeSlice(rv.Type(), 0, 0).Interface()
	}
	return v
}

func listLength(v any) (int, bool) {
	if v == nil {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return 0, false
	}
	return rv.Len(), true
}

func listIDs(v any) ([]string, error) {
	rv := reflect.ValueOf(v)
	if v == nil || rv.Kind() != reflect.Slice {
		return nil, clierr.Usage("--ids-only needs a command that returns a list")
	}
	ids := make([]string, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		id, ok := elementID(rv.Index(i))
		if !ok {
			return nil, clierr.Usage("--ids-only needs list items with an id")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func elementID(rv reflect.Value) (string, bool) {
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return "", false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		field := rv.FieldByName("ID")
		if !field.IsValid() {
			return "", false
		}
		return fmt.Sprint(field.Interface()), true
	case reflect.Map:
		for _, key := range []string{"id", "ID"} {
			value := rv.MapIndex(reflect.ValueOf(key))
			if value.IsValid() {
				return fmt.Sprint(value.Interface()), true
			}
		}
	}
	return "", false
}
