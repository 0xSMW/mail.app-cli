package cmd

import (
	"strconv"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

type threadedOutgoingFlags struct {
	replyTo   string
	replyAll  bool
	forwardOf string
}

type threadedSource struct {
	Account string         `json:"account"`
	Mailbox string         `json:"mailbox"`
	ID      string         `json:"id"`
	Mode    mail.ReplyMode `json:"mode"`
}

func (f *threadedOutgoingFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.replyTo, "reply-to", "", "Reply to this message ID in the explicit --mailbox (native backend)")
	cmd.Flags().BoolVar(&f.replyAll, "reply-all", false, "Use Mail's reply-all command with --reply-to")
	cmd.Flags().StringVar(&f.forwardOf, "forward-of", "", "Forward this message ID in the explicit --mailbox (native backend)")
}

func (f threadedOutgoingFlags) selection(cmd *cobra.Command) (*threadedSource, error) {
	reply := cmd.Flags().Changed("reply-to")
	forward := cmd.Flags().Changed("forward-of")
	if !reply && !forward && !f.replyAll {
		return nil, nil
	}
	if reply && forward {
		return nil, clierr.Usage("--reply-to and --forward-of are mutually exclusive")
	}
	if f.replyAll && !reply {
		return nil, clierr.Usage("--reply-all requires --reply-to")
	}
	mode, id := mail.ReplyToSender, f.replyTo
	if f.replyAll {
		mode = mail.ReplyToAll
	}
	if forward {
		mode, id = mail.ForwardMessage, f.forwardOf
	}
	number, err := strconv.ParseInt(id, 10, 64)
	if err != nil || number <= 0 {
		return nil, clierr.Usage("--reply-to and --forward-of require a positive message ID")
	}
	if !cmd.Flags().Changed("mailbox") || strings.TrimSpace(resolved.Mailbox.Value) == "" {
		return nil, clierr.Usage("reply and forward require an explicit --mailbox for the original message")
	}
	if !cmd.Flags().Changed("body") && !cmd.Flags().Changed("body-file") {
		return nil, clierr.Usage("reply and forward require --body or --body-file with the complete outgoing body")
	}
	if mailClient.Backend() != mail.BackendNative {
		return nil, clierr.Usage("--reply-to and --forward-of require the native backend; use --backend native")
	}
	return &threadedSource{Mailbox: resolved.Mailbox.Value, ID: id, Mode: mode}, nil
}

func (s threadedSource) input(outgoing mail.OutgoingInput) mail.ThreadedOutgoingInput {
	return mail.ThreadedOutgoingInput{OutgoingInput: outgoing, OriginalAccount: s.Account, OriginalMailbox: s.Mailbox, OriginalID: s.ID, Mode: s.Mode}
}

func threadedPreviewNotices(source *threadedSource, dryRun bool) []string {
	notices := previewNotices(dryRun)
	if source != nil && dryRun {
		notices = append(notices, "Original message lookup and Mail's reply or forward creation have not been tested.")
	}
	return notices
}
