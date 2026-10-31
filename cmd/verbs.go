package cmd

import (
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// Top-level verbs: the hot path for a person at the terminal and the
// simplest surface for an agent. Every one takes IDs positionally and
// resolves their mailbox through the Envelope Index unless --mailbox says
// otherwise.

// listUnified lists an inbox-style view: one account when in scope,
// otherwise all enabled accounts merged.
func (a *application) listUnified(kind string, limit, offset int, withContent bool) error {
	var messages []mail.Message
	var err error
	meta := map[string]any{"view": kind}
	scoped := unifiedListingScoped(a.resolved)
	if scoped {
		account, accountErr := a.requireAccount()
		if accountErr != nil {
			return accountErr
		}
		mailbox, mailboxErr := a.unifiedMailboxForAccount(account, kind)
		if mailboxErr != nil {
			return mailboxErr
		}
		unreadOnly, flaggedOnly := scopedUnifiedFilters(kind)
		messages, err = a.mailClient.GetMessagesJSON(account, mailbox, limit, offset, unreadOnly, flaggedOnly, withContent, "")
		meta["account"] = account
		meta["mailbox"] = mailbox
	} else {
		messages, err = a.mailClient.GetUnifiedMessagesJSON(kind, limit, offset, withContent)
		meta["account"] = "all"
	}
	if err != nil {
		return fmt.Errorf("list %s: %w", kind, err)
	}
	return a.writer.Write(output.Result{
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
func (a *application) unifiedMailboxForAccount(account, kind string) (string, error) {
	if a.resolved.Mailbox.Source != config.SourceDefault {
		return a.mailboxInScope(), nil
	}
	if _, special := specialMailboxCandidates(kind); !special {
		return "INBOX", nil
	}
	mailboxes, err := a.mailClient.GetMailboxesJSON(account)
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

func (a *application) showMessage(id string, metadataOnly, allLinks bool) error {
	refs, notices, err := a.locateMessages([]string{id})
	if err != nil {
		return err
	}
	ref := refs[0]
	var message *mail.Message
	if metadataOnly && ref.Envelope != nil {
		message = ref.Envelope
	} else {
		message, err = a.mailClient.GetMessageDetailsJSON(ref.Account, ref.Mailbox, ref.ID)
		if err != nil {
			return fmt.Errorf("get message: %w", err)
		}
		if message == nil {
			return clierr.New(clierr.CodeNotFound, fmt.Sprintf("message not found: %s in %s/%s", ref.ID, ref.Account, ref.Mailbox)).
				WithHint("the Envelope Index may be ahead of Mail.app; try again or pass --mailbox")
		}
		if metadataOnly {
			message.Content = ""
			message.Links, message.LinkStatus, message.Unsubscribe = nil, nil, nil
		}
	}
	_ = mail.RecordRecentMessage(*message, "show")
	return a.writer.Write(output.Result{
		Data:    message,
		Summary: fmt.Sprintf("%s from %s", output.Truncate(message.Subject, 60), displaySender(message.Sender)),
		Notices: notices,
		Meta:    map[string]any{"account": message.Account, "mailbox": message.Mailbox, "metadataOnly": metadataOnly},
		Plain:   renderMessageWithLinks(message, metadataOnly, allLinks),
	})
}

func (a *application) newIDVerb(use, short, action string, build func() (batchOptions, mail.Mutator)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " <message-id>...",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, mutate := build()
			opts.Action = action
			opts.DryRun = a.verbDryRun
			opts.Verify = a.verbVerify
			opts.Settle, opts.VerifyBudget = a.verbSettle, a.verbVerifyBudget
			opts.Journal = true
			if err := mail.ValidateBatchOptions(opts); err != nil {
				return clierr.Usage(err.Error())
			}
			return a.mutateByIDs(args, opts, mutate)
		},
	}
	cmd.Flags().BoolVar(&a.verbDryRun, "dry-run", false, "Report what would change without touching Mail.app")
	cmd.Flags().BoolVar(&a.verbVerify, "verify", false, "Re-read each message after mutation and record the outcome")
	cmd.Flags().DurationVar(&a.verbSettle, "settle", 0, "Verification settling window (0 uses the default)")
	cmd.Flags().DurationVar(&a.verbVerifyBudget, "verify-budget", mail.DefaultVerifyBudget, "Overall verification and identity capture budget per stage")
	return cmd
}

// gmailLabelHelpNote points Gmail users from mailbox commands to the labels
// adapter. Mail.app automation cannot remove a single Gmail label.
const gmailLabelHelpNote = "On Gmail this changes mailbox membership only. To remove Important or a user label from a message that stays where it is, use 'labels remove <id> --label <name>'."

// addGmailLabelHelp appends gmailLabelHelpNote to Long, built from Short when
// absent, and to the agent notes when the command has them. Help text only.
func addGmailLabelHelp(cmd *cobra.Command) {
	if cmd.Long == "" {
		cmd.Long = cmd.Short + "."
	}
	cmd.Long += "\n\n" + gmailLabelHelpNote
	if notes, ok := cmd.Annotations[annotationAgentNotes]; ok {
		cmd.Annotations[annotationAgentNotes] = notes + " " + gmailLabelHelpNote
	}
}

const bodySourceUsage = `Where the body comes from: "disk" reads Mail.app's message file and asks Mail.app only when there is none, "mail" always asks Mail.app to render it`

func (a *application) buildVerbsCommands() {
	var (
		verbUnread           bool
		verbLimit            int
		verbOffset           int
		verbWithContent      bool
		verbBodySource       string
		verbBodyAlternatives bool
		verbMetadataOnly     bool
		verbLinks            bool
		verbMoveTo           string
	)

	a.inboxCmd = &cobra.Command{
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
			return a.listUnified(kind, verbLimit, verbOffset, verbWithContent)
		},
	}
	a.unreadCmd = &cobra.Command{
		Use:   "unread",
		Short: "List unread inbox messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.listUnified("unread", verbLimit, verbOffset, verbWithContent)
		},
	}
	a.showCmd = &cobra.Command{
		Use:   "show <message-id>",
		Short: "Show a message with its body",
		Args:  cobra.ExactArgs(1),
		Annotations: map[string]string{
			annotationAgentNotes: "The mailbox is found through the Envelope Index; pass --account and --mailbox only when you know better. The body is read from Mail.app's message file and contentSource says where it came from. JSON includes links, linkStatus, and unsubscribe metadata. --links prints every destination in the human view. --metadata-only skips the body. Use links for discovery without Mail body rendering. Reading links never opens URLs or unsubscribes.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.mailClient.SetBodySource(verbBodySource); err != nil {
				return clierr.Usage(`--body-source must be "disk" or "mail"`)
			}
			a.mailClient.SetBodyAlternatives(verbBodyAlternatives)
			if verbMetadataOnly && verbLinks {
				return clierr.Usage("--links cannot be combined with --metadata-only")
			}
			return a.showMessage(args[0], verbMetadataOnly, verbLinks)
		},
	}
	a.linksCmd = a.newLinksCmd()
	a.seenCmd = a.newIDVerb("seen", "Mark messages read", "mark", func() (batchOptions, mail.Mutator) {
		return batchOptions{Read: true}, mail.MarkMutator(true)
	})
	a.unseenCmd = a.newIDVerb("unseen", "Mark messages unread", "mark", func() (batchOptions, mail.Mutator) {
		return batchOptions{Read: false}, mail.MarkMutator(false)
	})
	a.flagCmd = a.newIDVerb("flag", "Flag messages", "flag", func() (batchOptions, mail.Mutator) {
		return batchOptions{Flagged: true}, mail.FlagMutator(true)
	})
	a.unflagCmd = a.newIDVerb("unflag", "Unflag messages", "flag", func() (batchOptions, mail.Mutator) {
		return batchOptions{Flagged: false}, mail.FlagMutator(false)
	})
	a.archiveCmd = a.newIDVerb("archive", "Archive messages (All Mail on Gmail, Archive elsewhere)", "archive", func() (batchOptions, mail.Mutator) {
		return batchOptions{}, mail.ArchiveMutator(true)
	})
	a.deleteCmd = a.newIDVerb("delete", "Move messages to the trash", "delete", func() (batchOptions, mail.Mutator) {
		return batchOptions{}, mail.DeleteMutator
	})
	a.moveCmd = a.newIDVerb("move", "Move messages to another mailbox", "move", func() (batchOptions, mail.Mutator) {
		return batchOptions{TargetMailbox: verbMoveTo}, mail.MoveMutator(true)
	})

	for _, cmd := range []*cobra.Command{a.inboxCmd, a.unreadCmd} {
		registerPagingFlags(cmd, &verbLimit, &verbOffset)
		cmd.Flags().BoolVar(&verbWithContent, "with-content", false, "Include bodies (slow: one Mail.app call per ten messages)")
	}
	a.inboxCmd.Flags().BoolVarP(&verbUnread, "unread", "u", false, "Only unread messages")
	a.showCmd.Flags().StringVar(&verbBodySource, "body-source", mail.BodySourceDisk, bodySourceUsage)
	a.showCmd.Flags().BoolVar(&verbBodyAlternatives, "body-alternatives", false, "Include extracted text from MIME alternatives alongside body diagnostics")
	a.showCmd.Flags().BoolVar(&verbMetadataOnly, "metadata-only", false, "Skip the Mail.app body fetch; content and recipients are empty")
	a.showCmd.Flags().BoolVar(&verbLinks, "links", false, "Print all extracted link labels and full destinations in the human view")

	a.moveCmd.Flags().StringVar(&verbMoveTo, "to", "", "Target mailbox (required)")
	_ = a.moveCmd.MarkFlagRequired("to")
	a.archiveCmd.Annotations = map[string]string{
		annotationAgentNotes: "Archiving a Gmail INBOX message moves it to All Mail. Archiving something already in All Mail changes nothing and is reported as skipped (\"already in All Mail\") with exit 0. Mail.app assigns a moved message a new ID; find it again with search or recent.",
	}
	a.deleteCmd.Annotations = map[string]string{
		annotationAgentNotes: "delete moves to Trash through Mail.app; it does not purge. Preview with --dry-run. The message gets a new ID in Trash.",
	}
	a.moveCmd.Annotations = map[string]string{
		annotationAgentNotes: "--to takes a mailbox name in the same account. Use 'mailboxes list' to see names. Mail.app assigns the moved message a new ID in the target mailbox.",
	}
	addGmailLabelHelp(a.archiveCmd)
	addGmailLabelHelp(a.moveCmd)
	a.seenCmd.Annotations = map[string]string{annotationAgentNotes: "Idempotent. Multiple IDs are applied one Mail.app call at a time."}
	a.unseenCmd.Annotations = a.seenCmd.Annotations
}
