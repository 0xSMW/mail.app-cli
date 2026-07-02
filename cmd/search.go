package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	searchLimit   int
	searchAccount string
	searchMailbox string
	searchSince   string
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
		query := args[0]
		client := mail.NewClient()
		messages, err := client.SearchMessagesJSONSince(query, searchAccount, searchMailbox, searchLimit, searchSince)
		if err != nil {
			return fmt.Errorf("failed to search messages: %w", err)
		}

		return printJSON(messages, "search results")
	},
}

func init() {
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 50, "Maximum number of results")
	searchCmd.Flags().StringVarP(&searchAccount, "account", "a", "", "Limit search to specific account (optional)")
	searchCmd.Flags().StringVarP(&searchMailbox, "mailbox", "m", "", "Limit search to specific mailbox (optional, requires --account)")
	searchCmd.Flags().StringVarP(&searchSince, "since", "s", "", "Only messages since date (YYYY-MM-DD or YYYY-MM-DD HH:MM:SS)")
}
