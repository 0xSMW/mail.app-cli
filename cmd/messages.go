package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/cache"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

const messageCacheTTL = 5 * time.Minute

// sanitizeCacheKey replaces non-alphanumeric chars so the key is safe as a filename component.
func sanitizeCacheKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// invalidateMailboxCache removes all message-list cache entries for the given mailbox.
func invalidateMailboxCache(account, mailbox string) {
	if c, err := cache.New(); err == nil {
		prefix := fmt.Sprintf("msgs-%s-%s-", sanitizeCacheKey(account), sanitizeCacheKey(mailbox))
		_ = c.DeletePrefix(prefix)
	}
}

// newUnifiedCmd returns a cobra.Command for a unified mailbox view.
func (a *application) newUnifiedCmd(use, short, mailboxType string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.listUnified(mailboxType, a.msgLimit, a.msgOffset, a.msgWithContent)
		},
	}
}

func scopedUnifiedFilters(mailboxType string) (bool, bool) {
	return mailboxType == "unread", mailboxType == "flagged"
}

func (a *application) buildMessagesCommands() {
	var (
		msgNoCache       bool
		msgUnread        bool
		msgFlaggedFilter bool
		msgSince         string
		msgForceRefresh  bool
		msgRead          bool
		msgDryRun        bool
		msgVerify        bool
		msgFlaggedSet    bool
	)

	a.messagesCmd = &cobra.Command{
		Use:   "messages",
		Short: "List and act on messages in one mailbox",
		Long: `List and act on messages. The account and mailbox come from --account
and --mailbox, the config file, or defaults (the only account, INBOX).

The single-message verbs here (show, mark, flag, archive, delete, move) are
the 1.x spelling; the top-level show, seen, unseen, flag, unflag, archive,
delete, and move commands do the same work and accept several IDs.`,
		Annotations: map[string]string{
			annotationAgentNotes: "'messages list' lists one mailbox with filters. The single-message verbs here are 1.x spellings; prefer the top-level show, seen, flag, archive, delete, and move.",
		},
	}
	a.messagesListCmd = &cobra.Command{
		Use:   "list",
		Short: "List messages in the mailbox in scope",
		Annotations: map[string]string{
			annotationAgentNotes: "Results are cached 5 minutes per query; pass --no-cache after a mutation from another tool. --with-content is slow; prefer show for one body.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			account, err := a.requireAccount()
			if err != nil {
				return err
			}
			mailbox := a.mailboxInScope()
			cacheKey := fmt.Sprintf("msgs-%s-%s-%d-%d-%v-%v-%s-%v",
				sanitizeCacheKey(account), sanitizeCacheKey(mailbox),
				a.msgLimit, a.msgOffset, msgUnread, msgFlaggedFilter, sanitizeCacheKey(msgSince), a.msgWithContent)
			messages, source, err := loadCached(cacheKey, msgNoCache, msgForceRefresh, a.msgWithContent, messageCacheTTL, func() ([]mail.Message, error) {
				return a.mailClient.GetMessagesJSON(account, mailbox, a.msgLimit, a.msgOffset, msgUnread, msgFlaggedFilter, a.msgWithContent, msgSince)
			})
			if err != nil {
				return fmt.Errorf("get messages: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    messages,
				Summary: fmt.Sprintf("%s in %s/%s", plural(len(messages), "message"), account, mailbox),
				Meta:    map[string]any{"account": account, "mailbox": mailbox, "source": source},
				Plain:   renderMessages(messages, false),
			})
		},
	}
	messagesShowCmd := &cobra.Command{
		Use:   "show <message-id>",
		Short: "Show message details (same as top-level show)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.showMessage(args[0], false, false)
		},
	}
	a.messagesMarkCmd = &cobra.Command{
		Use:   "mark <message-id>",
		Short: "Mark a message read (default) or unread with --read=false",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.mutateByIDs(args, batchOptions{Action: "mark", Read: msgRead, DryRun: msgDryRun, Verify: msgVerify, Journal: true}, mail.MarkMutator(msgRead))
		},
	}
	a.messagesFlagCmd = &cobra.Command{
		Use:   "flag <message-id>",
		Short: "Flag a message (default) or unflag with --flagged=false",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.mutateByIDs(args, batchOptions{Action: "flag", Flagged: msgFlaggedSet, DryRun: msgDryRun, Verify: msgVerify, Journal: true}, mail.FlagMutator(msgFlaggedSet))
		},
	}
	a.messagesDeleteCmd = &cobra.Command{
		Use:   "delete <message-id>",
		Short: "Move a message to the trash",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.mutateByIDs(args, batchOptions{Action: "delete", DryRun: msgDryRun, Verify: msgVerify, Journal: true}, mail.DeleteMutator)
		},
	}
	a.messagesArchiveCmd = &cobra.Command{
		Use:   "archive <message-id>",
		Short: "Archive a message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.mutateByIDs(args, batchOptions{Action: "archive", DryRun: msgDryRun, Verify: msgVerify, Journal: true}, mail.ArchiveMutator(true))
		},
	}
	a.messagesMoveCmd = &cobra.Command{
		Use:   "move <message-id> <target-mailbox>",
		Short: "Move a message to another mailbox",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.mutateByIDs(args[:1], batchOptions{Action: "move", TargetMailbox: args[1], DryRun: msgDryRun, Verify: msgVerify, Journal: true}, mail.MoveMutator(true))
		},
	}
	a.messagesInboxCmd = a.newUnifiedCmd("inbox", "List inbox messages across all accounts", "inbox")
	a.messagesUnreadCmd = a.newUnifiedCmd("unread", "List unread messages across all accounts", "unread")
	a.messagesSentCmd = a.newUnifiedCmd("sent", "List sent messages across all accounts", "sent")
	a.messagesDraftsCmd = a.newUnifiedCmd("drafts", "List draft messages across all accounts", "drafts")
	a.messagesFlaggedCmd = a.newUnifiedCmd("flagged", "List flagged messages across all accounts", "flagged")
	a.messagesTrashCmd = a.newUnifiedCmd("trash", "List trash messages across all accounts", "trash")
	a.messagesJunkCmd = a.newUnifiedCmd("junk", "List junk/spam messages across all accounts", "junk")

	a.messagesCmd.AddCommand(
		a.messagesListCmd, messagesShowCmd, a.messagesMarkCmd, a.messagesFlagCmd,
		a.messagesDeleteCmd, a.messagesArchiveCmd, a.messagesMoveCmd,
		a.messagesInboxCmd, a.messagesUnreadCmd, a.messagesSentCmd, a.messagesDraftsCmd,
		a.messagesFlaggedCmd, a.messagesTrashCmd, a.messagesJunkCmd,
		a.messagesBatchCmd, a.messagesVIPCmd,
	)

	registerPagingFlags(a.messagesListCmd, &a.msgLimit, &a.msgOffset)
	a.messagesListCmd.Flags().BoolVarP(&msgUnread, "unread", "u", false, "Only unread messages")
	a.messagesListCmd.Flags().BoolVarP(&msgFlaggedFilter, "flagged", "f", false, "Only flagged messages")
	a.messagesListCmd.Flags().BoolVar(&a.msgWithContent, "with-content", false, "Include bodies (slow)")
	a.messagesListCmd.Flags().StringVarP(&msgSince, "since", "s", "", "Only messages since YYYY-MM-DD or 'YYYY-MM-DD HH:MM:SS'")
	a.messagesListCmd.Flags().BoolVar(&msgNoCache, "no-cache", false, "Bypass the cache and read live")
	a.messagesListCmd.Flags().BoolVar(&msgForceRefresh, "force-refresh", false, "Refresh the cache with a live read")

	a.messagesMarkCmd.Flags().BoolVarP(&msgRead, "read", "r", true, "Mark read (default) or --read=false for unread")
	a.messagesFlagCmd.Flags().BoolVarP(&msgFlaggedSet, "flagged", "f", true, "Flag (default) or --flagged=false to unflag")
	for _, cmd := range []*cobra.Command{messagesShowCmd, a.messagesMarkCmd, a.messagesFlagCmd, a.messagesDeleteCmd, a.messagesArchiveCmd, a.messagesMoveCmd} {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[annotationCompatibility] = "true"
	}
	for _, cmd := range []*cobra.Command{a.messagesMarkCmd, a.messagesFlagCmd, a.messagesDeleteCmd, a.messagesArchiveCmd, a.messagesMoveCmd} {
		cmd.Flags().BoolVar(&msgDryRun, "dry-run", false, "Report what would change without touching Mail.app")
		cmd.Flags().BoolVar(&msgVerify, "verify", false, "Re-read the message after mutation and record the outcome")
	}

	for _, cmd := range []*cobra.Command{
		a.messagesInboxCmd, a.messagesUnreadCmd, a.messagesSentCmd,
		a.messagesDraftsCmd, a.messagesFlaggedCmd, a.messagesTrashCmd, a.messagesJunkCmd,
	} {
		registerPagingFlags(cmd, &a.msgLimit, &a.msgOffset)
		cmd.Flags().BoolVar(&a.msgWithContent, "with-content", false, "Include bodies (slow)")
	}
}
