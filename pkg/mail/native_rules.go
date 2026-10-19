package mail

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func (n *nativeMail) outgoingRuleRefs(name string) ([]ae.Value, error) {
	v, e := n.get(ae.All("rule", ae.Null()))
	if e != nil {
		return nil, e
	}
	refs, e := nativeList(v, "rules")
	if e != nil {
		return nil, e
	}
	if name == "" {
		return refs, nil
	}
	matches := []ae.Value{}
	for _, ref := range refs {
		actual, e := n.stringProperty("pnam", ref)
		if e != nil {
			return nil, e
		}
		if actual == name {
			matches = append(matches, ref)
		}
	}
	return matches, nil
}
func (n *nativeMail) outgoingRule(name string) (ae.Value, error) {
	refs, e := n.outgoingRuleRefs(name)
	if e != nil {
		return ae.Value{}, e
	}
	if len(refs) == 0 {
		return ae.Value{}, notFound("rule", name)
	}
	if len(refs) != 1 {
		return ae.Value{}, fmt.Errorf("rule name is ambiguous: %s", name)
	}
	return refs[0], nil
}
func nativeRuleEnum(code string) string {
	switch code {
	case "tacc":
		return "account"
	case "tanr":
		return "any recipient"
	case "tccc":
		return "cc header"
	case "tevm":
		return "matches every message"
	case "tfro":
		return "from header"
	case "thdk":
		return "header key"
	case "tmec":
		return "message content"
	case "tmij":
		return "message is junk mail"
	case "tsii":
		return "sender is in my contacts"
	case "tsah":
		return "sender is in my previous recipients"
	case "tsim":
		return "sender is member of group"
	case "tsin":
		return "sender is not in my contacts"
	case "tnah":
		return "sender is not in my previous recipients"
	case "tsub":
		return "subject header"
	case "ttoo":
		return "to header"
	case "ttoc":
		return "to or cc header"
	case "tatt":
		return "attachment type"
	case "rqbw":
		return "begins with value"
	case "rqco":
		return "does contain value"
	case "rqdn":
		return "does not contain value"
	case "rqew":
		return "ends with value"
	case "rqie":
		return "equal to value"
	case "rqlt":
		return "less than value"
	case "rqgt":
		return "greater than value"
	case "rqno":
		return "none"
	default:
		// Mail.sdef assigns tsig to two different RuleType names. Preserve
		// that ambiguous code, as well as codes introduced by newer Mail.
		return code
	}
}

// Mail supports homogeneous property projections over an element collection.
// Do not send an arbitrary list of object specifiers as a getter: Mail rejects it.
func (n *nativeMail) ruleColumns(collection ae.Value, count int, label, phase string, codes ...string) (map[string][]ae.Value, error) {
	columns := make(map[string][]ae.Value, len(codes))
	if count == 0 {
		return columns, nil
	}
	for _, code := range codes {
		var value ae.Value
		err := n.phase(phase+"_"+code, func() error {
			var err error
			value, err = n.get(ae.Property(code, collection))
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("read %s property %s: %w", label, code, err)
		}
		if value.Kind != "list" || len(value.Items) != count {
			return nil, fmt.Errorf("read %s property %s: expected %d column values (descriptor kind=%q count=%d)", label, code, count, value.Kind, len(value.Items))
		}
		columns[code] = value.Items
	}
	return columns, nil
}

func (c *Client) listNativeRules() ([]Rule, error) {
	out := []Rule{}
	err := c.withNativeAutomation(0, func(n *nativeMail) error {
		var refs []ae.Value
		e := n.phase("rules_discovery", func() error {
			var err error
			refs, err = n.outgoingRuleRefs("")
			return err
		})
		if e != nil {
			return e
		}
		codes := []string{"pnam", "isac", "rstm", "rdme", "rmre", "rmfl"}
		columns, e := n.ruleColumns(ae.All("rule", ae.Null()), len(refs), "rules", "rules_columns", codes...)
		if e != nil {
			return e
		}
		for ruleIndex, ref := range refs {
			label := fmt.Sprintf("rule %d", ruleIndex+1)
			f := make(map[string]ae.Value, len(codes))
			for _, code := range codes {
				f[code] = columns[code][ruleIndex]
			}
			name, e := nativeString(f["pnam"], "rule name")
			if e != nil {
				return e
			}
			for _, code := range []string{"isac", "rstm", "rdme", "rmre", "rmfl"} {
				if f[code].Kind != "bool" {
					return fmt.Errorf("rule property %s unavailable", code)
				}
			}
			rule := Rule{Name: name, Enabled: f["isac"].Boolean}
			var conditionList ae.Value
			e = n.phase(fmt.Sprintf("rules_%d_conditions", ruleIndex+1), func() error {
				var err error
				conditionList, err = n.get(ae.All("rucr", ref))
				return err
			})
			if e != nil {
				return fmt.Errorf("read %s conditions: %w", label, e)
			}
			conditions, e := nativeList(conditionList, "rule conditions")
			if e != nil {
				return e
			}
			conditionColumns, e := n.ruleColumns(ae.All("rucr", ref), len(conditions), label+" conditions", fmt.Sprintf("rules_%d_condition_columns", ruleIndex+1), "rtyp", "rqua", "rexp")
			if e != nil {
				return e
			}
			for conditionIndex := range conditions {
				conditionLabel := fmt.Sprintf("%s condition %d", label, conditionIndex+1)
				cf := map[string]ae.Value{"rtyp": conditionColumns["rtyp"][conditionIndex], "rqua": conditionColumns["rqua"][conditionIndex], "rexp": conditionColumns["rexp"][conditionIndex]}
				for _, code := range []string{"rtyp", "rqua"} {
					if cf[code].Kind != "enum" || len(cf[code].Code) != 4 {
						return fmt.Errorf("read %s property %s: expected enumerated value (descriptor kind=%q code=%q)", conditionLabel, code, cf[code].Kind, cf[code].Code)
					}
				}
				expr, e := nativeString(cf["rexp"], "rule expression")
				if e != nil {
					return e
				}
				parts := []string{nativeRuleEnum(cf["rtyp"].Code), nativeRuleEnum(cf["rqua"].Code), expr}
				rule.Conditions = append(rule.Conditions, strings.TrimSpace(strings.Join(parts, " ")))
			}
			if f["rstm"].Boolean {
				box, e := n.get(ae.Property("rtme", ref))
				if e != nil {
					return fmt.Errorf("read %s move destination property rtme: %w", label, e)
				}
				// Mail can retain a move action after its destination disappears.
				// Its public missing-value descriptor is an observation to expose;
				// failed getters and malformed destination values remain errors.
				if box.Kind == "type" && box.Code == "msng" {
					rule.Actions = append(rule.Actions, "move to unavailable mailbox")
				} else {
					if !nativeReference(box) {
						return fmt.Errorf("read %s move destination property rtme: expected mailbox reference (descriptor kind=%q code=%q)", label, box.Kind, box.Code)
					}
					name, e := n.stringProperty("pnam", box)
					if e != nil {
						return fmt.Errorf("read %s move destination property pnam (descriptor kind=%q code=%q): %w", label, box.Kind, box.Code, e)
					}
					rule.Actions = append(rule.Actions, "move to "+name)
				}
			}
			if f["rdme"].Boolean {
				rule.Actions = append(rule.Actions, "delete")
			}
			if f["rmre"].Boolean {
				rule.Actions = append(rule.Actions, "mark read")
			}
			if f["rmfl"].Boolean {
				rule.Actions = append(rule.Actions, "mark flagged")
			}
			out = append(out, rule)
		}
		return nil
	})
	return out, err
}
func (n *nativeMail) outgoingRuleDestination(input RuleInput) (ae.Value, error) {
	if input.Account != "" {
		account, e := n.account(input.Account)
		if e != nil {
			return ae.Value{}, e
		}
		return n.outgoingRuleMailbox(account, input.MoveTo)
	}
	accounts, e := n.outgoingAccounts()
	if e != nil {
		return ae.Value{}, e
	}
	var found ae.Value
	count := 0
	for _, account := range accounts {
		box, e := n.outgoingRuleMailbox(account, input.MoveTo)
		if errors.Is(e, ErrNotFound) {
			continue
		}
		if e != nil {
			return ae.Value{}, e
		}
		found = box
		count++
	}
	if count == 0 {
		return ae.Value{}, notFound("target mailbox", input.MoveTo)
	}
	if count != 1 {
		return ae.Value{}, fmt.Errorf("target mailbox is ambiguous; specify an account")
	}
	return found, nil
}
func (n *nativeMail) outgoingRuleMailbox(account ae.Value, name string) (ae.Value, error) {
	if name == "Archive" || name == "All Mail" {
		box, e := n.mailbox(account, "All Mail")
		if e == nil {
			return box, nil
		}
		if !errors.Is(e, ErrNotFound) {
			return ae.Value{}, e
		}
		return n.mailbox(account, "Archive")
	}
	return n.mailbox(account, name)
}
func (c *Client) createNativeRule(input RuleInput) (*Rule, error) {
	var owned ae.Value
	cleanup := ""
	err := c.withNativeAutomation(0, func(n *nativeMail) (err error) {
		defer func() {
			if err != nil && (owned.Kind == "object" || owned.Kind == "opaque") {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(n.ctx), 5*time.Second)
				defer cancel()
				clean := *n
				clean.ctx = ctx
				_, e := clean.command("core", "delo", map[string]ae.Value{"----": owned})
				if e != nil {
					cleanup = e.Error()
					if disable := clean.set(ae.Property("isac", owned), ae.Bool(false)); disable != nil {
						cleanup += "; disable failed: " + disable.Error()
					}
				}
			}
		}()
		existing, e := n.outgoingRuleRefs(input.Name)
		if e != nil {
			return e
		}
		if len(existing) != 0 {
			return fmt.Errorf("rule name already exists: %s", input.Name)
		}
		box, e := n.outgoingRuleDestination(input)
		if e != nil {
			return e
		}
		owned, e = n.command("core", "crel", map[string]ae.Value{"kocl": ae.TypeCode("rule"), "prdt": ae.Record(map[string]ae.Value{"pnam": ae.String(input.Name), "isac": ae.Bool(false)})})
		if e != nil {
			var transport *ae.Error
			if errors.As(e, &transport) && transport.Attempted && !nativeReference(owned) {
				cleanup = "rule creation may have completed; owned object descriptor unavailable"
			}
			return e
		}
		if !nativeReference(owned) {
			cleanup = "rule creation receipt did not identify an owned object"
			return fmt.Errorf("Mail did not return an owned rule")
		}
		for _, property := range []struct {
			code  string
			value ae.Value
		}{{"racm", ae.Bool(true)}, {"rstm", ae.Bool(true)}, {"rtme", box}, {"rmre", ae.Bool(input.MarkRead)}} {
			if e = n.set(ae.Property(property.code, owned), property.value); e != nil {
				return e
			}
		}
		conditions := []struct{ kind, value string }{{"tfro", input.FromDomain}}
		for _, subject := range input.SubjectContains {
			conditions = append(conditions, struct{ kind, value string }{"tsub", subject})
		}
		for _, condition := range conditions {
			_, e = n.command("core", "crel", map[string]ae.Value{"kocl": ae.TypeCode("rucr"), "insh": owned, "prdt": ae.Record(map[string]ae.Value{"rtyp": ae.Enum(condition.kind), "rqua": ae.Enum("rqco"), "rexp": ae.String(condition.value)})})
			if e != nil {
				return e
			}
		}
		fields, e := n.getFields(owned, "pnam", "isac", "racm", "rstm", "rmre", "rtme")
		if e != nil {
			return e
		}
		if fields["pnam"].Kind != "string" || fields["pnam"].Text != input.Name {
			return fmt.Errorf("rule name readback mismatch")
		}
		for code, expected := range map[string]bool{"isac": false, "racm": true, "rstm": true, "rmre": input.MarkRead} {
			if fields[code].Kind != "bool" || fields[code].Boolean != expected {
				return fmt.Errorf("rule property %s readback mismatch", code)
			}
		}
		destinationName, e := n.stringProperty("pnam", fields["rtme"])
		if e != nil {
			return e
		}
		expectedDestinationName, e := n.stringProperty("pnam", box)
		if e != nil {
			return e
		}
		if destinationName != expectedDestinationName {
			return fmt.Errorf("rule destination readback mismatch")
		}
		actualOwner, e := n.get(ae.Property("mact", fields["rtme"]))
		if e != nil {
			return e
		}
		expectedOwner, e := n.get(ae.Property("mact", box))
		if e != nil {
			return e
		}
		actualOwnerID, e := n.stringProperty("ID  ", actualOwner)
		if e != nil {
			return e
		}
		expectedOwnerID, e := n.stringProperty("ID  ", expectedOwner)
		if e != nil {
			return e
		}
		if actualOwnerID == "" || actualOwnerID != expectedOwnerID {
			return fmt.Errorf("rule destination account readback mismatch")
		}
		conditionValues, e := n.get(ae.All("rucr", owned))
		if e != nil {
			return e
		}
		actualConditions, e := nativeList(conditionValues, "created rule conditions")
		if e != nil {
			return e
		}
		if len(actualConditions) != len(conditions) {
			return fmt.Errorf("rule condition count readback mismatch")
		}
		for i, actual := range actualConditions {
			fields, e := n.getFields(actual, "rtyp", "rqua", "rexp")
			if e != nil {
				return e
			}
			if fields["rtyp"].Kind != "enum" || fields["rtyp"].Code != conditions[i].kind || fields["rqua"].Kind != "enum" || fields["rqua"].Code != "rqco" || fields["rexp"].Kind != "string" || fields["rexp"].Text != conditions[i].value {
				return fmt.Errorf("rule condition %d readback mismatch", i+1)
			}
		}

		refs, e := n.outgoingRuleRefs(input.Name)
		if e != nil {
			return e
		}
		if len(refs) != 1 {
			return fmt.Errorf("created rule name is ambiguous")
		}
		if e = n.set(ae.Property("isac", owned), ae.Bool(input.Enabled)); e != nil {
			return e
		}
		enabled, e := n.get(ae.Property("isac", owned))
		if e != nil {
			return e
		}
		if enabled.Kind != "bool" || enabled.Boolean != input.Enabled {
			return fmt.Errorf("rule enabled readback mismatch")
		}
		return nil
	})
	if err != nil {
		if cleanup != "" {
			return nil, &MutationError{Operation: "rule_create", Phase: "configure", Cause: err, Cleanup: cleanup, Attempted: true, Uncertain: true}
		}
		return nil, err
	}
	return ruleFromInput(input), nil
}
func (c *Client) changeNativeRule(name string, enabled *bool) error {
	if name == "" {
		return fmt.Errorf("rule name is required")
	}
	return c.withNativeAutomation(0, func(n *nativeMail) error {
		ref, e := n.outgoingRule(name)
		if e != nil {
			return e
		}
		if enabled == nil {
			_, e = n.command("core", "delo", map[string]ae.Value{"----": ref})
			return e
		}
		if e = n.set(ae.Property("isac", ref), ae.Bool(*enabled)); e != nil {
			return e
		}
		v, e := n.get(ae.Property("isac", ref))
		if e != nil {
			return e
		}
		if v.Kind != "bool" || v.Boolean != *enabled {
			return fmt.Errorf("rule enabled readback mismatch")
		}
		return nil
	})
}
func (c *Client) listNativeSignatures(includeContent bool) ([]Signature, error) {
	out := []Signature{}
	err := c.withNativeAutomation(0, func(n *nativeMail) error {
		v, e := n.get(ae.All("situ", ae.Null()))
		if e != nil {
			return e
		}
		refs, e := nativeList(v, "signatures")
		if e != nil {
			return e
		}
		for _, ref := range refs {
			codes := []string{"pnam"}
			if includeContent {
				codes = append(codes, "ctnt")
			}
			f, e := n.getFields(ref, codes...)
			if e != nil {
				return e
			}
			name, e := nativeString(f["pnam"], "signature name")
			if e != nil {
				return e
			}
			signature := Signature{Name: name}
			if includeContent {
				signature.Content, e = nativeString(f["ctnt"], "signature content")
				if e != nil {
					return e
				}
			}
			out = append(out, signature)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}
