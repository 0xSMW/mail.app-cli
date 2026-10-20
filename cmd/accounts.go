package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

func (a *application) buildAccountsCommands() {
	var (
		accountsNoCache      bool
		accountsForceRefresh bool
	)

	a.accountsCmd = &cobra.Command{
		Use:   "accounts",
		Short: "List Mail.app accounts",
	}
	a.accountsListCmd = &cobra.Command{
		Use:   "list",
		Short: "List all accounts",
		Annotations: map[string]string{
			annotationAgentNotes: "Cached for 24h; pass --no-cache for a live read. 'name' is what --account expects.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, err := a.accountsCached(accountsNoCache, accountsForceRefresh)
			if err != nil {
				return fmt.Errorf("get accounts: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    accounts,
				Summary: plural(len(accounts), "account"),
				Plain:   renderAccounts(accounts),
			})
		},
	}
	accountsShowCmd := &cobra.Command{
		Use:   "show <account-name>",
		Short: "Show one account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {

			client := mail.NewClient().WithContext(cmd.Context())
			if err := client.SetBackend(a.flagBackend); err != nil {
				return clierr.Usage(err.Error())
			}
			accounts, err := client.GetAccountsJSON()
			if err != nil {
				return fmt.Errorf("get accounts: %w", err)
			}
			for _, acc := range accounts {
				if acc.Name == args[0] {
					return a.writer.Write(output.Result{
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

	a.accountsCmd.AddCommand(a.accountsListCmd, accountsShowCmd)
	registerCacheFlags(a.accountsListCmd, &accountsNoCache, &accountsForceRefresh)
}
