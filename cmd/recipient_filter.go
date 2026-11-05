package cmd

import (
	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

const recipientFilterUsage = "Only messages with this exact email in parsed To, Cc, or Bcc (live; implies --with-recipients)"

func normalizeRecipientFilter(value string, commands ...*cobra.Command) (string, error) {
	recipient, err := mail.NormalizeRecipientFilter(value)
	if err != nil {
		return "", clierr.Usage(err.Error())
	}
	if recipient == "" && len(commands) > 0 && commands[0].Flags().Changed("recipient") {
		return "", clierr.Usage("--recipient requires one email address")
	}
	return recipient, nil
}

func (a *application) writeRecipientListing(result mail.SearchResult, summary string, meta map[string]any, unified bool) error {
	meta["source"] = "live"
	meta["complete"] = result.Complete
	meta["failedMailboxes"] = result.FailedMailboxes
	var failure *clierr.Error
	if !result.Complete {
		failure = clierr.New(clierr.CodePartial, "recipient filtering incomplete; inspect failedMailboxes and retry")
	}
	return a.writer.Write(output.Result{Data: result.Messages, Summary: summary, Meta: meta, Err: failure, Plain: renderRecipientQuery(result, unified)})
}

func renderRecipientQuery(result mail.SearchResult, unified bool) func(*output.Printer) {
	return func(p *output.Printer) {
		renderMessages(result.Messages, unified, true)(p)
		for _, failure := range result.FailedMailboxes {
			p.Line("%s/%s: %s", linkDisplay(failure.Account), linkDisplay(failure.Mailbox), linkDisplay(failure.Error))
		}
	}
}
