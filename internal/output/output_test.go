package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
)

type item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newTestWriter(format Format, jq string) (*Writer, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	w, err := New(format, &out, &errOut, false, jq, "test cmd", 1)
	if err != nil {
		w, _ = New(format, &out, &errOut, false, "", "test cmd", 1)
		w.JQ = jq
	}
	return w, &out, &errOut
}

func TestResolvePrecedence(t *testing.T) {
	cases := []struct {
		flags      Flags
		configured string
		tty        bool
		want       Format
	}{
		{Flags{}, "auto", true, FormatPlain},
		{Flags{}, "auto", false, FormatJSON},
		{Flags{}, "json", true, FormatJSON},
		{Flags{}, "plain", false, FormatPlain},
		{Flags{JSON: true}, "plain", true, FormatJSON},
		{Flags{JQ: ".data"}, "plain", true, FormatJSON},
		{Flags{Agent: true}, "plain", true, FormatJSON},
		{Flags{Plain: true}, "json", false, FormatPlain},
		{Flags{Quiet: true, JSON: true}, "", true, FormatQuiet},
		{Flags{IDsOnly: true, Quiet: true}, "", true, FormatIDs},
		{Flags{Count: true, IDsOnly: true}, "", true, FormatCount},
	}
	for _, tc := range cases {
		got, err := Resolve(tc.flags, tc.configured, tc.tty)
		if err != nil {
			t.Fatalf("Resolve(%+v) error: %v", tc.flags, err)
		}
		if got != tc.want {
			t.Fatalf("Resolve(%+v, %q, tty=%v) = %q, want %q", tc.flags, tc.configured, tc.tty, got, tc.want)
		}
	}
	if _, err := Resolve(Flags{Plain: true, JSON: true}, "", true); err == nil {
		t.Fatal("--plain with --json was accepted")
	}
	if _, err := Resolve(Flags{JQ: ".", Count: true}, "", true); err == nil {
		t.Fatal("--jq with --count was accepted")
	}
}

func TestJSONEnvelopeShape(t *testing.T) {
	w, out, _ := newTestWriter(FormatJSON, "")
	err := w.Write(Result{Data: []item{{ID: "1", Name: "a"}}, Summary: "one item", Meta: map[string]any{"account": "X"}})
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if env["ok"] != true || env["schemaVersion"].(float64) != 1 || env["summary"] != "one item" {
		t.Fatalf("envelope = %v", env)
	}
	meta := env["meta"].(map[string]any)
	if meta["command"] != "test cmd" || meta["count"].(float64) != 1 || meta["account"] != "X" {
		t.Fatalf("meta = %v", meta)
	}
	data := env["data"].([]any)
	if data[0].(map[string]any)["id"] != "1" {
		t.Fatalf("data = %v", data)
	}
}

func TestNilSliceBecomesEmptyArray(t *testing.T) {
	var none []item
	w, out, _ := newTestWriter(FormatQuiet, "")
	if err := w.Write(Result{Data: none}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("quiet nil slice = %q", out.String())
	}
}

func TestIDsAndCount(t *testing.T) {
	data := []item{{ID: "10"}, {ID: "20"}}
	w, out, _ := newTestWriter(FormatIDs, "")
	if err := w.Write(Result{Data: data}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "10\n20\n" {
		t.Fatalf("ids = %q", out.String())
	}
	w, out, _ = newTestWriter(FormatCount, "")
	if err := w.Write(Result{Data: data}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "2" {
		t.Fatalf("count = %q", out.String())
	}
	w, _, _ = newTestWriter(FormatCount, "")
	err := w.Write(Result{Data: map[string]any{"x": 1}})
	var typed *clierr.Error
	if err == nil || !strings.Contains(err.Error(), "--count") {
		t.Fatalf("count on non-list = %v", err)
	}
	if !errorsAs(err, &typed) || typed.Code != clierr.CodeUsage {
		t.Fatalf("count on non-list code = %v", err)
	}
}

func TestJQRunsAgainstEnvelopeOrData(t *testing.T) {
	data := []item{{ID: "1", Name: "alpha"}, {ID: "2", Name: "beta"}}
	w, out, _ := newTestWriter(FormatJSON, ".data[].name")
	if err := w.Write(Result{Data: data}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "alpha\nbeta\n" {
		t.Fatalf("jq strings = %q", out.String())
	}
	w, out, _ = newTestWriter(FormatQuiet, "length")
	if err := w.Write(Result{Data: data}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "2" {
		t.Fatalf("quiet jq = %q", out.String())
	}
	if _, err := New(FormatJSON, &bytes.Buffer{}, &bytes.Buffer{}, false, ".data[", "x", 1); err == nil {
		t.Fatal("invalid jq accepted")
	}
}

func TestPlainUsesRendererAndErrorsGoToStderr(t *testing.T) {
	w, out, errOut := newTestWriter(FormatPlain, "")
	err := w.Write(Result{Data: 1, Notices: []string{"careful"}, Plain: func(p *Printer) {
		p.Table([]string{"A", "B"}, [][]string{{"1", "2"}})
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "A  B") || !strings.Contains(out.String(), "1  2") {
		t.Fatalf("table = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "notice: careful") {
		t.Fatalf("notice = %q", errOut.String())
	}

	w.Error(clierr.Usage("nope").WithHint("do this"))
	if !strings.Contains(errOut.String(), "mail-app-cli: nope") || !strings.Contains(errOut.String(), "hint: do this") {
		t.Fatalf("plain error = %q", errOut.String())
	}

	w, _, errOut = newTestWriter(FormatJSON, "")
	w.Error(clierr.New(clierr.CodeNotFound, "gone"))
	var env ErrorEnvelope
	if err := json.Unmarshal(errOut.Bytes(), &env); err != nil {
		t.Fatalf("error envelope: %v %q", err, errOut.String())
	}
	if env.OK || env.Code != "not_found" || env.ExitCode != 2 || env.Error != "gone" {
		t.Fatalf("error envelope = %+v", env)
	}
}

func TestResultErrMarksEnvelopeNotOK(t *testing.T) {
	w, out, errOut := newTestWriter(FormatJSON, "")
	err := w.Write(Result{Data: []item{{ID: "1"}}, Err: clierr.New(clierr.CodeMutationFailed, "1 failed")})
	var typed *clierr.Error
	if !errorsAs(err, &typed) || !typed.Reported {
		t.Fatalf("Write did not return the reported error: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Code != "mutation_failed" || env.ExitCode != 6 || env.Error != "1 failed" {
		t.Fatalf("envelope = %+v", env)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr should be empty in JSON mode, got %q", errOut.String())
	}
	w, out, errOut = newTestWriter(FormatQuiet, "")
	w.AddNotice("guessed mailbox")
	_ = w.Write(Result{Data: []item{}, Err: clierr.New(clierr.CodeMutationFailed, "1 failed")})
	if !strings.Contains(errOut.String(), "notice: guessed mailbox") || !strings.Contains(errOut.String(), "mutation_failed") {
		t.Fatalf("quiet stderr = %q", errOut.String())
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("quiet stdout = %q", out.String())
	}
}

func TestTableAlignsStyledCells(t *testing.T) {
	var out bytes.Buffer
	p := &Printer{Out: &out, Color: true}
	p.Table([]string{"A", "BB"}, [][]string{{p.Dim("1"), "x"}, {"333", p.Red("y")}})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	widths := map[int]bool{}
	for _, line := range lines {
		stripped := ansiPattern.ReplaceAllString(line, "")
		widths[strings.Index(stripped, "x")+strings.Index(stripped, "y")+strings.Index(stripped, "BB")] = true
	}
	for _, line := range lines {
		stripped := ansiPattern.ReplaceAllString(line, "")
		if len(stripped) < 5 {
			t.Fatalf("line too short: %q", stripped)
		}
		if stripped[5:6] == " " && !strings.HasPrefix(stripped, "333") {
			t.Fatalf("second column misaligned in %q", stripped)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("héllo wörld", 6); got != "héllo…" {
		t.Fatalf("Truncate = %q", got)
	}
	if got := Truncate("short", 10); got != "short" {
		t.Fatalf("Truncate short = %q", got)
	}
	if got := Truncate("a\nb", 5); got != "a b" {
		t.Fatalf("Truncate newline = %q", got)
	}
}

func errorsAs(err error, target **clierr.Error) bool {
	for err != nil {
		if e, ok := err.(*clierr.Error); ok {
			*target = e
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
