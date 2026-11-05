package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func formatDate(value string) string {
	value = strings.TrimSpace(value)
	if t, ok := mail.ParseMessageTime(value); ok {
		local := t.Local()
		if local.Year() == time.Now().Year() {
			return local.Format("Jan 02 15:04")
		}
		return local.Format("2006-01-02")
	}
	if len(value) >= 10 {
		return value[:10]
	}
	return value
}

func displaySender(raw string) string {
	return mail.ParseSender(raw).Name
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
func renderMessages(messages []mail.Message, showLocation bool, withRecipients ...bool) func(*output.Printer) {
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
			if len(withRecipients) > 0 && withRecipients[0] {
				m.ID = linkDisplay(m.ID)
				m.Subject = linkDisplay(m.Subject)
				m.Sender = linkDisplay(m.Sender)
				m.Account = linkDisplay(m.Account)
				m.Mailbox = linkDisplay(m.Mailbox)
				m.DateReceived = linkDisplay(m.DateReceived)
			}
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
		if len(withRecipients) == 0 || !withRecipients[0] {
			p.Table(headers, rows)
			return
		}
		var table bytes.Buffer
		tablePrinter := &output.Printer{Out: &table, Color: p.Color}
		tablePrinter.Table(headers, rows)
		lines := strings.Split(strings.TrimSuffix(table.String(), "\n"), "\n")
		p.Line("%s", lines[0])
		for i, message := range messages {
			p.Line("%s", lines[i+1])
			renderRecipientLines(p, message)
		}
	}
}

func renderMessage(m *mail.Message, metadataOnly bool) func(*output.Printer) {
	return renderMessageWithLinks(m, metadataOnly, false)
}

func renderMessageWithLinks(m *mail.Message, metadataOnly, allLinks bool) func(*output.Printer) {
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
		for _, field := range recipientFields(*m) {
			if field.status.State != "" && field.status.State != "not-requested" {
				pairs = append(pairs, [2]string{field.name, recipientDisplay(field.values, field.status)})
			} else if len(field.values) > 0 {
				pairs = append(pairs, [2]string{field.name, linkDisplay(strings.Join(field.values, ", "))})
			}
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
		} else {
			p.Line("%s", body)
		}
		if d := m.BodyDiagnostics; d != nil {
			for _, warning := range d.Warnings {
				p.Line("%s", p.Yellow("Extraction: "+warning))
			}
			if d.ExtractionError != "" {
				p.Line("%s", p.Yellow("Disk extraction: "+d.ExtractionError))
			}
			for _, alternative := range d.Alternatives {
				if alternative.Text != "" && !alternative.Selected {
					p.Blank()
					p.Line("MIME alternative %s (%s):", alternative.Part, alternative.MediaType)
					p.Line("%s", alternative.Text)
				}
			}
		}
		renderLinkInventory(p, m.Links, !allLinks)
		renderLinkStatus(p, m.LinkStatus, m.Unsubscribe)
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

func plural(n int, singular string) string { return output.Plural(n, singular) }

const recipientFlagUsage = "Include recipient and delivery headers without fetching bodies"

type recipientField struct {
	name   string
	values []string
	status mail.RecipientFieldStatus
}

func recipientFields(m mail.Message) []recipientField {
	return []recipientField{
		{"To", m.ToRecipients, m.RecipientStatus.To},
		{"Cc", m.CcRecipients, m.RecipientStatus.Cc},
		{"Bcc", m.BccRecipients, m.RecipientStatus.Bcc},
		{"Delivered-To", m.DeliveredTo, m.RecipientStatus.DeliveredTo},
		{"X-Original-To", m.OriginalTo, m.RecipientStatus.OriginalTo},
	}
}

func recipientDisplay(values []string, status mail.RecipientFieldStatus) string {
	switch status.State {
	case "available":
		if len(values) == 0 {
			return "(none in stored headers)"
		}
		return linkDisplay(strings.Join(values, ", "))
	case "unavailable":
		return "(unavailable)"
	default:
		return "(not requested)"
	}
}

func renderRecipientLines(p *output.Printer, message mail.Message) {
	for _, field := range recipientFields(message) {
		p.Line("  %s: %s", field.name, recipientDisplay(field.values, field.status))
	}
}
