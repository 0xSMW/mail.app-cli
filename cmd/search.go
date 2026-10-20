package cmd

import (
	"errors"
	"fmt"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// partialSearchOutputData preserves completeness metadata for regular search
// output while allowing list modifiers to operate on the matched messages.
func partialSearchOutputData(result mail.SearchResult, format output.Format) any {
	if format == output.FormatCount || format == output.FormatIDs {
		return result.Messages
	}
	return result
}

func (a *application) buildSearchCommands() {
	var (
		searchLimit        int
		searchSince        string
		searchAllowPartial bool
		searchNoCache      bool
		searchSender       string
		searchSenderDomain string
	)

	a.searchCmd = &cobra.Command{
		Use:   "search <query>",
		Short: "Search messages by subject, sender, and indexed summary",
		Long: `Search messages. Every term must match somewhere in the subject, sender,
or Mail's indexed summary. Without --mailbox the search covers every
mailbox with messages or an unknown count, plus Inbox, of each enabled
account (or of the one named with
--account); with --mailbox it is limited to that mailbox.`,
		Args: cobra.ExactArgs(1),
		Annotations: map[string]string{
			annotationAgentNotes: "Fails closed with exit 5 when any mailbox could not be completely searched; add --allow-partial to accept incomplete results, which then arrive as {messages, complete, searchedMailboxes, failedMailboxes}. Needs Envelope Index access (Full Disk Access) for cross-mailbox queries.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			account := a.resolved.Account.Value
			mailbox := ""
			if a.mailboxExplicit() {
				if account == "" {
					return clierr.Usage("--mailbox requires --account")
				}
				mailbox = a.mailboxInScope()
			}
			query := args[0]
			result, err := a.mailClient.SearchMessagesJSONSinceWithOptions(query, account, mailbox, searchLimit, searchSince, mail.SearchOptions{
				AllowPartial: searchAllowPartial,
			})
			if err != nil {
				var partialErr *mail.PartialSearchError
				if errors.As(err, &partialErr) {
					return err
				}
				if !searchNoCache {
					recentMessages, recentErr := mail.SearchRecentMessages(query, account, mailbox, searchLimit, searchSince)
					if recentErr == nil {
						recentMessages = mail.FilterBySender(recentMessages, searchSender, searchSenderDomain)
						if len(recentMessages) > 0 {
							return a.writer.Write(output.Result{
								Data:    recentMessages,
								Summary: fmt.Sprintf("%s from the recent-message journal", plural(len(recentMessages), "match")),
								Notices: []string{fmt.Sprintf("live search failed (%v); showing recently handled messages that match", err)},
								Meta:    map[string]any{"source": "recent", "query": query},
								Plain:   renderMessages(recentMessages, true),
							})
						}
					}
				}
				return fmt.Errorf("search messages: %w", err)
			}
			messages := mail.FilterBySender(result.Messages, searchSender, searchSenderDomain)
			if result.Complete {
				_ = mail.RecordRecentSearchResults(messages, query)
			}
			meta := map[string]any{"source": "index", "query": query, "complete": result.Complete, "searchedMailboxCount": len(result.SearchedMailboxes)}
			if searchAllowPartial {
				result.Messages = messages
				var notices []string
				if !result.Complete {
					notices = append(notices, fmt.Sprintf("%d mailbox(es) could not be completely searched", len(result.FailedMailboxes)))
				}
				return a.writer.Write(output.Result{
					Data:    partialSearchOutputData(result, a.writer.Format),
					Summary: fmt.Sprintf("%s for %q (complete: %v)", plural(len(messages), "match"), query, result.Complete),
					Notices: notices,
					Meta:    meta,
					Plain:   renderMessages(messages, true),
				})
			}
			return a.writer.Write(output.Result{
				Data:    messages,
				Summary: fmt.Sprintf("%s for %q", plural(len(messages), "match"), query),
				Meta:    meta,
				Plain:   renderMessages(messages, true),
			})
		},
	}

	a.searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 50, "Maximum results")
	a.searchCmd.Flags().StringVarP(&searchSince, "since", "s", "", "Only messages since YYYY-MM-DD or 'YYYY-MM-DD HH:MM:SS'")
	a.searchCmd.Flags().StringVar(&searchSender, "sender", "", "Only messages from this exact sender/email")
	a.searchCmd.Flags().StringVar(&searchSenderDomain, "sender-domain", "", "Only messages from this sender domain")
	a.searchCmd.Flags().BoolVar(&searchAllowPartial, "allow-partial", false, "Return partial results with completeness metadata")
	a.searchCmd.Flags().BoolVar(&searchNoCache, "no-cache", false, "Do not fall back to the recent-message journal when live search fails")
}
