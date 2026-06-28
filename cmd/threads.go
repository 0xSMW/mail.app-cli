package cmd

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

type threadSummary struct {
	ID           string         `json:"id"`
	Subject      string         `json:"subject"`
	Count        int            `json:"count"`
	UnreadCount  int            `json:"unreadCount"`
	LatestDate   string         `json:"latestDate"`
	Participants []string       `json:"participants"`
	MessageIDs   []string       `json:"messageIds"`
	Messages     []mail.Message `json:"messages,omitempty"`
}

var (
	threadAccount string
	threadMailbox string
	threadLimit   int
	threadDryRun  bool
)

var threadsCmd = &cobra.Command{
	Use:   "threads",
	Short: "Group and act on message threads",
}

var threadsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List message threads",
	RunE: func(cmd *cobra.Command, args []string) error {
		threads, err := loadThreads()
		if err != nil {
			return err
		}
		return printJSON(threads, "threads")
	},
}

var threadsShowCmd = &cobra.Command{
	Use:   "show [thread-id]",
	Short: "Show a message thread",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		threads, err := loadThreads()
		if err != nil {
			return err
		}
		for _, thread := range threads {
			if thread.ID == args[0] {
				thread.Messages = messagesForThread(thread.MessageIDs, threadAccount, threadMailbox)
				return printJSON(thread, "thread")
			}
		}
		return fmt.Errorf("thread not found: %s", args[0])
	},
}

var threadsArchiveCmd = &cobra.Command{
	Use:   "archive [thread-id]",
	Short: "Archive every message in a thread",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAccountAndMailbox(threadAccount, threadMailbox); err != nil {
			return err
		}
		threads, err := loadThreads()
		if err != nil {
			return err
		}
		for _, thread := range threads {
			if thread.ID != args[0] {
				continue
			}
			oldAccount, oldMailbox, oldDryRun, oldYes := msgAccount, msgMailbox, batchDryRun, batchYes
			msgAccount, msgMailbox, batchDryRun, batchYes = threadAccount, threadMailbox, threadDryRun, true
			defer func() {
				msgAccount, msgMailbox, batchDryRun, batchYes = oldAccount, oldMailbox, oldDryRun, oldYes
			}()
			return runMessageBatch("archive", thread.MessageIDs, "", func(client *mail.Client, item batchItem) error {
				return client.ArchiveMessage(item.Account, item.SourceMailbox, item.ID)
			})
		}
		return fmt.Errorf("thread not found: %s", args[0])
	},
}

var lastLoadedThreadMessages []mail.Message

func loadThreads() ([]threadSummary, error) {
	if err := requireAccountAndMailbox(threadAccount, threadMailbox); err != nil {
		return nil, err
	}
	client := mail.NewClient()
	messages, err := client.GetMessagesJSON(threadAccount, threadMailbox, threadLimit, 0, false, false, false, "")
	if err != nil {
		return nil, err
	}
	lastLoadedThreadMessages = messages
	byKey := map[string]*threadSummary{}
	for _, message := range messages {
		key := normalizeThreadSubject(message.Subject)
		if key == "" {
			key = "message-" + message.ID
		}
		thread, ok := byKey[key]
		if !ok {
			thread = &threadSummary{
				ID:      key,
				Subject: strings.TrimSpace(message.Subject),
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
	return threads, nil
}

func messagesForThread(ids []string, account, mailbox string) []mail.Message {
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	var messages []mail.Message
	for _, message := range lastLoadedThreadMessages {
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
		cmd.Flags().StringVarP(&threadAccount, "account", "a", "", "Account name")
		cmd.Flags().StringVarP(&threadMailbox, "mailbox", "m", "", "Mailbox name")
		cmd.Flags().IntVarP(&threadLimit, "limit", "l", 200, "Maximum messages to inspect")
	}
	threadsArchiveCmd.Flags().BoolVar(&threadDryRun, "dry-run", false, "Show mutation without applying it")
}
