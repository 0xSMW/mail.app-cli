package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/robertmeta/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	mailboxAccount string
)

var mailboxesCmd = &cobra.Command{
	Use:   "mailboxes",
	Short: "Manage Mail.app mailboxes",
	Long:  `View and manage your Mail.app mailboxes.`,
}

var mailboxesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all mailboxes",
	Long:  `List all mailboxes across all accounts or for a specific account.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		mailboxes, err := client.GetMailboxesJSON(mailboxAccount)
		if err != nil {
			return fmt.Errorf("failed to get mailboxes: %w", err)
		}

		if len(mailboxes) == 0 {
			fmt.Println("No mailboxes found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ACCOUNT\tMAILBOX\tUNREAD\tTOTAL")
		fmt.Fprintln(w, "-------\t-------\t------\t-----")
		for _, mbox := range mailboxes {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", mbox.Account, mbox.Name, mbox.UnreadCount, mbox.TotalCount)
		}
		w.Flush()

		return nil
	},
}

func init() {
	mailboxesCmd.AddCommand(mailboxesListCmd)
	mailboxesListCmd.Flags().StringVarP(&mailboxAccount, "account", "a", "", "Filter by account name")
}
