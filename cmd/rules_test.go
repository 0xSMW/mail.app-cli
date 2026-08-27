package cmd

import (
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

func TestRulesCreateRejectsEmptySubjectBeforeDryRun(t *testing.T) {
	code, _, stderr := run(t, "rules", "create", "Receipts", "-a", "Gmail", "--from-domain", "stripe.com",
		"--move-to", "Receipts", "--subject-contains", "", "--dry-run", "--json")
	if code != 1 || !strings.Contains(stderr, "subject contains value is required") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if err := mail.ValidateRuleInput(mail.RuleInput{Name: "x", MoveTo: "y", FromDomain: "z"}); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}
