package cmd

import (
	"errors"
	"fmt"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

var smartLimit int

var smartCmd = &cobra.Command{
	Use:   "smart",
	Short: "List Today and query across accounts",
	Long: `List the built-in Today view and query across accounts.

Mail does not expose custom Smart Mailboxes through its public Apple-event
dictionary. Today is calculated from Envelope Index last-viewed timestamps;
if that index is unavailable the command returns an unavailable capability
error rather than a successful empty list.`,
}

func smartCapabilityError(err error) error {
	var capability *mail.CapabilityError
	if !errors.As(err, &capability) {
		return err
	}
	result := clierr.Wrap(clierr.CodeUnavailable, err, capability.Error())
	if capability.Status == mail.CapabilityUnavailable {
		return result.WithHint("grant Full Disk Access to the app launching mail-app-cli, then rerun")
	}
	return result.WithHint("Mail's public automation API does not expose custom Smart Mailboxes")
}

func renderSmart(boxes []mail.SmartMailbox) func(*output.Printer) {
	rows := make([][]string, 0, len(boxes))
	for _, box := range boxes {
		rows = append(rows, []string{box.Name, fmt.Sprint(box.Unread), fmt.Sprint(box.TotalCount)})
	}
	return renderTable([]string{"NAME", "UNREAD", "TOTAL"}, rows, "no smart mailboxes")
}

var smartListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show the supported built-in Today view",
	RunE: func(cmd *cobra.Command, args []string) error {
		boxes, err := mailClient.ListSmartMailboxes()
		if err != nil {
			return smartCapabilityError(err)
		}
		return writer.Write(output.Result{
			Data:    boxes,
			Summary: "Today (built-in view; custom Smart Mailboxes unsupported)",
			Plain:   renderSmart(boxes),
			Meta: map[string]any{
				"scope":                "built_in_today",
				"customSmartMailboxes": "unsupported",
			},
		})
	},
}

var smartShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a smart mailbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		boxes, err := mailClient.ListSmartMailboxes()
		if err != nil {
			return smartCapabilityError(err)
		}
		for _, box := range boxes {
			if box.Name == args[0] {
				return writer.Write(output.Result{Data: box, Summary: "Smart mailbox " + box.Name, Plain: renderSmart([]mail.SmartMailbox{box})})
			}
		}
		return smartCapabilityError(&mail.CapabilityError{
			Capability: "custom Smart Mailbox " + args[0],
			Status:     mail.CapabilityUnsupported,
		})
	},
}

var smartQueryCmd = &cobra.Command{
	Use:   "query <query>",
	Short: "Search across accounts with normal search semantics",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messages, err := mailClient.SearchMessagesJSON(args[0], "", "", smartLimit)
		if err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    messages,
			Summary: fmt.Sprintf("%s for %q", plural(len(messages), "match"), args[0]),
			Plain:   renderMessages(messages, true),
		})
	},
}

func init() {
	smartCmd.AddCommand(smartListCmd, smartShowCmd, smartQueryCmd)
	smartQueryCmd.Flags().IntVarP(&smartLimit, "limit", "l", 50, "Maximum messages")
}
