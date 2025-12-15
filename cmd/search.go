package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/robertmeta/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	searchLimit int
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for messages",
	Long: `Search for messages across all mailboxes.
The query will search in subject, sender, and content.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		client := mail.NewClient()
		messages, err := client.SearchMessagesJSON(query, searchLimit)
		if err != nil {
			return fmt.Errorf("failed to search messages: %w", err)
		}

		if len(messages) == 0 {
			fmt.Println("No messages found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ACCOUNT\tMAILBOX\tSUBJECT\tFROM\tDATE")
		fmt.Fprintln(w, "-------\t-------\t-------\t----\t----")
		for _, msg := range messages {
			subject := msg.Subject
			if len(subject) > 40 {
				subject = subject[:37] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", msg.Account, msg.Mailbox, subject, msg.Sender, msg.DateReceived)
		}
		w.Flush()

		return nil
	},
}

func init() {
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 50, "Maximum number of results")
}
