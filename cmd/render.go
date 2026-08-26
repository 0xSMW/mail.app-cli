package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

func formatDate(value string) string {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, value); err == nil {
			local := t.Local()
			if local.Year() == time.Now().Year() {
				return local.Format("Jan 02 15:04")
			}
			return local.Format("2006-01-02")
		}
	}
	if len(value) >= 10 {
		return value[:10]
	}
	return value
}

func displaySender(raw string) string {
	raw = strings.TrimSpace(raw)
	if idx := strings.LastIndex(raw, "<"); idx > 0 {
		name := strings.Trim(strings.TrimSpace(raw[:idx]), `"'`)
		if name != "" {
			return name
		}
	}
	return raw
}

func messageFlags(p *output.Printer, m mail.Message) string {
	unread := " "
	if !m.Read {
		unread = p.Cyan("•")
	}
	flagged := " "
	if m.Flagged {
		flagged = p.Yellow("⚑")
	}
	return unread + flagged
}

// renderMessages prints the message table. showLocation adds the
// account/mailbox column for cross-mailbox listings.
func renderMessages(messages []mail.Message, showLocation bool) func(*output.Printer) {
	return func(p *output.Printer) {
		if len(messages) == 0 {
			p.Line("%s", p.Dim("no messages"))
			return
		}
		headers := []string{"ID", "DATE", "  ", "FROM", "SUBJECT"}
		if showLocation {
			headers = append(headers, "LOCATION")
		}
		rows := make([][]string, 0, len(messages))
		for _, m := range messages {
			subject := output.Truncate(m.Subject, 70)
			if !m.Read {
				subject = p.Bold(subject)
			}
			row := []string{
				p.Dim(m.ID),
				formatDate(m.DateReceived),
				messageFlags(p, m),
				output.Truncate(displaySender(m.Sender), 28),
				subject,
			}
			if showLocation {
				row = append(row, p.Dim(m.Account+"/"+m.Mailbox))
			}
			rows = append(rows, row)
		}
		p.Table(headers, rows)
	}
}

func renderMessage(m *mail.Message, metadataOnly bool) func(*output.Printer) {
	return func(p *output.Printer) {
		state := []string{}
		if !m.Read {
			state = append(state, "unread")
		}
		if m.Flagged {
			state = append(state, "flagged")
		}
		pairs := [][2]string{
			{"From", m.Sender},
		}
		if len(m.ToRecipients) > 0 {
			pairs = append(pairs, [2]string{"To", strings.Join(m.ToRecipients, ", ")})
		}
		if len(m.CcRecipients) > 0 {
			pairs = append(pairs, [2]string{"Cc", strings.Join(m.CcRecipients, ", ")})
		}
		pairs = append(pairs,
			[2]string{"Date", formatDate(m.DateReceived)},
			[2]string{"Subject", p.Bold(m.Subject)},
			[2]string{"Location", m.Account + "/" + m.Mailbox},
		)
		id := "id " + m.ID
		if len(state) > 0 {
			id += " (" + strings.Join(state, ", ") + ")"
		}
		pairs = append(pairs, [2]string{"Message", id})
		p.KeyValues(pairs)
		if metadataOnly {
			return
		}
		p.Blank()
		body := strings.TrimRight(m.Content, "\n")
		if body == "" {
			p.Line("%s", p.Dim("(no text content)"))
			return
		}
		p.Line("%s", body)
	}
}

func renderMailboxes(mailboxes []mail.Mailbox) func(*output.Printer) {
	return func(p *output.Printer) {
		if len(mailboxes) == 0 {
			p.Line("%s", p.Dim("no mailboxes"))
			return
		}
		rows := make([][]string, 0, len(mailboxes))
		for _, mb := range mailboxes {
			unread := fmt.Sprint(mb.UnreadCount)
			if mb.UnreadCount > 0 {
				unread = p.Bold(unread)
			}
			rows = append(rows, []string{mb.Account, mb.Name, unread, fmt.Sprint(mb.TotalCount)})
		}
		p.Table([]string{"ACCOUNT", "MAILBOX", "UNREAD", "TOTAL"}, rows)
	}
}

func renderAccounts(accounts []mail.Account) func(*output.Printer) {
	return func(p *output.Printer) {
		rows := make([][]string, 0, len(accounts))
		for _, a := range accounts {
			enabled := p.Green("yes")
			if !a.Enabled {
				enabled = p.Dim("no")
			}
			rows = append(rows, []string{a.Name, a.EmailAddress, enabled, p.Dim(a.ID)})
		}
		p.Table([]string{"NAME", "EMAIL", "ENABLED", "ID"}, rows)
	}
}

func renderAttachments(attachments []mail.Attachment) func(*output.Printer) {
	return func(p *output.Printer) {
		if len(attachments) == 0 {
			p.Line("%s", p.Dim("no attachments"))
			return
		}
		rows := make([][]string, 0, len(attachments))
		for _, a := range attachments {
			rows = append(rows, []string{fmt.Sprint(a.Index), a.Name, formatSize(a.FileSize), a.MimeType})
		}
		p.Table([]string{"#", "NAME", "SIZE", "TYPE"}, rows)
	}
}

func formatSize(bytes int) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func renderKeyValueList(rows [][2]string) func(*output.Printer) {
	return func(p *output.Printer) { p.KeyValues(rows) }
}

func renderTable(headers []string, rows [][]string, empty string) func(*output.Printer) {
	return func(p *output.Printer) {
		if len(rows) == 0 {
			p.Line("%s", p.Dim(empty))
			return
		}
		p.Table(headers, rows)
	}
}

func renderLine(format string, args ...any) func(*output.Printer) {
	return func(p *output.Printer) { p.Line(format, args...) }
}

func plural(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}
