package cmd

import (
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

type threadSummary = mail.ThreadSummary

func renderThreads(threads []threadSummary) func(*output.Printer) {
	return func(p *output.Printer) {
		rows := make([][]string, 0, len(threads))
		for _, t := range threads {
			subject := output.Truncate(t.Subject, 60)
			if t.UnreadCount > 0 {
				subject = p.Bold(subject)
			}
			rows = append(rows, []string{p.Dim(output.Truncate(t.ID, 40)), formatDate(t.LatestDate), fmt.Sprintf("%d/%d", t.UnreadCount, t.Count), subject, output.Truncate(strings.Join(t.Participants, ", "), 40)})
		}
		if len(rows) == 0 {
			p.Line("%s", p.Dim("no threads"))
			return
		}
		p.Table([]string{"THREAD", "LATEST", "UNREAD/ALL", "SUBJECT", "PARTICIPANTS"}, rows)
	}
}

func (a *application) loadThreads() ([]threadSummary, []mail.Message, error) {
	account, err := a.requireAccount()
	if err != nil {
		return nil, nil, err
	}
	messages, err := a.mailClient.GetMessagesJSON(account, a.mailboxInScope(), a.threadLimit, 0, false, false, false, "")
	if err != nil {
		return nil, nil, err
	}
	return mail.GroupThreads(messages), messages, nil
}

func (a *application) buildThreadsCommands() {
	var (
		threadDryRun bool
		threadVerify bool
	)

	a.threadsCmd = &cobra.Command{
		Use:   "threads",
		Short: "Group messages in one mailbox by subject",
		Annotations: map[string]string{
			annotationAgentNotes: "Threads are synthetic: grouped by normalized subject, not by Message-ID headers. 'synthetic: true' with count > 1 means the grouping is a guess, and 'threads archive' refuses it.",
		},
	}
	a.threadsListCmd = &cobra.Command{
		Use:   "list",
		Short: "List threads in the mailbox in scope",
		RunE: func(cmd *cobra.Command, args []string) error {
			threads, _, err := a.loadThreads()
			if err != nil {
				return err
			}
			return a.writer.Write(output.Result{Data: threads, Summary: plural(len(threads), "thread"), Plain: renderThreads(threads)})
		},
	}
	threadsShowCmd := &cobra.Command{
		Use:   "show <thread-id>",
		Short: "Show a thread's messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			threads, messages, err := a.loadThreads()
			if err != nil {
				return err
			}
			for _, thread := range threads {
				if thread.ID == args[0] {
					thread.Messages = mail.MessagesForThread(thread.MessageIDs, messages)
					return a.writer.Write(output.Result{
						Data:    thread,
						Summary: fmt.Sprintf("Thread %q with %s", thread.Subject, plural(thread.Count, "message")),
						Plain:   renderMessages(thread.Messages, false),
					})
				}
			}
			return clierr.New(clierr.CodeNotFound, "thread not found: "+args[0])
		},
	}
	a.threadsArchiveCmd = &cobra.Command{
		Use:   "archive <thread-id>",
		Short: "Archive every message in a thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			threads, messages, err := a.loadThreads()
			if err != nil {
				return err
			}
			for _, thread := range threads {
				if thread.ID != args[0] {
					continue
				}
				if !mail.ThreadArchiveAllowed(thread) {
					return clierr.Usagef("refusing to archive synthetic subject-only thread %q; archive individual messages instead", thread.ID)
				}
				items := make([]batchItem, 0, len(thread.MessageIDs))
				for _, message := range mail.MessagesForThread(thread.MessageIDs, messages) {
					items = append(items, batchItem{ID: message.ID, Account: message.Account, SourceMailbox: message.Mailbox, Subject: message.Subject})
				}
				opts := batchOptions{Action: "archive", DryRun: threadDryRun, Verify: threadVerify}
				result, mutationErr := a.runDurableMessageBatch(a.mailClient, opts, items, mail.ArchiveMutator(false), "")
				return a.writeReceipt(result, opts, nil, mutationErr, "")
			}
			return clierr.New(clierr.CodeNotFound, "thread not found: "+args[0])
		},
	}

	addGmailLabelHelp(a.threadsArchiveCmd)
	a.threadsCmd.AddCommand(a.threadsListCmd, threadsShowCmd, a.threadsArchiveCmd)
	for _, cmd := range []*cobra.Command{a.threadsListCmd, threadsShowCmd, a.threadsArchiveCmd} {
		cmd.Flags().IntVarP(&a.threadLimit, "limit", "l", 200, "Maximum messages to inspect")
	}
	a.threadsArchiveCmd.Flags().BoolVar(&threadDryRun, "dry-run", false, "Report what would change without touching Mail.app")
	a.threadsArchiveCmd.Flags().BoolVar(&threadVerify, "verify", false, "Re-read each message after mutation and record the outcome")
}
