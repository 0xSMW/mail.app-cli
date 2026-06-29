package cmd

import (
	"encoding/json"
	"fmt"
)

func printJSON(v any, label string) error {
	output, err := marshalIndentedJSON(v, label)
	if err != nil {
		return err
	}

	fmt.Println(string(output))
	return nil
}

func marshalIndentedJSON(v any, label string) ([]byte, error) {
	output, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s: %w", label, err)
	}
	return output, nil
}

func requireAccountAndMailbox(account, mailbox string) error {
	if account == "" || mailbox == "" {
		return fmt.Errorf("both --account and --mailbox are required")
	}
	return nil
}
