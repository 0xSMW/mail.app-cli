package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

// The mutation engine lives in pkg/mail.
type (
	batchItem    = mail.BatchItem
	batchResult  = mail.BatchResult
	batchOptions = mail.BatchOptions
)

// runMessageBatch runs the engine and invalidates the message-list cache for
// every mailbox the run touched. An explicit --mailbox is trusted as the
// source; otherwise archive resolves it through the Envelope Index.
func runMessageBatch(client *mail.Client, opts batchOptions, items []batchItem, mutate mail.Mutator) (batchResult, error) {
	opts.TrustSource = mailboxExplicit()
	result, err := mail.RunBatch(client, opts, items, mutate)
	if !opts.DryRun {
		invalidateBatchCaches(opts.Action, result.Items)
	}
	return result, err
}

func invalidateBatchCaches(action string, items []batchItem) {
	seen := map[string]bool{}
	invalidate := func(account, mailbox string) {
		if account == "" || mailbox == "" {
			return
		}
		key := account + "\x00" + mailbox
		if seen[key] {
			return
		}
		seen[key] = true
		invalidateMailboxCache(account, mailbox)
	}
	for _, item := range items {
		invalidate(item.Account, item.SourceMailbox)
		invalidate(item.Account, item.TargetMailbox)
		if action == "archive" || action == "delete" {
			invalidate(item.Account, "Archive")
			invalidate(item.Account, "All Mail")
		}
	}
}

func renderReceipt(result batchResult, opts batchOptions) func(*output.Printer) {
	return func(p *output.Printer) {
		summary := result.Summary(opts)
		if result.Failed > 0 {
			p.Line("%s", p.Red(summary))
		} else if result.DryRun {
			p.Line("%s", p.Yellow(summary))
		} else {
			p.Line("%s", p.Green(summary))
		}
		if !result.DryRun && result.Failed == 0 && result.Skipped == 0 && len(result.Items) <= 1 {
			return
		}
		rows := make([][]string, 0, len(result.Items))
		for _, item := range result.Items {
			location := item.Account + "/" + item.SourceMailbox
			if item.TargetMailbox != "" {
				location += " → " + item.TargetMailbox
			}
			status := item.Status
			switch item.Status {
			case "failed":
				status = p.Red(status)
			case "succeeded":
				status = p.Green(status)
			default:
				status = p.Yellow(status)
			}
			detail := output.Truncate(item.Subject, 50)
			if item.Error != "" {
				detail = p.Red(output.Truncate(item.Error, 60))
			} else if item.VerifyError != "" {
				detail = p.Red(output.Truncate(item.VerifyError, 60))
			}
			rows = append(rows, []string{p.Dim(item.ID), status, location, detail})
		}
		p.Table([]string{"ID", "STATUS", "LOCATION", "DETAIL"}, rows)
	}
}

// writeReceipt emits the receipt in the current format and returns the
// mutation error, if any, so the process exits non-zero after reporting.
func writeReceipt(result batchResult, opts batchOptions, notices []string, mutationErr error, reportFile string) error {
	var reportErr error
	if reportFile != "" {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			reportErr = fmt.Errorf("encode batch report: %w", err)
		} else if err := os.WriteFile(reportFile, append(data, '\n'), 0o644); err != nil {
			reportErr = fmt.Errorf("write batch report %q: %w", reportFile, err)
		}
	}
	if reportErr != nil {
		// The report is supplemental: mutations have already happened, so their
		// receipt must remain the sole stdout result. Keep this failure as a
		// notice rather than turning a successful receipt into a mutation error.
		notices = append(notices, reportErr.Error())
	}
	if err := writer.Write(output.Result{
		Data:    result,
		Summary: result.Summary(opts),
		Notices: notices,
		Meta:    map[string]any{"action": result.Action, "dryRun": result.DryRun},
		Plain:   renderReceipt(result, opts),
		Err:     clierr.Classify(mutationErr),
	}); err != nil {
		// A reported mutation failure is authoritative even when the optional
		// report could not be written.
		return err
	}
	if reportErr != nil {
		// The receipt above already describes the completed mutation. Return a
		// reported supplemental failure to retain a non-zero status without
		// emitting a second or conflicting error envelope.
		failure := clierr.Wrap(clierr.CodeInternal, reportErr, reportErr.Error())
		failure.Reported = true
		return failure
	}
	return nil
}

// itemsFromRefs turns located messages into receipt items.
func itemsFromRefs(refs []messageRef) []batchItem {
	items := make([]batchItem, 0, len(refs))
	for _, ref := range refs {
		item := batchItem{ID: ref.ID, Account: ref.Account, SourceMailbox: ref.Mailbox}
		if ref.Envelope != nil {
			item.Subject = ref.Envelope.Subject
		}
		items = append(items, item)
	}
	return items
}

// mutateByIDs is the shared body of every ID-driven mutation verb.
func mutateByIDs(ids []string, opts batchOptions, mutate mail.Mutator) error {
	refs, notices, err := locateMessages(ids)
	if err != nil {
		return err
	}
	result, mutationErr := runMessageBatch(mailClient, opts, itemsFromRefs(refs), mutate)
	return writeReceipt(result, opts, notices, mutationErr, "")
}

// --- messages batch -------------------------------------------------------

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
	Short: "Apply bulk operations to selected messages",
	Long:  "Apply bulk operations to selected messages. Select with positional IDs, --stdin, or --query/--sender/--sender-domain within the account and mailbox in scope.",
	Annotations: map[string]string{
		annotationAgentNotes: "Query-selected archive, delete, and move need --yes unless --dry-run is set. Explicit IDs do not. Always preview with --dry-run first.",
	},
}

var messagesBatchArchiveCmd = &cobra.Command{
	Use:   "archive [message-id...]",
	Short: "Archive selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("archive", args, "", mail.ArchiveMutator(false))
	},
}

var messagesBatchDeleteCmd = &cobra.Command{
	Use:   "delete [message-id...]",
	Short: "Delete selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("delete", args, "", mail.DeleteMutator)
	},
}

var messagesBatchMoveCmd = &cobra.Command{
	Use:   "move <target-mailbox> [message-id...]",
	Short: "Move selected messages",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("move", args[1:], args[0], mail.MoveMutator(false))
	},
}

var messagesBatchMarkCmd = &cobra.Command{
	Use:   "mark [message-id...]",
	Short: "Mark selected messages read or unread",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("mark", args, "", mail.MarkMutator(batchRead))
	},
}

var messagesBatchFlagCmd = &cobra.Command{
	Use:   "flag [message-id...]",
	Short: "Flag or unflag selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("flag", args, "", mail.FlagMutator(batchFlagged))
	},
}

func batchOptionsFromFlags(action, target string) batchOptions {
	opts := batchOptions{
		Action:         action,
		TargetMailbox:  target,
		DryRun:         batchDryRun,
		Verify:         batchVerify,
		MarkReadBefore: batchMarkReadBefore,
		ChunkSize:      batchChunkSize,
		Read:           batchRead,
		Flagged:        batchFlagged,
	}
	if batchProgress {
		opts.Progress = writer.Stderr
	}
	return opts
}

func runSelectedBatch(action string, argIDs []string, targetMailbox string, mutate mail.Mutator) error {
	opts := batchOptionsFromFlags(action, targetMailbox)
	bySelector := batchQuery != "" || batchSender != "" || batchSenderDomain != ""
	if bySelector && (len(argIDs) > 0 || batchStdin) {
		return clierr.Usage("--query, --sender, and --sender-domain cannot be combined with message IDs or --stdin")
	}
	if bySelector && !opts.DryRun && requiresBatchConfirmation(action) && !batchYes {
		return clierr.Usagef("%s by selector needs --yes unless --dry-run is set", action).WithHint("run with --dry-run first to see what would change")
	}

	var items []batchItem
	var notices []string
	if bySelector {
		account, err := requireAccount()
		if err != nil {
			return err
		}
		mailbox := mailboxInScope()
		messages, err := resolveBatchMessages(account, mailbox)
		if err != nil {
			return err
		}
		for _, message := range messages {
			items = append(items, batchItem{ID: message.ID, Account: message.Account, SourceMailbox: message.Mailbox, Subject: message.Subject})
		}
	} else {
		ids, err := collectBatchIDs(argIDs)
		if err != nil {
			return err
		}
		refs, locateNotices, err := locateMessages(ids)
		if err != nil {
			return err
		}
		notices = locateNotices
		items = itemsFromRefs(refs)
	}
	if len(items) == 0 {
		return clierr.New(clierr.CodeNotFound, "no messages selected")
	}
	result, mutationErr := runMessageBatch(mailClient, opts, items, mutate)
	return writeReceipt(result, opts, notices, mutationErr, batchReportFile)
}

func collectBatchIDs(argIDs []string) ([]string, error) {
	ids := append([]string(nil), argIDs...)
	if batchStdin {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if id := strings.TrimSpace(scanner.Text()); id != "" {
				ids = append(ids, id)
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	if len(ids) == 0 {
		return nil, clierr.Usage("provide message IDs, --stdin, or --query")
	}
	return uniqueStrings(ids), nil
}

func resolveBatchMessages(account, mailbox string) ([]mail.Message, error) {
	var messages []mail.Message
	var err error
	if batchQuery != "" {
		messages, err = mailClient.SearchMessagesJSON(batchQuery, account, mailbox, batchLimit)
		if err != nil {
			return nil, fmt.Errorf("resolve --query: %w", err)
		}
	} else {
		messages, err = mailClient.GetMessagesJSON(account, mailbox, batchLimit, 0, false, false, false, "")
		if err != nil {
			return nil, fmt.Errorf("list messages for filtered batch: %w", err)
		}
	}
	return mail.FilterBySender(messages, batchSender, batchSenderDomain), nil
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
	messagesBatchCmd.AddCommand(messagesBatchArchiveCmd, messagesBatchDeleteCmd, messagesBatchMoveCmd, messagesBatchMarkCmd, messagesBatchFlagCmd)

	for _, cmd := range []*cobra.Command{
		messagesBatchArchiveCmd, messagesBatchDeleteCmd, messagesBatchMoveCmd, messagesBatchMarkCmd, messagesBatchFlagCmd,
	} {
		cmd.Flags().StringVar(&batchQuery, "query", "", "Search query used to select messages")
		cmd.Flags().StringVar(&batchSender, "sender", "", "Only select messages from this exact sender/email")
		cmd.Flags().StringVar(&batchSenderDomain, "sender-domain", "", "Only select messages from this sender domain")
		cmd.Flags().BoolVar(&batchStdin, "stdin", false, "Read message IDs from stdin, one per line")
		cmd.Flags().BoolVar(&batchDryRun, "dry-run", false, "Report what would change without touching Mail.app")
		cmd.Flags().BoolVar(&batchYes, "yes", false, "Confirm a selector-driven archive, delete, or move")
		cmd.Flags().BoolVar(&batchMarkReadBefore, "mark-read", false, "Mark each selected message read before archive/delete/move")
		cmd.Flags().BoolVar(&batchVerify, "verify", false, "Re-read each message after mutation and record the outcome")
		cmd.Flags().BoolVar(&batchProgress, "progress", false, "Print per-chunk and per-message progress to stderr")
		cmd.Flags().IntVar(&batchChunkSize, "chunk-size", 0, "Process selected messages in chunks of this size")
		cmd.Flags().StringVar(&batchReportFile, "report-file", "", "Also write the receipt JSON to this path")
		cmd.Flags().IntVarP(&batchLimit, "limit", "l", 100, "Maximum selector-selected messages")
	}
	messagesBatchMarkCmd.Flags().BoolVar(&batchRead, "read", true, "Mark messages read; use --read=false for unread")
	messagesBatchFlagCmd.Flags().BoolVar(&batchFlagged, "flagged", true, "Flag messages; use --flagged=false to unflag")
}
