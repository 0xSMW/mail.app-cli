package tui

import (
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestBodyRenderingAndQuotePreserveSanitizedLineStructure(t *testing.T) {
	for _, tt := range []struct {
		name, content, body, quoted string
	}{
		{"empty", "", "(no text content)\n", "> "},
		{"blank lines", "\n\n", "\n\n\n", "> \n> \n> "},
		{"mixed newlines and controls", "  indent\r\n\tcode\x07 \r世界\u202e 👩‍💻\n", "  indent\n    code\n世界 👩‍💻\n\n", ">   indent\n>     code\n> 世界 👩‍💻\n> "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := &mail.Message{Content: tt.content, Sender: "Sender", DateReceived: "unknown"}
			r := newReader(newStyles(false))
			r.width = 80
			if got := r.render(msg); !strings.HasSuffix(got, tt.body) {
				t.Fatalf("reader = %q, want body suffix %q", got, tt.body)
			}
			if got, want := quote(msg), "On unknown, Sender wrote:\n"+tt.quoted; got != want {
				t.Fatalf("quote = %q, want %q", got, want)
			}
		})
	}
}

func TestExplicitMarkWithDuplicateTargetsPreservesReaderGuardAndSelection(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(map[bool]string{false: "unread", true: "read"}[read], func(t *testing.T) {
			m := loadedModel(t)
			current, other := m.list.messages[1], m.list.messages[2]
			key, otherKey := bodyKey(current), bodyKey(other)
			m.reader.open, m.readerKey = true, key
			m.list.selected = map[string]bool{key: true, otherKey: true}
			m.noAutoRead = map[string]bool{key: true, otherKey: true, "unrelated": true}
			if !read {
				delete(m.noAutoRead, key)
				delete(m.noAutoRead, otherKey)
			}
			_ = m.act([]mail.Message{other, current, current}, mail.BatchOptions{Action: "mark", Read: read}, mail.MarkMutator(read))
			if len(m.list.selected) != 0 {
				t.Fatalf("selection retained: %v", m.list.selected)
			}
			if m.noAutoRead[key] != !read || m.noAutoRead[otherKey] || !m.noAutoRead["unrelated"] {
				t.Fatalf("guards after read=%v: %v", read, m.noAutoRead)
			}
			if m.list.messages[1].Read != read || m.list.messages[2].Read != read {
				t.Fatalf("targets did not receive requested state: %v", m.list.messages)
			}
		})
	}
}
