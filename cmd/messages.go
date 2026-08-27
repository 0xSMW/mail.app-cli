package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/cache"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

const messageCacheTTL = 5 * time.Minute

var (
	msgLimit         int
	msgOffset        int
	msgUnread        bool
	msgFlaggedFilter bool
	msgWithContent   bool
	msgRead          bool
	msgFlaggedSet    bool
	msgSince         string
	msgNoCache       bool
	msgForceRefresh  bool
	msgDryRun        bool
	msgVerify        bool
)

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

var messagesCmd = &cobra.Command{
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

var messagesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List messages in the mailbox in scope",
	Annotations: map[string]string{
		annotationAgentNotes: "Results are cached 5 minutes per query; pass --no-cache after a mutation from another tool. --with-content is slow; prefer show for one body.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		account, err := requireAccount()
		if err != nil {
			return err
		}
		mailbox := mailboxInScope()
		var c *cache.Cache
		var cacheErr error
		if !msgNoCache {
			c, cacheErr = cache.New()
		}
		cacheKey := fmt.Sprintf("msgs-%s-%s-%d-%d-%v-%v-%s-%v",
			sanitizeCacheKey(account), sanitizeCacheKey(mailbox),
			msgLimit, msgOffset, msgUnread, msgFlaggedFilter, sanitizeCacheKey(msgSince), msgWithContent)

		var messages []mail.Message
		source := "live"
		if !msgNoCache && !msgForceRefresh && !msgWithContent && cacheErr == nil {
			c.SetTTL(messageCacheTTL)
			if found, err := c.Get(cacheKey, &messages); err == nil && found {
				source = "cache"
			}
		}
		if source == "live" {
			messages, err = mailClient.GetMessagesJSON(account, mailbox, msgLimit, msgOffset, msgUnread, msgFlaggedFilter, msgWithContent, msgSince)
			if err != nil {
				return fmt.Errorf("get messages: %w", err)
			}
			if !msgNoCache && !msgWithContent && cacheErr == nil {
				c.SetTTL(messageCacheTTL)
				_ = c.Set(cacheKey, messages)
			}
		}
		return writer.Write(output.Result{
			Data:    messages,
			Summary: fmt.Sprintf("%s in %s/%s", plural(len(messages), "message"), account, mailbox),
			Meta:    map[string]any{"account": account, "mailbox": mailbox, "source": source},
			Plain:   renderMessages(messages, false),
		})
	},
}

var messagesShowCmd = &cobra.Command{
	Use:   "show <message-id>",
	Short: "Show message details (same as top-level show)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return showMessage(args[0], false)
	},
}

var messagesMarkCmd = &cobra.Command{
	Use:   "mark <message-id>",
	Short: "Mark a message read (default) or unread with --read=false",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateByIDs(args, batchOptions{Action: "mark", Read: msgRead, DryRun: msgDryRun, Verify: msgVerify, Journal: true}, markMutator(msgRead))
	},
}

var messagesFlagCmd = &cobra.Command{
	Use:   "flag <message-id>",
	Short: "Flag a message (default) or unflag with --flagged=false",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateByIDs(args, batchOptions{Action: "flag", Flagged: msgFlaggedSet, DryRun: msgDryRun, Verify: msgVerify, Journal: true}, flagMutator(msgFlaggedSet))
	},
}

var messagesDeleteCmd = &cobra.Command{
	Use:   "delete <message-id>",
	Short: "Move a message to the trash",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateByIDs(args, batchOptions{Action: "delete", DryRun: msgDryRun, Verify: msgVerify, Journal: true}, deleteMutator)
	},
}

var messagesArchiveCmd = &cobra.Command{
	Use:   "archive <message-id>",
	Short: "Archive a message",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateByIDs(args, batchOptions{Action: "archive", DryRun: msgDryRun, Verify: msgVerify, Journal: true}, archiveMutator(true))
	},
}

var messagesMoveCmd = &cobra.Command{
	Use:   "move <message-id> <target-mailbox>",
	Short: "Move a message to another mailbox",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateByIDs(args[:1], batchOptions{Action: "move", TargetMailbox: args[1], DryRun: msgDryRun, Verify: msgVerify, Journal: true}, moveMutator(true))
	},
}

// newUnifiedCmd returns a cobra.Command for a unified mailbox view.
func newUnifiedCmd(use, short, mailboxType string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listUnified(mailboxType, msgLimit, msgOffset, msgWithContent)
		},
	}
}

func scopedUnifiedFilters(mailboxType string) (bool, bool) {
	return mailboxType == "unread", mailboxType == "flagged"
}

var messagesInboxCmd = newUnifiedCmd("inbox", "List inbox messages across all accounts", "inbox")
var messagesUnreadCmd = newUnifiedCmd("unread", "List unread messages across all accounts", "unread")
var messagesSentCmd = newUnifiedCmd("sent", "List sent messages across all accounts", "sent")
var messagesDraftsCmd = newUnifiedCmd("drafts", "List draft messages across all accounts", "drafts")
var messagesFlaggedCmd = newUnifiedCmd("flagged", "List flagged messages across all accounts", "flagged")
var messagesTrashCmd = newUnifiedCmd("trash", "List trash messages across all accounts", "trash")
var messagesJunkCmd = newUnifiedCmd("junk", "List junk/spam messages across all accounts", "junk")

func init() {
	messagesCmd.AddCommand(
		messagesListCmd, messagesShowCmd, messagesMarkCmd, messagesFlagCmd,
		messagesDeleteCmd, messagesArchiveCmd, messagesMoveCmd,
		messagesInboxCmd, messagesUnreadCmd, messagesSentCmd, messagesDraftsCmd,
		messagesFlaggedCmd, messagesTrashCmd, messagesJunkCmd,
		messagesBatchCmd, messagesVIPCmd,
	)

	messagesListCmd.Flags().IntVarP(&msgLimit, "limit", "l", 25, "Maximum messages to return")
	messagesListCmd.Flags().IntVarP(&msgOffset, "offset", "o", 0, "Messages to skip (pagination)")
	messagesListCmd.Flags().BoolVarP(&msgUnread, "unread", "u", false, "Only unread messages")
	messagesListCmd.Flags().BoolVarP(&msgFlaggedFilter, "flagged", "f", false, "Only flagged messages")
	messagesListCmd.Flags().BoolVar(&msgWithContent, "with-content", false, "Include bodies (slow)")
	messagesListCmd.Flags().StringVarP(&msgSince, "since", "s", "", "Only messages since YYYY-MM-DD or 'YYYY-MM-DD HH:MM:SS'")
	messagesListCmd.Flags().BoolVar(&msgNoCache, "no-cache", false, "Bypass the cache and read live")
	messagesListCmd.Flags().BoolVar(&msgForceRefresh, "force-refresh", false, "Refresh the cache with a live read")

	messagesMarkCmd.Flags().BoolVarP(&msgRead, "read", "r", true, "Mark read (default) or --read=false for unread")
	messagesFlagCmd.Flags().BoolVarP(&msgFlaggedSet, "flagged", "f", true, "Flag (default) or --flagged=false to unflag")
	for _, cmd := range []*cobra.Command{messagesShowCmd, messagesMarkCmd, messagesFlagCmd, messagesDeleteCmd, messagesArchiveCmd, messagesMoveCmd} {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[annotationCompatibility] = "true"
	}
	for _, cmd := range []*cobra.Command{messagesMarkCmd, messagesFlagCmd, messagesDeleteCmd, messagesArchiveCmd, messagesMoveCmd} {
		cmd.Flags().BoolVar(&msgDryRun, "dry-run", false, "Report what would change without touching Mail.app")
		cmd.Flags().BoolVar(&msgVerify, "verify", false, "Re-read the message after mutation and record the outcome")
	}

	for _, cmd := range []*cobra.Command{
		messagesInboxCmd, messagesUnreadCmd, messagesSentCmd,
		messagesDraftsCmd, messagesFlaggedCmd, messagesTrashCmd, messagesJunkCmd,
	} {
		cmd.Flags().IntVarP(&msgLimit, "limit", "l", 25, "Maximum messages to return")
		cmd.Flags().IntVarP(&msgOffset, "offset", "o", 0, "Messages to skip (pagination)")
		cmd.Flags().BoolVar(&msgWithContent, "with-content", false, "Include bodies (slow)")
	}
}
