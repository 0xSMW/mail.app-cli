package mail

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCreateRuleScriptCombinesSenderAndSubjectConditions(t *testing.T) {
	input := RuleInput{
		Name:            `Clearance "publishes"`,
		Account:         "Klu.ai",
		FromDomain:      "support@npmjs.com",
		MoveTo:          "Trash",
		Enabled:         true,
		SubjectContains: []string{"Successfully published @clearance/", `package "ready"`},
		MarkRead:        true,
	}
	script := createRuleScript(input)

	for _, want := range []string{
		`if name of acc is not "Klu.ai" then set shouldInspect to false`,
		`name:"Clearance \"publishes\""`,
		`set all conditions must be met to true`,
		`set mark read to true`,
		`set enabled of newRule to true`,
		`set enabled of newRule to false`,
		`rule type:from header, qualifier:does contain value, expression:"support@npmjs.com"`,
		`rule type:subject header, qualifier:does contain value, expression:"Successfully published @clearance/"`,
		`rule type:subject header, qualifier:does contain value, expression:"package \"ready\""`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("createRuleScript missing %q", want)
		}
	}
	if got := strings.Count(script, "rule type:subject header"); got != 2 {
		t.Fatalf("subject condition count = %d, want 2", got)
	}
}

func TestCreateRuleScriptOmitsMarkReadByDefault(t *testing.T) {
	script := createRuleScript(RuleInput{
		Name:       "Receipts",
		FromDomain: "stripe.com",
		MoveTo:     "Receipts",
		Enabled:    true,
	})
	if strings.Contains(script, "set mark read to true") {
		t.Fatal("createRuleScript added mark-read action without MarkRead")
	}
}

func TestCreateRuleScriptDeletesIncompleteRule(t *testing.T) {
	script := createRuleScript(RuleInput{
		Name:       "Receipts",
		FromDomain: "stripe.com",
		MoveTo:     "Receipts",
		Enabled:    true,
	})

	createAt := strings.Index(script, `make new rule at end of rules with properties {name:"Receipts", enabled:false}`)
	enableAt := strings.Index(script, `if true then set enabled of newRule to true`)
	deleteAt := strings.Index(script, `delete newRule`)
	disableFallbackAt := strings.Index(script, `set enabled of newRule to false`)
	if createAt < 0 || enableAt < 0 || deleteAt < 0 || disableFallbackAt < 0 {
		t.Fatalf("create rule script is missing disabled creation, final enable, deletion rollback, or disable fallback:\n%s", script)
	}
	if createAt > enableAt {
		t.Fatal("rule is enabled before it is created")
	}
	if deleteAt < enableAt || disableFallbackAt < deleteAt {
		t.Fatal("failure rollback must delete the partial rule and disable it only if deletion fails")
	}
}

func TestValidateRuleInputRejectsEmptySubjectContains(t *testing.T) {
	err := ValidateRuleInput(RuleInput{
		Name:            "Receipts",
		FromDomain:      "stripe.com",
		MoveTo:          "Receipts",
		SubjectContains: []string{""},
	})
	if err == nil || err.Error() != "subject contains value is required" {
		t.Fatalf("ValidateRuleInput() error = %v, want empty subject error", err)
	}
}

func TestRuleFromInputIncludesNewConditionsAndActions(t *testing.T) {
	rule := ruleFromInput(RuleInput{
		Name:            "Weekly digest",
		FromDomain:      "communications@ramp.com",
		MoveTo:          "Trash",
		Enabled:         true,
		SubjectContains: []string{"Your weekly digest ("},
		MarkRead:        true,
	})
	wantConditions := []string{
		"from contains communications@ramp.com",
		"subject contains Your weekly digest (",
	}
	wantActions := []string{"move to Trash", "mark read"}
	if strings.Join(rule.Conditions, "|") != strings.Join(wantConditions, "|") {
		t.Fatalf("conditions = %v, want %v", rule.Conditions, wantConditions)
	}
	if strings.Join(rule.Actions, "|") != strings.Join(wantActions, "|") {
		t.Fatalf("actions = %v, want %v", rule.Actions, wantActions)
	}
}

func TestRuleInputExistingJSONShapeIsUnchanged(t *testing.T) {
	data, err := json.Marshal(RuleInput{
		Name:       "Receipts",
		Account:    "Gmail",
		FromDomain: "stripe.com",
		MoveTo:     "Receipts",
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"Receipts","account":"Gmail","fromDomain":"stripe.com","moveTo":"Receipts","enabled":true}`
	if string(data) != want {
		t.Fatalf("RuleInput JSON = %s, want %s", data, want)
	}
}
