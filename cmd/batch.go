package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

// batchItem is one message inside a mutation receipt. The same shape is
// used whether one ID or five hundred were requested.
type batchItem struct {
	ID            string `json:"id"`
	Account       string `json:"account"`
	SourceMailbox string `json:"sourceMailbox"`
	TargetMailbox string `json:"targetMailbox,omitempty"`
	Subject       string `json:"subject,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	MarkedRead    bool   `json:"markedRead,omitempty"`
	VerifyStatus  string `json:"verifyStatus,omitempty"`
	VerifyError   string `json:"verifyError,omitempty"`
}

// batchResult is the mutation receipt.
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

type batchOptions struct {
	Action         string
	TargetMailbox  string
	DryRun         bool
	Verify         bool
	Progress       bool
	MarkReadBefore bool
	ChunkSize      int
	Read           bool
	Flagged        bool
	// Journal records each message in the recent-message journal. It costs
	// index queries per message, so bulk selections leave it off.
	Journal bool
}

type mutator func(*mail.Client, *batchItem) error

func archiveMutator(journal bool) mutator {
	return func(client *mail.Client, item *batchItem) error {
		if journal {
			_ = client.RecordRecentEnvelope(item.Account, item.SourceMailbox, item.ID, "archive")
		}
		destination, err := client.ArchiveMessageWithDestination(item.Account, item.SourceMailbox, item.ID)
		if err != nil {
			return err
		}
		item.TargetMailbox = destination
		if journal {
			_ = mail.UpdateRecentMessageLocation(item.Account, item.ID, destination, "archive")
		}
		return nil
	}
}

func deleteMutator(client *mail.Client, item *batchItem) error {
	return client.DeleteMessageResolved(item.Account, item.SourceMailbox, item.ID)
}

func moveMutator(journal bool) mutator {
	return func(client *mail.Client, item *batchItem) error {
		if journal {
			_ = client.RecordRecentEnvelope(item.Account, item.SourceMailbox, item.ID, "move")
		}
		if err := client.MoveMessage(item.Account, item.SourceMailbox, item.ID, item.TargetMailbox); err != nil {
			return err
		}
		if journal {
			_ = mail.UpdateRecentMessageLocation(item.Account, item.ID, item.TargetMailbox, "move")
		}
		return nil
	}
}

func markMutator(read bool) mutator {
	return func(client *mail.Client, item *batchItem) error {
		return client.MarkMessageAsRead(item.Account, item.SourceMailbox, item.ID, read)
	}
}

func flagMutator(flagged bool) mutator {
	return func(client *mail.Client, item *batchItem) error {
		return client.FlagMessage(item.Account, item.SourceMailbox, item.ID, flagged)
	}
}

// runMessageBatch applies mutate to every item and returns the receipt. The
// error is non-nil only when at least one item failed; the receipt is
// always complete.
func runMessageBatch(client *mail.Client, opts batchOptions, items []batchItem, mutate mutator) (batchResult, error) {
	result := batchResult{
		Action:    opts.Action,
		DryRun:    opts.DryRun,
		StartedAt: time.Now().Format(time.RFC3339),
		Matched:   len(items),
		Items:     make([]batchItem, 0, len(items)),
	}
	for i := range items {
		if opts.TargetMailbox != "" {
			items[i].TargetMailbox = opts.TargetMailbox
		}
	}
	chunkSize := normalizedBatchChunkSize(len(items), opts.ChunkSize)
	result.Chunks = (len(items) + chunkSize - 1) / chunkSize
	if opts.DryRun {
		for _, item := range items {
			item.Status = "dry-run"
			result.Skipped++
			result.Items = append(result.Items, item)
		}
		result.EndedAt = time.Now().Format(time.RFC3339)
		return result, nil
	}

	for start := 0; start < len(items); start += chunkSize {
		end := start + chunkSize
		if end > len(items) {
			end = len(items)
		}
		if opts.Progress {
			fmt.Fprintf(writer.Stderr, "%s: chunk %d/%d (%d messages)\n", opts.Action, (start/chunkSize)+1, result.Chunks, end-start)
		}
		for _, item := range items[start:end] {
			if opts.Action == "move" && strings.EqualFold(item.TargetMailbox, item.SourceMailbox) {
				item.Status = "skipped"
				item.Error = "already in " + item.TargetMailbox
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			result.Attempted++
			if opts.MarkReadBefore && opts.Action != "mark" {
				if err := client.MarkMessageAsRead(item.Account, item.SourceMailbox, item.ID, true); err != nil {
					item.Status = "failed"
					item.Error = fmt.Sprintf("mark-read before %s failed: %v", opts.Action, err)
					result.Failed++
					result.Items = append(result.Items, item)
					continue
				}
				item.MarkedRead = true
			}
			if err := mutate(client, &item); err != nil {
				item.Status = "failed"
				item.Error = err.Error()
				result.Failed++
			} else {
				item.Status = "succeeded"
				if opts.Verify {
					verifyStatus, verifyErr := verifyBatchMutation(client, opts, item)
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
			if opts.Progress {
				fmt.Fprintf(writer.Stderr, "%s: %d/%d %s %s\n", opts.Action, result.Attempted, len(items), item.ID, item.Status)
			}
			result.Items = append(result.Items, item)
		}
	}
	result.EndedAt = time.Now().Format(time.RFC3339)
	invalidateBatchCaches(opts.Action, result.Items)

	if result.Failed > 0 {
		return result, clierr.New(clierr.CodeMutationFailed, fmt.Sprintf("%s failed for %d of %d message(s)", opts.Action, result.Failed, result.Attempted))
	}
	return result, nil
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

func verifyBatchMutation(client *mail.Client, opts batchOptions, item batchItem) (string, error) {
	present := func(mailbox string) (bool, error) {
		message, err := client.GetMessageDetailsForVerificationJSON(item.Account, mailbox, item.ID)
		if err != nil {
			return false, err
		}
		return message != nil, nil
	}
	switch opts.Action {
	case "archive", "move":
		// Mail.app may keep the ID (Gmail label changes) or assign a new one
		// (real moves), and Gmail keeps every message in All Mail. So: a
		// no-op is fine; archiving into All Mail is proven by absence from
		// the source; moving out of All Mail is proven by presence in the
		// destination; anything else accepts either proof.
		if item.TargetMailbox == "" || strings.EqualFold(item.TargetMailbox, item.SourceMailbox) {
			return "already-in-destination", nil
		}
		if mail.IsArchiveAlias(item.TargetMailbox) {
			inSource, err := present(item.SourceMailbox)
			if err != nil {
				return "verification-failed", err
			}
			if inSource {
				return "present-in-source", fmt.Errorf("message still present in %s", item.SourceMailbox)
			}
			return "absent-from-source", nil
		}
		inDestination, err := present(item.TargetMailbox)
		if err != nil {
			return "verification-failed", err
		}
		if inDestination {
			return "present-in-destination", nil
		}
		if mail.IsArchiveAlias(item.SourceMailbox) {
			return "destination-unverified", fmt.Errorf("message not found in %s by its old ID; Mail.app may have renumbered it", item.TargetMailbox)
		}
		inSource, err := present(item.SourceMailbox)
		if err != nil {
			return "verification-failed", err
		}
		if !inSource {
			return "absent-from-source", nil
		}
		return "present-in-source", fmt.Errorf("message still present in %s", item.SourceMailbox)
	case "delete":
		inSource, err := present(item.SourceMailbox)
		if err != nil {
			return "verification-failed", err
		}
		if inSource {
			return "present-in-source", fmt.Errorf("message still present in %s", item.SourceMailbox)
		}
		return "absent-from-source", nil
	}
	message, err := client.GetMessageDetailsForVerificationJSON(item.Account, item.SourceMailbox, item.ID)
	if err != nil {
		return "verification-failed", err
	}
	if message == nil {
		return "verification-failed", fmt.Errorf("message not found after mutation")
	}
	if opts.Action == "mark" {
		if message.Read == opts.Read {
			return "matched", nil
		}
		return "mismatch", fmt.Errorf("read status mismatch")
	}
	if opts.Action == "flag" {
		if message.Flagged == opts.Flagged {
			return "matched", nil
		}
		return "mismatch", fmt.Errorf("flagged status mismatch")
	}
	return "unchecked", nil
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

// receiptSummary is the one-line human description of a receipt.
func receiptSummary(result batchResult, opts batchOptions) string {
	verb := map[string]string{
		"archive": "Archived", "delete": "Deleted", "move": "Moved", "mark": "Marked", "flag": "Flagged",
	}[result.Action]
	if result.Action == "mark" {
		if opts.Read {
			verb = "Marked read"
		} else {
			verb = "Marked unread"
		}
	}
	if result.Action == "flag" && !opts.Flagged {
		verb = "Unflagged"
	}
	count := plural(result.Matched, "message")
	target := ""
	if result.Action == "move" && opts.TargetMailbox != "" {
		target = " to " + opts.TargetMailbox
	}
	if result.Action == "archive" {
		destinations := map[string]bool{}
		for _, item := range result.Items {
			if item.TargetMailbox != "" {
				destinations[item.TargetMailbox] = true
			}
		}
		if len(destinations) == 1 {
			for name := range destinations {
				target = " to " + name
			}
		}
	}
	if result.DryRun {
		return fmt.Sprintf("Dry run: would have %s %s%s", strings.ToLower(verb), count, target)
	}
	if result.Failed > 0 || result.Skipped > 0 {
		summary := fmt.Sprintf("%s %d of %s%s", verb, result.Succeeded, count, target)
		if result.Failed > 0 {
			summary += fmt.Sprintf("; %d failed", result.Failed)
		}
		if result.Skipped > 0 {
			summary += fmt.Sprintf("; %d already there", result.Skipped)
		}
		return summary
	}
	return fmt.Sprintf("%s %s%s", verb, count, target)
}

func renderReceipt(result batchResult, opts batchOptions) func(*output.Printer) {
	return func(p *output.Printer) {
		summary := receiptSummary(result, opts)
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
	if reportFile != "" {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("encode batch report: %w", err)
		}
		if err := os.WriteFile(reportFile, append(data, '\n'), 0o644); err != nil {
			return err
		}
	}
	return writer.Write(output.Result{
		Data:    result,
		Summary: receiptSummary(result, opts),
		Notices: notices,
		Meta:    map[string]any{"action": result.Action, "dryRun": result.DryRun},
		Plain:   renderReceipt(result, opts),
		Err:     clierr.Classify(mutationErr),
	})
}

// itemsFromRefs turns located messages into receipt items. Archive acts
// from the INBOX-or-backing mailbox so it never strips a user label.
func itemsFromRefs(refs []messageRef, action string) []batchItem {
	items := make([]batchItem, 0, len(refs))
	for _, ref := range refs {
		source := ref.Mailbox
		if action == "archive" && ref.ArchiveMailbox != "" {
			source = ref.ArchiveMailbox
		}
		item := batchItem{ID: ref.ID, Account: ref.Account, SourceMailbox: source}
		if ref.Envelope != nil {
			item.Subject = ref.Envelope.Subject
		}
		items = append(items, item)
	}
	return items
}

// mutateByIDs is the shared body of every ID-driven mutation verb.
func mutateByIDs(ids []string, opts batchOptions, mutate mutator) error {
	refs, notices, err := locateMessages(ids)
	if err != nil {
		return err
	}
	result, mutationErr := runMessageBatch(mailClient, opts, itemsFromRefs(refs, opts.Action), mutate)
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
		return runSelectedBatch("archive", args, "", archiveMutator(false))
	},
}

var messagesBatchDeleteCmd = &cobra.Command{
	Use:   "delete [message-id...]",
	Short: "Delete selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("delete", args, "", deleteMutator)
	},
}

var messagesBatchMoveCmd = &cobra.Command{
	Use:   "move <target-mailbox> [message-id...]",
	Short: "Move selected messages",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("move", args[1:], args[0], moveMutator(false))
	},
}

var messagesBatchMarkCmd = &cobra.Command{
	Use:   "mark [message-id...]",
	Short: "Mark selected messages read or unread",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("mark", args, "", markMutator(batchRead))
	},
}

var messagesBatchFlagCmd = &cobra.Command{
	Use:   "flag [message-id...]",
	Short: "Flag or unflag selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelectedBatch("flag", args, "", flagMutator(batchFlagged))
	},
}

func batchOptionsFromFlags(action, target string) batchOptions {
	return batchOptions{
		Action:         action,
		TargetMailbox:  target,
		DryRun:         batchDryRun,
		Verify:         batchVerify,
		Progress:       batchProgress,
		MarkReadBefore: batchMarkReadBefore,
		ChunkSize:      batchChunkSize,
		Read:           batchRead,
		Flagged:        batchFlagged,
	}
}

func runSelectedBatch(action string, argIDs []string, targetMailbox string, mutate mutator) error {
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
		items = itemsFromRefs(refs, action)
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
	return filterMessagesBySender(messages, batchSender, batchSenderDomain), nil
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
