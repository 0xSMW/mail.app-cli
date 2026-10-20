package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// The mutation engine lives in pkg/mail.
type (
	batchItem    = mail.BatchItem
	batchResult  = mail.BatchResult
	batchOptions = mail.BatchOptions
)

// runMessageBatch runs the engine and invalidates the message-list cache for
// every mailbox the run touched. An explicit --mailbox is trusted for ordinary
// moves, but never for archive: Gmail messages can be listed in Important and
// INBOX simultaneously, and archive must remove the INBOX label.
func (a *application) runMessageBatch(client *mail.Client, opts batchOptions, items []batchItem, mutate mail.Mutator) (batchResult, error) {
	opts.TrustSource = trustExplicitSource(opts.Action, a.mailboxExplicit())
	opts.ExplicitSource = a.mailboxExplicit()
	result, err := mail.RunBatch(client, opts, items, mutate)
	if !opts.DryRun {
		invalidateBatchCaches(opts.Action, result.Items)
	}
	return result, err
}

func trustExplicitSource(action string, mailboxWasExplicit bool) bool {
	return mailboxWasExplicit && action != "archive"
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
		} else if result.DryRun || result.Unverified > 0 {
			p.Line("%s", p.Yellow(summary))
		} else {
			p.Line("%s", p.Green(summary))
		}
		if !result.DryRun && result.Failed == 0 && result.Unverified == 0 && result.Skipped == 0 && len(result.Items) <= 1 {
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
				detail = p.Yellow(output.Truncate(item.VerifyError, 60))
			}
			rows = append(rows, []string{p.Dim(item.ID), status, location, detail})
		}
		p.Table([]string{"ID", "STATUS", "LOCATION", "DETAIL"}, rows)
	}
}

// writeReceipt emits the receipt in the current format and returns the
// mutation error, if any, so the process exits non-zero after reporting.
func (a *application) writeReceipt(result batchResult, opts batchOptions, notices []string, mutationErr error, reportFile string) error {
	var reportErr error
	if reportFile != "" && result.ReceiptPath != "" {
		reportErr = writeBatchReport(reportFile, result)
		if reportErr != nil {
			notices = append(notices, reportErr.Error())
		}
	}
	if err := a.writer.Write(output.Result{
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
		failure := clierr.Wrap(clierr.CodeInternal, reportErr, reportErr.Error())
		failure.Reported = true
		return failure
	}
	return nil
}

// preflightBatchReceipt verifies the legacy final report target before Mail
// state can change, then creates a separate append-only sidecar journal.
func (a *application) preflightBatchReceipt(opts *batchOptions, reportFile string) (*mail.BatchJournal, error) {
	journalPath := ""
	if reportFile != "" {
		absoluteReport, err := filepath.Abs(reportFile)
		if err != nil {
			return nil, fmt.Errorf("resolve batch report path: %w", err)
		}
		if err := preflightBatchReport(absoluteReport); err != nil {
			return nil, err
		}
		journalPath = absoluteReport + ".journal.jsonl"
	}
	var (
		journal *mail.BatchJournal
		err     error
	)
	if journalPath == "" {
		journal, err = mail.CreateDefaultBatchJournal()
	} else {
		journal, err = mail.CreateBatchJournal(journalPath)
	}
	if err != nil {
		return nil, err
	}
	opts.Receipt = journal
	fmt.Fprintf(a.writer.Stderr, "receipt journal: %s\n", journal.Path())
	return journal, nil
}

func preflightBatchReport(path string) error {
	dir := filepath.Dir(path)
	probe, err := os.CreateTemp(dir, ".mail-app-cli-report-*")
	if err != nil {
		return fmt.Errorf("preflight batch report %q: %w", path, err)
	}
	probeName := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probeName)
		return fmt.Errorf("preflight batch report %q: %w", path, err)
	}
	if err := os.Remove(probeName); err != nil {
		return fmt.Errorf("preflight batch report %q: %w", path, err)
	}
	if file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
		return fmt.Errorf("preflight batch report %q: %w", path, err)
	} else if err := file.Close(); err != nil {
		return fmt.Errorf("preflight batch report %q: %w", path, err)
	}
	return nil
}

func writeBatchReport(path string, result batchResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode batch report: %w", err)
	}
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".mail-app-cli-report-*")
	if err != nil {
		return fmt.Errorf("write batch report %q: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write batch report %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write batch report %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write batch report %q: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("write batch report %q: %w", path, err)
	}
	return nil
}

func (a *application) runDurableMessageBatch(client *mail.Client, opts batchOptions, items []batchItem, mutate mail.Mutator, reportFile string) (result batchResult, mutationErr error) {
	journal, err := a.preflightBatchReceipt(&opts, reportFile)
	if err != nil {
		return batchResult{Action: opts.Action, DryRun: opts.DryRun}, err
	}
	ctx, stop := signal.NotifyContext(client.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, mutationErr = a.runMessageBatch(client.WithContext(ctx), opts, items, mutate)
	if err := journal.Close(); err != nil {
		result.JournalError = err.Error()
		mutationErr = errors.Join(mutationErr, err)
	}
	return result, mutationErr
}

// itemsFromRefs turns located messages into receipt items.
func itemsFromRefs(refs []messageRef) []batchItem {
	items := make([]batchItem, 0, len(refs))
	for _, ref := range refs {
		item := batchItem{ID: ref.ID, Account: ref.Account, SourceMailbox: ref.Mailbox}
		if ref.Envelope != nil {
			item.Subject = ref.Envelope.Subject
			item.Sender = ref.Envelope.Sender
			item.DateSent = ref.Envelope.DateSent
			item.MessageSize = ref.Envelope.MessageSize
			item.Identity = mail.StableIdentityFromMessage(*ref.Envelope)
		}
		items = append(items, item)
	}
	return items
}

// mutateByIDs is the shared body of every ID-driven mutation verb.
func (a *application) mutateByIDs(ids []string, opts batchOptions, mutate mail.Mutator) error {
	refs, notices, err := a.locateMessages(ids)
	if err != nil {
		return err
	}
	result, mutationErr := a.runDurableMessageBatch(a.mailClient, opts, itemsFromRefs(refs), mutate, "")
	return a.writeReceipt(result, opts, notices, mutationErr, "")
}

// --- messages batch -------------------------------------------------------

func (a *application) batchOptionsFromFlags(action, target string) batchOptions {
	opts := batchOptions{
		Action:         action,
		TargetMailbox:  target,
		DryRun:         a.batchDryRun,
		Verify:         a.batchVerify,
		MarkReadBefore: a.batchMarkReadBefore,
		MarkReadOnSkip: a.batchMarkReadOnSkip,
		Settle:         a.batchSettle,
		VerifyBudget:   a.batchVerifyBudget,
		ChunkSize:      a.batchChunkSize,
		ReuseBridge:    true,
		Read:           a.batchRead,
		Flagged:        a.batchFlagged,
	}
	if a.batchProgress {
		opts.Progress = a.writer.Stderr
	}
	return opts
}

func (a *application) runSelectedBatch(action string, argIDs []string, targetMailbox string, mutate mail.Mutator) error {
	opts := a.batchOptionsFromFlags(action, targetMailbox)
	if err := mail.ValidateBatchOptions(opts); err != nil {
		return clierr.Usage(err.Error())
	}
	bySelector := a.batchQuery != "" || a.batchSender != "" || a.batchSenderDomain != ""
	if bySelector && (len(argIDs) > 0 || a.batchStdin) {
		return clierr.Usage("--query, --sender, and --sender-domain cannot be combined with message IDs or --stdin")
	}
	if bySelector && !opts.DryRun && requiresBatchConfirmation(action) && !a.batchYes {
		return clierr.Usagef("%s by selector needs --yes unless --dry-run is set", action).WithHint("run with --dry-run first to see what would change")
	}

	var items []batchItem
	var notices []string
	if bySelector {
		account, err := a.requireAccount()
		if err != nil {
			return err
		}
		mailbox := a.mailboxInScope()
		messages, err := a.resolveBatchMessages(account, mailbox)
		if err != nil {
			return err
		}
		for _, message := range messages {
			items = append(items, batchItem{
				ID: message.ID, Account: message.Account, SourceMailbox: message.Mailbox,
				Subject: message.Subject, Sender: message.Sender, DateSent: message.DateSent,
				MessageSize: message.MessageSize, Identity: mail.StableIdentityFromMessage(message),
			})
		}
	} else {
		ids, err := a.collectBatchIDs(argIDs)
		if err != nil {
			return err
		}
		refs, locateNotices, err := a.locateMessages(ids)
		if err != nil {
			return err
		}
		notices = locateNotices
		items = itemsFromRefs(refs)
	}
	if len(items) == 0 {
		return clierr.New(clierr.CodeNotFound, "no messages selected")
	}
	result, mutationErr := a.runDurableMessageBatch(a.mailClient, opts, items, mutate, a.batchReportFile)
	return a.writeReceipt(result, opts, notices, mutationErr, a.batchReportFile)
}

func (a *application) collectBatchIDs(argIDs []string) ([]string, error) {
	ids := append([]string(nil), argIDs...)
	if a.batchStdin {
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

func (a *application) resolveBatchMessages(account, mailbox string) ([]mail.Message, error) {
	var messages []mail.Message
	var err error
	if a.batchQuery != "" {
		messages, err = a.mailClient.SearchMessagesJSON(a.batchQuery, account, mailbox, a.batchLimit)
		if err != nil {
			return nil, fmt.Errorf("resolve --query: %w", err)
		}
	} else {
		messages, err = a.mailClient.GetMessagesJSON(account, mailbox, a.batchLimit, 0, false, false, false, "")
		if err != nil {
			return nil, fmt.Errorf("list messages for filtered batch: %w", err)
		}
	}
	return mail.FilterBySender(messages, a.batchSender, a.batchSenderDomain), nil
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

func (a *application) buildBatchCommands() {
	a.messagesBatchCmd = &cobra.Command{
		Use:   "batch",
		Short: "Apply bulk operations to selected messages",
		Long:  "Apply bulk operations to selected messages. Select with positional IDs, --stdin, or --query/--sender/--sender-domain within the account and mailbox in scope.",
		Annotations: map[string]string{
			annotationAgentNotes: "Query-selected archive, delete, and move need --yes unless --dry-run is set. Explicit IDs do not. Always preview with --dry-run first.",
		},
	}
	a.messagesBatchArchiveCmd = &cobra.Command{
		Use:   "archive [message-id...]",
		Short: "Archive selected messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSelectedBatch("archive", args, "", mail.ArchiveMutator(false))
		},
	}
	a.messagesBatchDeleteCmd = &cobra.Command{
		Use:   "delete [message-id...]",
		Short: "Delete selected messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSelectedBatch("delete", args, "", mail.DeleteMutator)
		},
	}
	a.messagesBatchMoveCmd = &cobra.Command{
		Use:   "move <target-mailbox> [message-id...]",
		Short: "Move selected messages",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSelectedBatch("move", args[1:], args[0], mail.MoveMutator(false))
		},
	}
	a.messagesBatchMarkCmd = &cobra.Command{
		Use:   "mark [message-id...]",
		Short: "Mark selected messages read or unread",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSelectedBatch("mark", args, "", mail.MarkMutator(a.batchRead))
		},
	}
	a.messagesBatchFlagCmd = &cobra.Command{
		Use:   "flag [message-id...]",
		Short: "Flag or unflag selected messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSelectedBatch("flag", args, "", mail.FlagMutator(a.batchFlagged))
		},
	}

	a.messagesBatchCmd.AddCommand(a.messagesBatchArchiveCmd, a.messagesBatchDeleteCmd, a.messagesBatchMoveCmd, a.messagesBatchMarkCmd, a.messagesBatchFlagCmd)

	for _, cmd := range []*cobra.Command{
		a.messagesBatchArchiveCmd, a.messagesBatchDeleteCmd, a.messagesBatchMoveCmd, a.messagesBatchMarkCmd, a.messagesBatchFlagCmd,
	} {
		cmd.Flags().StringVar(&a.batchQuery, "query", "", "Search query used to select messages")
		cmd.Flags().StringVar(&a.batchSender, "sender", "", "Only select messages from this exact sender/email")
		cmd.Flags().StringVar(&a.batchSenderDomain, "sender-domain", "", "Only select messages from this sender domain")
		cmd.Flags().BoolVar(&a.batchStdin, "stdin", false, "Read message IDs from stdin, one per line")
		cmd.Flags().BoolVar(&a.batchDryRun, "dry-run", false, "Report what would change without touching Mail.app")
		cmd.Flags().BoolVar(&a.batchYes, "yes", false, "Confirm a selector-driven archive, delete, or move")
		cmd.Flags().BoolVar(&a.batchMarkReadBefore, "mark-read", false, "Mark each selected message read before archive/delete/move")
		cmd.Flags().DurationVar(&a.batchSettle, "settle", 0, "Extend the final verification observation window (up to 5m)")
		cmd.Flags().DurationVar(&a.batchVerifyBudget, "verify-budget", 0, "Bound all verification checks (default 30s, up to 10m)")
		cmd.Flags().BoolVar(&a.batchVerify, "verify", false, "Re-read each message after mutation and record the outcome")
		cmd.Flags().BoolVar(&a.batchProgress, "progress", false, "Print per-chunk and per-message progress to stderr")
		cmd.Flags().IntVar(&a.batchChunkSize, "chunk-size", 0, "Process selected messages in chunks of this size")
		cmd.Flags().StringVar(&a.batchReportFile, "report-file", "", "Also write the receipt JSON to this path; journal uses .journal.jsonl")
		cmd.Flags().IntVarP(&a.batchLimit, "limit", "l", 100, "Maximum selector-selected messages")
	}
	a.messagesBatchArchiveCmd.Flags().BoolVar(&a.batchMarkReadOnSkip, "mark-read-on-skip", false, "Mark already archived messages read; requires --mark-read")
	a.messagesBatchMarkCmd.Flags().BoolVar(&a.batchRead, "read", true, "Mark messages read; use --read=false for unread")
	a.messagesBatchFlagCmd.Flags().BoolVar(&a.batchFlagged, "flagged", true, "Flag messages; use --flagged=false to unflag")
}
