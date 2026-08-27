package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	accountsNoCache      bool
	accountsForceRefresh bool
)

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "List Mail.app accounts",
}

var accountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all accounts",
	Annotations: map[string]string{
		annotationAgentNotes: "Cached for 24h; pass --no-cache for a live read. 'name' is what --account expects.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		accounts, err := accountsCached(accountsNoCache, accountsForceRefresh)
		if err != nil {
			return fmt.Errorf("get accounts: %w", err)
		}
		return writer.Write(output.Result{
			Data:    accounts,
			Summary: plural(len(accounts), "account"),
			Plain:   renderAccounts(accounts),
		})
	},
}

var accountsShowCmd = &cobra.Command{
	Use:   "show <account-name>",
	Short: "Show one account",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Details and not-found responses must reflect Mail.app now. The
		// account-list cache is intentionally long-lived, so use a separate
		// client to bypass both its on-disk entry and any in-process cache
		// primed from it.
		accounts, err := mail.NewClient().GetAccountsJSON()
		if err != nil {
			return fmt.Errorf("get accounts: %w", err)
		}
		for _, acc := range accounts {
			if acc.Name == args[0] {
				return writer.Write(output.Result{
					Data:    acc,
					Summary: acc.Name + " <" + acc.EmailAddress + ">",
					Plain: renderKeyValueList([][2]string{
						{"Name", acc.Name}, {"Email", acc.EmailAddress}, {"Type", acc.AccountType},
						{"User", acc.UserName}, {"Enabled", fmt.Sprint(acc.Enabled)}, {"ID", acc.ID},
					}),
				})
			}
		}
		return clierr.New(clierr.CodeNotFound, "account not found: "+args[0]).WithHint("run 'mail-app-cli accounts list' for names")
	},
}

func init() {
	accountsCmd.AddCommand(accountsListCmd, accountsShowCmd)
	accountsListCmd.Flags().BoolVar(&accountsNoCache, "no-cache", false, "Bypass the cache and read from Mail.app")
	accountsListCmd.Flags().BoolVar(&accountsForceRefresh, "force-refresh", false, "Refresh the cache from Mail.app")
}
