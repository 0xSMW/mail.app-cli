package cmd

import (
	"fmt"

	"github.com/intelligrit/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	searchLimit   int
	searchAccount string
	searchMailbox string
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for messages",
	Long: `Search for messages across mailboxes.
The query will search in subject and sender (content search disabled for performance).
By default searches INBOX of all accounts. Use --account and --mailbox to narrow the search.
Output is JSON format. Use jq for advanced filtering: mail-app-cli search "query" | jq '.[] | select(.read==false)'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		client := mail.NewClient()
		messages, err := client.SearchMessagesJSON(query, searchAccount, searchMailbox, searchLimit)
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
}
