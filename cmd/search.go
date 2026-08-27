package cmd

import (
	"errors"
	"fmt"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	searchLimit        int
	searchSince        string
	searchSender       string
	searchSenderDomain string
	searchNoCache      bool
	searchAllowPartial bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search messages by subject, sender, and indexed summary",
	Long: `Search messages. Every term must match somewhere in the subject, sender,
or Mail's indexed summary. Without --mailbox the search covers every
non-empty mailbox of each enabled account (or of the one named with
--account); with --mailbox it is limited to that mailbox.`,
	Args: cobra.ExactArgs(1),
	Annotations: map[string]string{
		annotationAgentNotes: "Fails closed with exit 5 when any mailbox could not be searched; add --allow-partial to accept incomplete results, which then arrive as {messages, complete, searchedMailboxes, failedMailboxes}. Needs Envelope Index access (Full Disk Access) for cross-mailbox queries.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		account := resolved.Account.Value
		mailbox := ""
		if mailboxExplicit() {
			if account == "" {
				return clierr.Usage("--mailbox requires --account")
			}
			mailbox = mailboxInScope()
		}
		query := args[0]
		result, err := mailClient.SearchMessagesJSONSinceWithOptions(query, account, mailbox, searchLimit, searchSince, mail.SearchOptions{
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
						return writer.Write(output.Result{
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
				notices = append(notices, fmt.Sprintf("%d mailbox(es) could not be searched", len(result.FailedMailboxes)))
			}
			return writer.Write(output.Result{
				Data:    partialSearchOutputData(result, writer.Format),
				Summary: fmt.Sprintf("%s for %q (complete: %v)", plural(len(messages), "match"), query, result.Complete),
				Notices: notices,
				Meta:    meta,
				Plain:   renderMessages(messages, true),
			})
		}
		return writer.Write(output.Result{
			Data:    messages,
			Summary: fmt.Sprintf("%s for %q", plural(len(messages), "match"), query),
			Meta:    meta,
			Plain:   renderMessages(messages, true),
		})
	},
}

// partialSearchOutputData preserves completeness metadata for regular search
// output while allowing list modifiers to operate on the matched messages.
func partialSearchOutputData(result mail.SearchResult, format output.Format) any {
	if format == output.FormatCount || format == output.FormatIDs {
		return result.Messages
	}
	return result
}

func init() {
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 50, "Maximum results")
	searchCmd.Flags().StringVarP(&searchSince, "since", "s", "", "Only messages since YYYY-MM-DD or 'YYYY-MM-DD HH:MM:SS'")
	searchCmd.Flags().StringVar(&searchSender, "sender", "", "Only messages from this exact sender/email")
	searchCmd.Flags().StringVar(&searchSenderDomain, "sender-domain", "", "Only messages from this sender domain")
	searchCmd.Flags().BoolVar(&searchAllowPartial, "allow-partial", false, "Return partial cross-mailbox results with completeness metadata")
	searchCmd.Flags().BoolVar(&searchNoCache, "no-cache", false, "Do not fall back to the recent-message journal when live search fails")
}
