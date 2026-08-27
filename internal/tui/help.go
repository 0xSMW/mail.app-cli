package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type helpBinding struct {
	key  string
	desc string
}

func (m model) helpView() string {
	if m.helpHidden {
		return m.styles.muted.Render("? help")
	}
	var parts []string
	for _, b := range m.helpBindings() {
		parts = append(parts, m.styles.helpKey.Render(b.key)+" "+m.styles.helpDesc.Render(b.desc))
	}
	line := strings.Join(parts, m.styles.muted.Render("  "))
	if m.width > 0 {
		return ansi.Wrap(line, m.width, "")
	}
	return line
}

func (m model) helpBindings() []helpBinding {
	if m.modal != nil {
		return m.modal.helpBindings()
	}
	switch m.focus {
	case focusSidebar:
		return []helpBinding{
			{"j/k", "move"}, {"enter", "open mailbox"}, {"tab", "list"}, {"/", "search"}, {"c", "compose"}, {"ctrl+r", "refresh"}, {"q", "quit"},
		}
	case focusReader:
		return []helpBinding{
			{"j/k", "scroll"}, {"n/p", "next/prev"}, {"e", "archive"}, {"#", "trash"}, {"m", "move"}, {"u", "read/unread"}, {"!", "flag"},
			{"r", "reply"}, {"R", "reply all"}, {"f", "forward"}, {"esc", "back"},
		}
	}
	bindings := []helpBinding{
		{"j/k", "move"}, {"enter", "read"}, {"space", "select"}, {"e", "archive"}, {"#", "trash"}, {"m", "move"}, {"u", "read/unread"}, {"!", "flag"},
		{"/", "search"}, {"c", "compose"}, {"r", "reply"}, {"1-9", "mailbox"}, {"tab", "sidebar"}, {"ctrl+r", "refresh"}, {"q", "quit"},
	}
	if m.list.mode == listSearch {
		bindings = append([]helpBinding{{"esc", "leave search"}}, bindings...)
	}
	return bindings
}
