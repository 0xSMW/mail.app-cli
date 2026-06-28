package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	ruleDryRun     bool
	ruleAccount    string
	ruleMailbox    string
	ruleQuery      string
	ruleLimit      int
	ruleFromDomain string
	ruleMoveTo     string
	ruleEnabled    bool
)

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "List and manage Mail.app rules",
}

var rulesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Mail.app rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		rules, err := client.ListRules()
		if err != nil {
			return fmt.Errorf("failed to list rules: %w", err)
		}
		return printJSON(rules, "rules")
	},
}

var rulesShowCmd = &cobra.Command{
	Use:   "show [rule-name]",
	Short: "Show a Mail.app rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		rules, err := client.ListRules()
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if rule.Name == args[0] {
				return printJSON(rule, "rule")
			}
		}
		return fmt.Errorf("rule not found: %s", args[0])
	},
}

var rulesEnableCmd = &cobra.Command{
	Use:   "enable [rule-name]",
	Short: "Enable a Mail.app rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setRuleEnabled(args[0], true)
	},
}

var rulesDisableCmd = &cobra.Command{
	Use:   "disable [rule-name]",
	Short: "Disable a Mail.app rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setRuleEnabled(args[0], false)
	},
}

var rulesDeleteCmd = &cobra.Command{
	Use:   "delete [rule-name]",
	Short: "Delete a Mail.app rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if ruleDryRun {
			return printJSON(map[string]any{"dryRun": true, "action": "delete", "rule": args[0]}, "rule delete dry-run")
		}
		client := mail.NewClient()
		if err := client.DeleteRule(args[0]); err != nil {
			return err
		}
		return printJSON(map[string]any{"rule": args[0], "deleted": true}, "rule delete result")
	},
}

var rulesCreateCmd = &cobra.Command{
	Use:   "create [rule-name]",
	Short: "Create a Mail.app rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if ruleMoveTo == "" {
			return fmt.Errorf("--move-to is required")
		}
		if ruleFromDomain == "" {
			return fmt.Errorf("--from-domain is required")
		}
		input := mail.RuleInput{
			Name:       args[0],
			Account:    ruleAccount,
			FromDomain: ruleFromDomain,
			MoveTo:     ruleMoveTo,
			Enabled:    ruleEnabled,
		}
		if ruleDryRun {
			return printJSON(map[string]any{
				"dryRun": true,
				"rule":   input,
			}, "rule create dry-run")
		}
		client := mail.NewClient()
		rule, err := client.CreateRule(input)
		if err != nil {
			return fmt.Errorf("failed to create rule: %w", err)
		}
		return printJSON(rule, "rule")
	},
}

var rulesApplyCmd = &cobra.Command{
	Use:   "apply [rule-name]",
	Short: "Preview rule application with existing message selectors",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAccountAndMailbox(ruleAccount, ruleMailbox); err != nil {
			return err
		}
		if ruleQuery == "" {
			return fmt.Errorf("--query is required for rule apply preview")
		}
		client := mail.NewClient()
		messages, err := client.SearchMessagesJSON(ruleQuery, ruleAccount, ruleMailbox, ruleLimit)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{
			"rule":    args[0],
			"dryRun":  true,
			"matched": len(messages),
			"items":   messages,
		}, "rule apply result")
	},
}

func setRuleEnabled(name string, enabled bool) error {
	if ruleDryRun {
		return printJSON(map[string]any{"dryRun": true, "rule": name, "enabled": enabled}, "rule mutation dry-run")
	}
	client := mail.NewClient()
	if err := client.SetRuleEnabled(name, enabled); err != nil {
		return err
	}
	return printJSON(map[string]any{"rule": name, "enabled": enabled}, "rule mutation result")
}

func init() {
	rulesCmd.AddCommand(rulesListCmd, rulesShowCmd, rulesCreateCmd, rulesEnableCmd, rulesDisableCmd, rulesDeleteCmd, rulesApplyCmd)
	for _, cmd := range []*cobra.Command{rulesCreateCmd, rulesEnableCmd, rulesDisableCmd, rulesDeleteCmd, rulesApplyCmd} {
		cmd.Flags().BoolVar(&ruleDryRun, "dry-run", false, "Show mutation without applying it")
	}
	rulesApplyCmd.Flags().StringVarP(&ruleAccount, "account", "a", "", "Account name")
	rulesApplyCmd.Flags().StringVarP(&ruleMailbox, "mailbox", "m", "", "Mailbox name")
	rulesApplyCmd.Flags().StringVar(&ruleQuery, "query", "", "Selector query for apply preview")
	rulesApplyCmd.Flags().IntVarP(&ruleLimit, "limit", "l", 100, "Maximum matched messages")
	rulesCreateCmd.Flags().StringVarP(&ruleAccount, "account", "a", "", "Account containing target mailbox")
	rulesCreateCmd.Flags().StringVar(&ruleFromDomain, "from-domain", "", "Sender domain condition")
	rulesCreateCmd.Flags().StringVar(&ruleMoveTo, "move-to", "", "Target mailbox action")
	rulesCreateCmd.Flags().BoolVar(&ruleEnabled, "enabled", true, "Create rule as enabled")
}
