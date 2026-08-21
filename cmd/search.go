package cmd

import (
	"errors"
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	searchLimit        int
	searchAccount      string
	searchMailbox      string
	searchSince        string
	searchSender       string
	searchSenderDomain string
	searchNoCache      bool
	searchAllowPartial bool
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for messages",
	Long: `Search for messages across mailboxes.
The query matches all terms across subject, sender, and indexed message summaries.
By default searches All Mail/Archive for a specific account, or INBOX across accounts.
Use --mailbox with --account to narrow the search to a specific mailbox.
Output is JSON format. Use jq for advanced filtering: mail-app-cli search "query" | jq '.[] | select(.read==false)'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if searchMailbox != "" && searchAccount == "" {
			return fmt.Errorf("--mailbox requires --account")
		}
		query := args[0]
		client := mail.NewClient()
		result, err := client.SearchMessagesJSONSinceWithOptions(query, searchAccount, searchMailbox, searchLimit, searchSince, mail.SearchOptions{
			AllowPartial: searchAllowPartial,
		})
		if err != nil {
			var partialErr *mail.PartialSearchError
			if errors.As(err, &partialErr) {
				return fmt.Errorf("failed to search messages: %w", err)
			}
			if !searchNoCache {
				recentMessages, recentErr := mail.SearchRecentMessages(query, searchAccount, searchMailbox, searchLimit, searchSince)
				if recentErr == nil {
					recentMessages = filterMessagesBySender(recentMessages, searchSender, searchSenderDomain)
					if len(recentMessages) > 0 {
						return printJSON(recentMessages, "recent search results")
					}
				}
			}
			return fmt.Errorf("failed to search messages: %w", err)
		}
		messages := result.Messages
		messages = filterMessagesBySender(messages, searchSender, searchSenderDomain)
		if result.Complete {
			_ = mail.RecordRecentSearchResults(messages, query)
		}
		if searchAllowPartial {
			result.Messages = messages
			return printJSON(result, "structured search results")
		}

		return printJSON(messages, "search results")
	},
}

func init() {
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 50, "Maximum number of results")
	searchCmd.Flags().StringVarP(&searchAccount, "account", "a", "", "Limit search to specific account (optional)")
	searchCmd.Flags().StringVarP(&searchMailbox, "mailbox", "m", "", "Limit search to specific mailbox (optional, requires --account)")
	searchCmd.Flags().StringVarP(&searchSince, "since", "s", "", "Only messages since date (YYYY-MM-DD or YYYY-MM-DD HH:MM:SS)")
	searchCmd.Flags().StringVar(&searchSender, "sender", "", "Only return messages from this exact sender/email")
	searchCmd.Flags().StringVar(&searchSenderDomain, "sender-domain", "", "Only return messages from this sender domain")
	searchCmd.Flags().BoolVar(&searchAllowPartial, "allow-partial", false, "Return partial cross-mailbox results with completeness metadata")
	searchCmd.Flags().BoolVar(&searchNoCache, "no-cache", false, "Accepted for compatibility; search results are not cached")
}
