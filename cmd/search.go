package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/robertmeta/mail-app-cli/pkg/mail"
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
By default searches INBOX of all accounts. Use --account and --mailbox to narrow the search.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		client := mail.NewClient()
		messages, err := client.SearchMessagesJSON(query, searchAccount, searchMailbox, searchLimit)
		if err != nil {
			return fmt.Errorf("failed to search messages: %w", err)
		}

		if len(messages) == 0 {
			fmt.Println("No messages found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tACCOUNT\tMAILBOX\tSUBJECT\tFROM\tDATE")
		fmt.Fprintln(w, "--\t-------\t-------\t-------\t----\t----")
		for _, msg := range messages {
			subject := msg.Subject
			if len(subject) > 40 {
				subject = subject[:37] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", msg.ID, msg.Account, msg.Mailbox, subject, msg.Sender, msg.DateReceived)
		}
		w.Flush()

		return nil
	},
}

func init() {
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 50, "Maximum number of results")
	searchCmd.Flags().StringVarP(&searchAccount, "account", "a", "", "Limit search to specific account (optional)")
	searchCmd.Flags().StringVarP(&searchMailbox, "mailbox", "m", "", "Limit search to specific mailbox (optional, requires --account)")
}
