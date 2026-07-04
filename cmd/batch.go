package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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
	MarkedRead    bool   `json:"markedRead,omitempty"`
	VerifyStatus  string `json:"verifyStatus,omitempty"`
	VerifyError   string `json:"verifyError,omitempty"`
}

type batchResult struct {
	Action    string      `json:"action"`
	DryRun    bool        `json:"dryRun"`
	StartedAt string      `json:"startedAt,omitempty"`
	EndedAt   string      `json:"endedAt,omitempty"`
	Matched   int         `json:"matched"`
	Attempted int         `json:"attempted"`
	Succeeded int         `json:"succeeded"`
	Failed    int         `json:"failed"`
	Skipped   int         `json:"skipped"`
	Chunks    int         `json:"chunks,omitempty"`
	Items     []batchItem `json:"items"`
}

var (
	batchQuery          string
	batchSender         string
	batchSenderDomain   string
	batchStdin          bool
	batchDryRun         bool
	batchYes            bool
	batchRead           bool
	batchFlagged        bool
	batchMarkReadBefore bool
	batchVerify         bool
	batchProgress       bool
	batchChunkSize      int
	batchLimit          int
	batchReportFile     string
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
	if batchQuery != "" || batchSender != "" || batchSenderDomain != "" {
		messages, err := resolveBatchMessages(client)
		if err != nil {
			return err
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
		Action:    action,
		DryRun:    batchDryRun,
		StartedAt: time.Now().Format(time.RFC3339),
		Matched:   len(items),
		Items:     make([]batchItem, 0, len(items)),
	}
	chunkSize := normalizedBatchChunkSize(len(items), batchChunkSize)
	result.Chunks = (len(items) + chunkSize - 1) / chunkSize
	if batchDryRun {
		for _, item := range items {
			item.Status = "dry-run"
			result.Skipped++
			result.Items = append(result.Items, item)
		}
		result.EndedAt = time.Now().Format(time.RFC3339)
		return writeBatchResult(result)
	}

	for start := 0; start < len(items); start += chunkSize {
		end := start + chunkSize
		if end > len(items) {
			end = len(items)
		}
		if batchProgress {
			fmt.Fprintf(os.Stderr, "batch %s: chunk %d/%d (%d messages)\n", action, (start/chunkSize)+1, result.Chunks, end-start)
		}
		for _, item := range items[start:end] {
			result.Attempted++
			if batchMarkReadBefore && action != "mark" {
				if err := client.MarkMessageAsRead(item.Account, item.SourceMailbox, item.ID, true); err != nil {
					item.Status = "failed"
					item.Error = fmt.Sprintf("mark-read before %s failed: %v", action, err)
					result.Failed++
					result.Items = append(result.Items, item)
					continue
				}
				item.MarkedRead = true
			}
			err := mutate(client, item)
			if err != nil {
				item.Status = "failed"
				item.Error = err.Error()
				result.Failed++
			} else {
				item.Status = "succeeded"
				if batchVerify {
					verifyStatus, verifyErr := verifyBatchMutation(client, action, item)
					item.VerifyStatus = verifyStatus
					if verifyErr != nil {
						item.Status = "failed"
						item.VerifyError = verifyErr.Error()
						result.Failed++
					} else {
						result.Succeeded++
					}
				} else {
					result.Succeeded++
				}
			}
			if batchProgress {
				fmt.Fprintf(os.Stderr, "batch %s: %d/%d %s %s\n", action, result.Attempted, len(items), item.ID, item.Status)
			}
			result.Items = append(result.Items, item)
		}
	}
	result.EndedAt = time.Now().Format(time.RFC3339)

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

	if err := writeBatchResult(result); err != nil {
		return err
	}
	if result.Failed > 0 {
		return fmt.Errorf("%s failed for %d of %d message(s)", action, result.Failed, result.Attempted)
	}
	return nil
}

func resolveBatchIDs(argIDs []string) ([]string, error) {
	if batchQuery != "" || batchSender != "" || batchSenderDomain != "" {
		if len(argIDs) > 0 || batchStdin {
			return nil, fmt.Errorf("--query, --sender, and --sender-domain cannot be combined with message IDs or --stdin")
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

func resolveBatchMessages(client *mail.Client) ([]mail.Message, error) {
	var messages []mail.Message
	var err error
	if batchQuery != "" {
		messages, err = client.SearchMessagesJSON(batchQuery, msgAccount, msgMailbox, batchLimit)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve --query: %w", err)
		}
	} else {
		messages, err = client.GetMessagesJSON(msgAccount, msgMailbox, batchLimit, 0, false, false, false, "")
		if err != nil {
			return nil, fmt.Errorf("failed to list messages for filtered batch: %w", err)
		}
	}
	return filterMessagesBySender(messages, batchSender, batchSenderDomain), nil
}

func normalizedBatchChunkSize(total, requested int) int {
	if total <= 0 {
		return 1
	}
	if requested <= 0 || requested > total {
		return total
	}
	return requested
}

func verifyBatchMutation(client *mail.Client, action string, item batchItem) (string, error) {
	message, err := client.GetMessageDetailsJSON(item.Account, item.SourceMailbox, item.ID)
	if action == "archive" || action == "delete" || action == "move" {
		if err != nil || message == nil {
			return "absent-from-source", nil
		}
		return "present-in-source", fmt.Errorf("message still present in source mailbox")
	}
	if err != nil {
		return "verification-failed", err
	}
	if action == "mark" {
		if message.Read == batchRead {
			return "matched", nil
		}
		return "mismatch", fmt.Errorf("read status mismatch")
	}
	if action == "flag" {
		if message.Flagged == batchFlagged {
			return "matched", nil
		}
		return "mismatch", fmt.Errorf("flagged status mismatch")
	}
	return "unchecked", nil
}

func writeBatchResult(result batchResult) error {
	if err := printJSON(result, "batch result"); err != nil {
		return err
	}
	if batchReportFile == "" {
		return nil
	}
	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal batch report: %w", err)
	}
	output = append(output, '\n')
	return os.WriteFile(batchReportFile, output, 0644)
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
		cmd.Flags().StringVar(&batchSender, "sender", "", "Only select messages from this exact sender/email")
		cmd.Flags().StringVar(&batchSenderDomain, "sender-domain", "", "Only select messages from this sender domain")
		cmd.Flags().BoolVar(&batchStdin, "stdin", false, "Read message IDs from stdin, one per line")
		cmd.Flags().BoolVar(&batchDryRun, "dry-run", false, "Print selected messages without mutating Mail.app")
		cmd.Flags().BoolVar(&batchYes, "yes", false, "Confirm destructive bulk mutation")
		cmd.Flags().BoolVar(&batchMarkReadBefore, "mark-read", false, "Mark each selected message as read before archive/delete/move")
		cmd.Flags().BoolVar(&batchVerify, "verify", false, "Verify each mutation postcondition and include verification status")
		cmd.Flags().BoolVar(&batchProgress, "progress", false, "Print per-chunk and per-message progress to stderr")
		cmd.Flags().IntVar(&batchChunkSize, "chunk-size", 0, "Process selected messages in chunks of this size")
		cmd.Flags().StringVar(&batchReportFile, "report-file", "", "Write the full batch result JSON to this path")
		cmd.Flags().IntVarP(&batchLimit, "limit", "l", 100, "Maximum query/filter-selected messages")
	}
	messagesBatchMarkCmd.Flags().BoolVar(&batchRead, "read", true, "Mark messages as read; use --read=false for unread")
	messagesBatchFlagCmd.Flags().BoolVar(&batchFlagged, "flagged", true, "Flag messages; use --flagged=false to unflag")
}
