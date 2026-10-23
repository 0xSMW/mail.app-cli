package cmd

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// Link results deliberately have no open, fetch, POST, or send operation.
type messageLinksResult struct {
	ID          string                     `json:"id"`
	Account     string                     `json:"account"`
	Mailbox     string                     `json:"mailbox"`
	Links       []mail.MessageLink         `json:"links"`
	LinkStatus  *mail.LinkExtractionStatus `json:"linkStatus"`
	Unsubscribe *mail.MessageUnsubscribe   `json:"unsubscribe,omitempty"`
}

func unsubscribeLink(link mail.MessageLink) bool {
	return link.Kind == "unsubscribe" || link.Kind == "preferences"
}

func linkResult(message *mail.Message, unsubscribeOnly bool) messageLinksResult {
	result := messageLinksResult{
		ID: message.ID, Account: message.Account, Mailbox: message.Mailbox,
		Links: []mail.MessageLink{}, LinkStatus: message.LinkStatus, Unsubscribe: message.Unsubscribe,
	}
	for _, link := range message.Links {
		if !unsubscribeOnly || unsubscribeLink(link) {
			result.Links = append(result.Links, link)
		}
	}
	return result
}

func (a *application) newLinksCmd() *cobra.Command {
	var unsubscribeOnly bool
	cmd := &cobra.Command{
		Use: "links <message-id>", Short: "List message links and unsubscribe destinations without opening them",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Read-only discovery. Reads local MIME, including partial files, without requesting Mail to render the body. Header fallback and partial sources report incomplete linkStatus and exit 5 while retaining discovered destinations. --unsubscribe includes header unsubscribe destinations and body unsubscribe/preferences candidates. oneClickAdvertised describes headers only, not verified eligibility. No URLs are opened or fetched, and no mail is sent."},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isNumericID(args[0]) {
				return clierr.Usagef("message ID %q is not numeric", args[0])
			}
			refs, notices, err := a.locateMessages(args)
			if err != nil {
				return err
			}
			ref := refs[0]
			message, err := a.mailClient.GetMessageLinksJSON(ref.Account, ref.Mailbox, ref.ID)
			if err != nil {
				return fmt.Errorf("get message links: %w", err)
			}
			if message == nil {
				return clierr.New(clierr.CodeNotFound, fmt.Sprintf("message not found: %s", ref.ID))
			}
			return a.writeMessageLinks(message, unsubscribeOnly, notices)
		},
	}
	cmd.Flags().BoolVar(&unsubscribeOnly, "unsubscribe", false, "Show unsubscribe and preferences destinations only")
	return cmd
}

func (a *application) writeMessageLinks(message *mail.Message, unsubscribeOnly bool, notices []string) error {
	result := linkResult(message, unsubscribeOnly)
	var failure *clierr.Error
	if result.LinkStatus == nil || !result.LinkStatus.Complete {
		failure = clierr.New(clierr.CodePartial, "link extraction incomplete; inspect linkStatus and available destinations")
	}
	return a.writer.Write(output.Result{
		Data: result, Err: failure, Notices: notices,
		Summary: fmt.Sprintf("%d link destinations for message %s", len(result.Links), result.ID),
		Meta:    map[string]any{"account": result.Account, "mailbox": result.Mailbox, "unsubscribeOnly": unsubscribeOnly},
		Plain: func(p *output.Printer) {
			p.Line("Message %s (%s/%s)", linkDisplay(result.ID), linkDisplay(result.Account), linkDisplay(result.Mailbox))
			renderLinkInventory(p, result.Links, false)
			renderLinkStatus(p, result.LinkStatus, result.Unsubscribe)
		},
	})
}

// Escape controls in the human view, while JSON keeps the exact destination.
// In particular, never turn a message-supplied escape sequence into terminal UI.
func linkDisplay(value string) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			fmt.Fprintf(&b, "\\u%04x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func renderLinkInventory(p *output.Printer, links []mail.MessageLink, unsubscribeOnly bool) {
	var selected []mail.MessageLink
	for _, link := range links {
		if !unsubscribeOnly || unsubscribeLink(link) {
			selected = append(selected, link)
		}
	}
	if len(selected) == 0 && unsubscribeOnly {
		return
	}
	p.Blank()
	if unsubscribeOnly {
		p.Line("Unsubscribe and preferences destinations:")
	} else {
		p.Line("Link destinations:")
	}
	if len(selected) == 0 {
		p.Line("  No matching destinations found in the available source.")
	}
	for i, link := range selected {
		label := strings.TrimSpace(link.Text)
		if label == "" {
			label = link.Kind
		}
		provenance := link.Source
		if link.MIMEPart != "" {
			provenance += " part " + link.MIMEPart
			if !link.Selected {
				provenance += ", alternative"
			}
		}
		if !link.Openable {
			provenance += ", unsupported destination"
		}
		p.Line("  %d. %s [%s]", i+1, linkDisplay(label), linkDisplay(provenance))
		p.Line("     %s", linkDisplay(link.URL))
	}
}

func renderLinkStatus(p *output.Printer, status *mail.LinkExtractionStatus, unsubscribe *mail.MessageUnsubscribe) {
	if status != nil {
		if !status.Complete {
			p.Line("Link extraction is incomplete; additional destinations may be unavailable.")
		}
		for _, warning := range status.Warnings {
			p.Line("Links: %s", linkDisplay(warning))
		}
	}
	if unsubscribe != nil && unsubscribe.OneClickAdvertised {
		p.Line("One-click unsubscribe is advertised by the headers; eligibility is unverified. No action performed.")
	}
}
