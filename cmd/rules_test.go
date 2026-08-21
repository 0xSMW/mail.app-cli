package cmd

import (
	"strings"
	"testing"
)

func TestRulesCreateDryRunUsesLiveInputValidation(t *testing.T) {
	previousDryRun := ruleDryRun
	previousAccount := ruleAccount
	previousFromDomain := ruleFromDomain
	previousMoveTo := ruleMoveTo
	previousSubjects := ruleSubjects
	previousMarkRead := ruleMarkRead
	previousEnabled := ruleEnabled
	t.Cleanup(func() {
		ruleDryRun = previousDryRun
		ruleAccount = previousAccount
		ruleFromDomain = previousFromDomain
		ruleMoveTo = previousMoveTo
		ruleSubjects = previousSubjects
		ruleMarkRead = previousMarkRead
		ruleEnabled = previousEnabled
	})

	ruleDryRun = true
	ruleAccount = "Gmail"
	ruleFromDomain = "stripe.com"
	ruleMoveTo = "Receipts"
	ruleSubjects = []string{""}
	ruleMarkRead = false
	ruleEnabled = true

	err := rulesCreateCmd.RunE(rulesCreateCmd, []string{"Receipts"})
	if err == nil || !strings.Contains(err.Error(), "subject contains value is required") {
		t.Fatalf("rules create --dry-run error = %v, want empty subject validation error", err)
	}
}
