package cmd

import (
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/config"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

// Top-level verbs: the hot path for a person at the terminal and the
// simplest surface for an agent. Every one takes IDs positionally and
// resolves their mailbox through the Envelope Index unless --mailbox says
// otherwise.

var (
	verbDryRun       bool
	verbVerify       bool
	verbMoveTo       string
	verbLimit        int
	verbOffset       int
	verbUnread       bool
	verbWithContent  bool
	verbMetadataOnly bool
)

var inboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "List INBOX across accounts, one account with --account, or another mailbox with --mailbox",
	Annotations: map[string]string{
		annotationAgentNotes: "Newest first. Without --account this merges every enabled account's INBOX. IDs feed show, seen, flag, archive, delete, and move.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		kind := "inbox"
		if verbUnread {
			kind = "unread"
		}
		return listUnified(kind, verbLimit, verbOffset, verbWithContent)
	},
}

var unreadCmd = &cobra.Command{
	Use:   "unread",
	Short: "List unread inbox messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return listUnified("unread", verbLimit, verbOffset, verbWithContent)
	},
}

// listUnified lists an inbox-style view: one account when in scope,
// otherwise all enabled accounts merged.
func listUnified(kind string, limit, offset int, withContent bool) error {
	var messages []mail.Message
	var err error
	meta := map[string]any{"view": kind}
	scoped := unifiedListingScoped(resolved)
	if scoped {
		account, accountErr := requireAccount()
		if accountErr != nil {
			return accountErr
		}
		mailbox, mailboxErr := unifiedMailboxForAccount(account, kind)
		if mailboxErr != nil {
			return mailboxErr
		}
		unreadOnly, flaggedOnly := scopedUnifiedFilters(kind)
		messages, err = mailClient.GetMessagesJSON(account, mailbox, limit, offset, unreadOnly, flaggedOnly, withContent, "")
		meta["account"] = account
		meta["mailbox"] = mailbox
	} else {
		messages, err = mailClient.GetUnifiedMessagesJSON(kind, limit, offset, withContent)
		meta["account"] = "all"
	}
	if err != nil {
		return fmt.Errorf("list %s: %w", kind, err)
	}
	return writer.Write(output.Result{
		Data:    messages,
		Summary: fmt.Sprintf("%s in %s", plural(len(messages), "message"), kind),
		Meta:    meta,
		Plain:   renderMessages(messages, !scoped),
	})
}

// unifiedListingScoped reports whether a unified-style listing should be
// limited to one account and mailbox. A configured mailbox is a scope even
// when it came from the environment or config file rather than --mailbox.
func unifiedListingScoped(scope config.Resolved) bool {
	return scope.Account.Value != "" || scope.Mailbox.Source != config.SourceDefault
}

// unifiedMailboxForAccount respects a configured mailbox. When an account is
// the only scope, special views use that account's real special mailbox name
// (for example, "Sent Items") instead of falling back to INBOX.
func unifiedMailboxForAccount(account, kind string) (string, error) {
	if resolved.Mailbox.Source != config.SourceDefault {
		return mailboxInScope(), nil
	}
	if _, special := specialMailboxCandidates(kind); !special {
		return "INBOX", nil
	}
	mailboxes, err := mailClient.GetMailboxesJSON(account)
	if err != nil {
		return "", fmt.Errorf("list mailboxes for %s: %w", account, err)
	}
	mailbox, _ := specialMailboxForAccount(kind, account, mailboxes)
	return mailbox, nil
}

func specialMailboxForAccount(kind, account string, mailboxes []mail.Mailbox) (string, bool) {
	candidates, special := specialMailboxCandidates(kind)
	if !special {
		return "", false
	}
	for _, mailbox := range mailboxes {
		if mailbox.Account != "" && mailbox.Account != account {
			continue
		}
		for _, candidate := range candidates {
			if strings.EqualFold(strings.TrimSpace(mailbox.Name), candidate) {
				return mailbox.Name, true
			}
		}
	}
	return candidates[0], true
}

func specialMailboxCandidates(kind string) ([]string, bool) {
	switch kind {
	case "sent":
		return []string{"Sent", "Sent Messages", "Sent Mail", "Sent Items"}, true
	case "drafts":
		return []string{"Drafts", "Draft"}, true
	case "trash":
		return []string{"Trash", "Deleted Messages", "Deleted Items", "Bin"}, true
	case "junk":
		return []string{"Junk", "Spam", "Junk E-mail", "Junk Email", "CATEGORY_SPAM"}, true
	default:
		return nil, false
	}
}

var showCmd = &cobra.Command{
	Use:   "show <message-id>",
	Short: "Show a message with its body",
	Args:  cobra.ExactArgs(1),
	Annotations: map[string]string{
		annotationAgentNotes: "The mailbox is found through the Envelope Index; pass --account and --mailbox only when you know better. --metadata-only skips the slow Mail.app body fetch.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return showMessage(args[0], verbMetadataOnly)
	},
}

func showMessage(id string, metadataOnly bool) error {
	refs, notices, err := locateMessages([]string{id})
	if err != nil {
		return err
	}
	ref := refs[0]
	var message *mail.Message
	if metadataOnly && ref.Envelope != nil {
		message = ref.Envelope
	} else {
		message, err = mailClient.GetMessageDetailsJSON(ref.Account, ref.Mailbox, ref.ID)
		if err != nil {
			return fmt.Errorf("get message: %w", err)
		}
		if message == nil {
			return clierr.New(clierr.CodeNotFound, fmt.Sprintf("message not found: %s in %s/%s", ref.ID, ref.Account, ref.Mailbox)).
				WithHint("the Envelope Index may be ahead of Mail.app; try again or pass --mailbox")
		}
		if metadataOnly {
			message.Content = ""
		}
	}
	_ = mail.RecordRecentMessage(*message, "show")
	return writer.Write(output.Result{
		Data:    message,
		Summary: fmt.Sprintf("%s from %s", output.Truncate(message.Subject, 60), displaySender(message.Sender)),
		Notices: notices,
		Meta:    map[string]any{"account": message.Account, "mailbox": message.Mailbox, "metadataOnly": metadataOnly},
		Plain:   renderMessage(message, metadataOnly),
	})
}

func newIDVerb(use, short, action string, build func() (batchOptions, mutator)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " <message-id>...",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, mutate := build()
			opts.Action = action
			opts.DryRun = verbDryRun
			opts.Verify = verbVerify
			opts.Journal = true
			return mutateByIDs(args, opts, mutate)
		},
	}
	cmd.Flags().BoolVar(&verbDryRun, "dry-run", false, "Report what would change without touching Mail.app")
	cmd.Flags().BoolVar(&verbVerify, "verify", false, "Re-read each message after mutation and record the outcome")
	return cmd
}

var seenCmd = newIDVerb("seen", "Mark messages read", "mark", func() (batchOptions, mutator) {
	return batchOptions{Read: true}, markMutator(true)
})

var unseenCmd = newIDVerb("unseen", "Mark messages unread", "mark", func() (batchOptions, mutator) {
	return batchOptions{Read: false}, markMutator(false)
})

var flagCmd = newIDVerb("flag", "Flag messages", "flag", func() (batchOptions, mutator) {
	return batchOptions{Flagged: true}, flagMutator(true)
})

var unflagCmd = newIDVerb("unflag", "Unflag messages", "flag", func() (batchOptions, mutator) {
	return batchOptions{Flagged: false}, flagMutator(false)
})

var archiveCmd = newIDVerb("archive", "Archive messages (All Mail on Gmail, Archive elsewhere)", "archive", func() (batchOptions, mutator) {
	return batchOptions{}, archiveMutator(true)
})

var deleteCmd = newIDVerb("delete", "Move messages to the trash", "delete", func() (batchOptions, mutator) {
	return batchOptions{}, deleteMutator
})

var moveCmd = newIDVerb("move", "Move messages to another mailbox", "move", func() (batchOptions, mutator) {
	return batchOptions{TargetMailbox: verbMoveTo}, moveMutator(true)
})

func init() {
	for _, cmd := range []*cobra.Command{inboxCmd, unreadCmd} {
		cmd.Flags().IntVarP(&verbLimit, "limit", "l", 25, "Maximum messages to return")
		cmd.Flags().IntVarP(&verbOffset, "offset", "o", 0, "Messages to skip (pagination)")
		cmd.Flags().BoolVar(&verbWithContent, "with-content", false, "Include bodies (slow: one Mail.app call per ten messages)")
	}
	inboxCmd.Flags().BoolVarP(&verbUnread, "unread", "u", false, "Only unread messages")
	showCmd.Flags().BoolVar(&verbMetadataOnly, "metadata-only", false, "Skip the Mail.app body fetch; content and recipients are empty")

	moveCmd.Flags().StringVar(&verbMoveTo, "to", "", "Target mailbox (required)")
	_ = moveCmd.MarkFlagRequired("to")
	archiveCmd.Annotations = map[string]string{
		annotationAgentNotes: "Archiving a Gmail INBOX message moves it to All Mail. Archiving something already in All Mail is a no-op that still reports success. Mail.app assigns a moved message a new ID; find it again with search or recent.",
	}
	deleteCmd.Annotations = map[string]string{
		annotationAgentNotes: "delete moves to Trash through Mail.app; it does not purge. Preview with --dry-run. The message gets a new ID in Trash.",
	}
	moveCmd.Annotations = map[string]string{
		annotationAgentNotes: "--to takes a mailbox name in the same account. Use 'mailboxes list' to see names. Mail.app assigns the moved message a new ID in the target mailbox.",
	}
	seenCmd.Annotations = map[string]string{annotationAgentNotes: "Idempotent. Multiple IDs are applied one Mail.app call at a time."}
	unseenCmd.Annotations = seenCmd.Annotations
}
