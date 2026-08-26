package cmd

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

type threadSummary struct {
	ID           string         `json:"id"`
	Subject      string         `json:"subject"`
	Synthetic    bool           `json:"synthetic"`
	Count        int            `json:"count"`
	UnreadCount  int            `json:"unreadCount"`
	LatestDate   string         `json:"latestDate"`
	Participants []string       `json:"participants"`
	MessageIDs   []string       `json:"messageIds"`
	Messages     []mail.Message `json:"messages,omitempty"`
}

var (
	threadLimit  int
	threadDryRun bool
	threadVerify bool
)

var threadsCmd = &cobra.Command{
	Use:   "threads",
	Short: "Group messages in one mailbox by subject",
	Annotations: map[string]string{
		annotationAgentNotes: "Threads are synthetic: grouped by normalized subject, not by Message-ID headers. 'synthetic: true' with count > 1 means the grouping is a guess, and 'threads archive' refuses it.",
	},
}

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

var threadsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List threads in the mailbox in scope",
	RunE: func(cmd *cobra.Command, args []string) error {
		threads, _, err := loadThreads()
		if err != nil {
			return err
		}
		return writer.Write(output.Result{Data: threads, Summary: plural(len(threads), "thread"), Plain: renderThreads(threads)})
	},
}

var threadsShowCmd = &cobra.Command{
	Use:   "show <thread-id>",
	Short: "Show a thread's messages",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		threads, messages, err := loadThreads()
		if err != nil {
			return err
		}
		for _, thread := range threads {
			if thread.ID == args[0] {
				thread.Messages = messagesForThread(thread.MessageIDs, messages)
				return writer.Write(output.Result{
					Data:    thread,
					Summary: fmt.Sprintf("Thread %q with %s", thread.Subject, plural(thread.Count, "message")),
					Plain:   renderMessages(thread.Messages, false),
				})
			}
		}
		return clierr.New(clierr.CodeNotFound, "thread not found: "+args[0])
	},
}

var threadsArchiveCmd = &cobra.Command{
	Use:   "archive <thread-id>",
	Short: "Archive every message in a thread",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		threads, messages, err := loadThreads()
		if err != nil {
			return err
		}
		for _, thread := range threads {
			if thread.ID != args[0] {
				continue
			}
			if !threadArchiveAllowed(thread) {
				return clierr.Usagef("refusing to archive synthetic subject-only thread %q; archive individual messages instead", thread.ID)
			}
			items := make([]batchItem, 0, len(thread.MessageIDs))
			for _, message := range messagesForThread(thread.MessageIDs, messages) {
				items = append(items, batchItem{ID: message.ID, Account: message.Account, SourceMailbox: message.Mailbox, Subject: message.Subject})
			}
			opts := batchOptions{Action: "archive", DryRun: threadDryRun, Verify: threadVerify}
			result, mutationErr := runMessageBatch(mailClient, opts, items, archiveMutator(false))
			return writeReceipt(result, opts, nil, mutationErr, "")
		}
		return clierr.New(clierr.CodeNotFound, "thread not found: "+args[0])
	},
}

func loadThreads() ([]threadSummary, []mail.Message, error) {
	account, err := requireAccount()
	if err != nil {
		return nil, nil, err
	}
	messages, err := mailClient.GetMessagesJSON(account, mailboxInScope(), threadLimit, 0, false, false, false, "")
	if err != nil {
		return nil, nil, err
	}
	byKey := map[string]*threadSummary{}
	for _, message := range messages {
		key := normalizeThreadSubject(message.Subject)
		if key == "" {
			key = "message-" + message.ID
		}
		thread, ok := byKey[key]
		if !ok {
			thread = &threadSummary{
				ID:           key,
				Subject:      strings.TrimSpace(message.Subject),
				Synthetic:    !strings.HasPrefix(key, "message-"),
				Participants: []string{},
				MessageIDs:   []string{},
			}
			byKey[key] = thread
		}
		thread.Count++
		if !message.Read {
			thread.UnreadCount++
		}
		if message.DateReceived > thread.LatestDate {
			thread.LatestDate = message.DateReceived
		}
		thread.MessageIDs = append(thread.MessageIDs, message.ID)
		if message.Sender != "" && !containsString(thread.Participants, message.Sender) {
			thread.Participants = append(thread.Participants, message.Sender)
		}
	}
	threads := make([]threadSummary, 0, len(byKey))
	for _, thread := range byKey {
		sort.Strings(thread.Participants)
		threads = append(threads, *thread)
	}
	sort.Slice(threads, func(i, j int) bool {
		return threads[i].LatestDate > threads[j].LatestDate
	})
	return threads, messages, nil
}

func threadArchiveAllowed(thread threadSummary) bool {
	return !thread.Synthetic || thread.Count <= 1
}

func messagesForThread(ids []string, loaded []mail.Message) []mail.Message {
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	var messages []mail.Message
	for _, message := range loaded {
		if idSet[message.ID] {
			messages = append(messages, message)
		}
	}
	return messages
}

func normalizeThreadSubject(subject string) string {
	subject = strings.TrimSpace(strings.ToLower(subject))
	for {
		trimmed := strings.TrimSpace(subject)
		for _, prefix := range []string{"re:", "fw:", "fwd:"} {
			if strings.HasPrefix(trimmed, prefix) {
				trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			}
		}
		if trimmed == subject {
			break
		}
		subject = trimmed
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range subject {
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func init() {
	threadsCmd.AddCommand(threadsListCmd, threadsShowCmd, threadsArchiveCmd)
	for _, cmd := range []*cobra.Command{threadsListCmd, threadsShowCmd, threadsArchiveCmd} {
		cmd.Flags().IntVarP(&threadLimit, "limit", "l", 200, "Maximum messages to inspect")
	}
	threadsArchiveCmd.Flags().BoolVar(&threadDryRun, "dry-run", false, "Report what would change without touching Mail.app")
	threadsArchiveCmd.Flags().BoolVar(&threadVerify, "verify", false, "Re-read each message after mutation and record the outcome")
}
