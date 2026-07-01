package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

type batchItem struct {
	ID            string `json:"id"`
	Account       string `json:"account"`
	SourceMailbox string `json:"sourceMailbox"`
	TargetMailbox string `json:"targetMailbox,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

type batchResult struct {
	Action    string      `json:"action"`
	DryRun    bool        `json:"dryRun"`
	Matched   int         `json:"matched"`
	Attempted int         `json:"attempted"`
	Succeeded int         `json:"succeeded"`
	Failed    int         `json:"failed"`
	Skipped   int         `json:"skipped"`
	Items     []batchItem `json:"items"`
}

var (
	batchQuery   string
	batchStdin   bool
	batchDryRun  bool
	batchYes     bool
	batchRead    bool
	batchFlagged bool
)

var messagesBatchCmd = &cobra.Command{
	Use:   "batch",
	Short: "Apply safe bulk operations to messages",
	Long:  "Apply safe bulk operations to selected messages. Select messages with stdin IDs, positional IDs, or --query.",
}

var messagesBatchArchiveCmd = &cobra.Command{
	Use:   "archive [message-id...]",
	Short: "Archive selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMessageBatch("archive", args, "", func(client *mail.Client, item batchItem) error {
			return client.ArchiveMessage(item.Account, item.SourceMailbox, item.ID)
		})
	},
}

var messagesBatchDeleteCmd = &cobra.Command{
	Use:   "delete [message-id...]",
	Short: "Delete selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMessageBatch("delete", args, "", func(client *mail.Client, item batchItem) error {
			return client.DeleteMessageResolved(item.Account, item.SourceMailbox, item.ID)
		})
	},
}

var messagesBatchMoveCmd = &cobra.Command{
	Use:   "move [target-mailbox] [message-id...]",
	Short: "Move selected messages",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targetMailbox := args[0]
		ids := args[1:]
		return runMessageBatch("move", ids, targetMailbox, func(client *mail.Client, item batchItem) error {
			return client.MoveMessage(item.Account, item.SourceMailbox, item.ID, item.TargetMailbox)
		})
	},
}

var messagesBatchMarkCmd = &cobra.Command{
	Use:   "mark [message-id...]",
	Short: "Mark selected messages read or unread",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMessageBatch("mark", args, "", func(client *mail.Client, item batchItem) error {
			return client.MarkMessageAsRead(item.Account, item.SourceMailbox, item.ID, batchRead)
		})
	},
}

var messagesBatchFlagCmd = &cobra.Command{
	Use:   "flag [message-id...]",
	Short: "Flag or unflag selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMessageBatch("flag", args, "", func(client *mail.Client, item batchItem) error {
			return client.FlagMessage(item.Account, item.SourceMailbox, item.ID, batchFlagged)
		})
	},
}

func runMessageBatch(action string, argIDs []string, targetMailbox string, mutate func(*mail.Client, batchItem) error) error {
	if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
		return err
	}
	if !batchDryRun && requiresBatchConfirmation(action) && !batchYes {
		return fmt.Errorf("%s requires --yes unless --dry-run is set", action)
	}

	ids, err := resolveBatchIDs(argIDs)
	if err != nil {
		return err
	}

	client := mail.NewClient()
	items := make([]batchItem, 0, len(ids))
	if batchQuery != "" {
		messages, err := client.SearchMessagesJSON(batchQuery, msgAccount, msgMailbox, msgLimit)
		if err != nil {
			return fmt.Errorf("failed to resolve --query: %w", err)
		}
		for _, message := range messages {
			items = append(items, batchItem{
				ID:            message.ID,
				Account:       message.Account,
				SourceMailbox: message.Mailbox,
				TargetMailbox: targetMailbox,
			})
		}
	} else {
		for _, id := range ids {
			items = append(items, batchItem{
				ID:            id,
				Account:       msgAccount,
				SourceMailbox: msgMailbox,
				TargetMailbox: targetMailbox,
			})
		}
	}
	if len(items) == 0 {
		return fmt.Errorf("no messages selected")
	}

	result := batchResult{
		Action:  action,
		DryRun:  batchDryRun,
		Matched: len(items),
		Items:   make([]batchItem, 0, len(items)),
	}
	if batchDryRun {
		for _, item := range items {
			item.Status = "dry-run"
			result.Skipped++
			result.Items = append(result.Items, item)
		}
		return printJSON(result, "batch result")
	}

	for _, item := range items {
		result.Attempted++
		err := mutate(client, item)
		if err != nil {
			item.Status = "failed"
			item.Error = err.Error()
			result.Failed++
		} else {
			item.Status = "succeeded"
			result.Succeeded++
		}
		result.Items = append(result.Items, item)
	}

	invalidateMailboxCache(msgAccount, msgMailbox)
	if targetMailbox != "" {
		invalidateMailboxCache(msgAccount, targetMailbox)
	}
	if action == "archive" {
		invalidateMailboxCache(msgAccount, "Archive")
		invalidateMailboxCache(msgAccount, "All Mail")
	}
	if action == "delete" {
		invalidateMailboxCache(msgAccount, "Archive")
		invalidateMailboxCache(msgAccount, "All Mail")
	}

	if err := printJSON(result, "batch result"); err != nil {
		return err
	}
	if result.Failed > 0 {
		return fmt.Errorf("%s failed for %d of %d message(s)", action, result.Failed, result.Attempted)
	}
	return nil
}

func resolveBatchIDs(argIDs []string) ([]string, error) {
	if batchQuery != "" {
		if len(argIDs) > 0 || batchStdin {
			return nil, fmt.Errorf("--query cannot be combined with message IDs or --stdin")
		}
		return nil, nil
	}

	ids := append([]string(nil), argIDs...)
	if batchStdin {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			id := strings.TrimSpace(scanner.Text())
			if id != "" {
				ids = append(ids, id)
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("provide message IDs, --stdin, or --query")
	}
	return uniqueStrings(ids), nil
}

func requiresBatchConfirmation(action string) bool {
	return action == "archive" || action == "delete" || action == "move"
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func init() {
	messagesBatchCmd.AddCommand(messagesBatchArchiveCmd)
	messagesBatchCmd.AddCommand(messagesBatchDeleteCmd)
	messagesBatchCmd.AddCommand(messagesBatchMoveCmd)
	messagesBatchCmd.AddCommand(messagesBatchMarkCmd)
	messagesBatchCmd.AddCommand(messagesBatchFlagCmd)

	for _, cmd := range []*cobra.Command{
		messagesBatchArchiveCmd,
		messagesBatchDeleteCmd,
		messagesBatchMoveCmd,
		messagesBatchMarkCmd,
		messagesBatchFlagCmd,
	} {
		cmd.Flags().StringVar(&batchQuery, "query", "", "Search query used to select messages")
		cmd.Flags().BoolVar(&batchStdin, "stdin", false, "Read message IDs from stdin, one per line")
		cmd.Flags().BoolVar(&batchDryRun, "dry-run", false, "Print selected messages without mutating Mail.app")
		cmd.Flags().BoolVar(&batchYes, "yes", false, "Confirm destructive bulk mutation")
		cmd.Flags().IntVarP(&msgLimit, "limit", "l", 100, "Maximum query-selected messages")
	}
	messagesBatchMarkCmd.Flags().BoolVar(&batchRead, "read", true, "Mark messages as read; use --read=false for unread")
	messagesBatchFlagCmd.Flags().BoolVar(&batchFlagged, "flagged", true, "Flag messages; use --flagged=false to unflag")
}
