package mail

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

type rulesDescriptorFixture struct {
	rules                    []ae.Value
	conditions               []ae.Value
	fields                   map[string]map[string]ae.Value
	readErrors               map[string]error
	events                   []ae.Event
	configureErr, cleanupErr error
}

func newRulesDescriptorFixture() *rulesDescriptorFixture {
	return &rulesDescriptorFixture{fields: map[string]map[string]ae.Value{}, readErrors: map[string]error{}}
}
func ruleFixtureKey(v ae.Value) string    { return v.Text }
func ruleFixtureRef(text string) ae.Value { return ae.Value{Kind: "opaque", Text: text} }
func (f *rulesDescriptorFixture) get(v ae.Value) (ae.Value, error) {
	if v.Kind == "list" {
		items := make([]ae.Value, len(v.Items))
		for i, ref := range v.Items {
			value, e := f.get(ref)
			if e != nil {
				return ae.Value{}, e
			}
			items[i] = value
		}
		return ae.List(items...), nil
	}
	class := descriptorClass(v)
	prop := descriptorProperty(v)
	container := v.Fields["from"]
	if class == "rule" {
		return ae.List(f.rules...), nil
	}
	if class == "mact" {
		return ae.List(ruleFixtureRef("account")), nil
	}
	if class == "mbxp" {
		if v.Fields["form"].Code == "indx" && v.Fields["seld"].Kind == "int" && v.Fields["seld"].Number == 1 && container.Text == "account" {
			return ruleFixtureRef("box"), nil
		}
		if container.Text == "account" {
			return ae.List(ruleFixtureRef("box")), nil
		}
		return ae.List(), nil
	}
	if class == "rucr" {
		return ae.List(f.conditions...), nil
	}
	if class == "situ" {
		return ae.List(ruleFixtureRef("signature")), nil
	}
	if prop != "" {
		if err := f.readErrors[container.Text+":"+prop]; err != nil {
			return ae.Value{}, err
		}
		if descriptorClass(container) == "mbxp" {
			selection := container.Fields["seld"]
			parent := container.Fields["from"]
			if selection.Kind == "ordinal" && selection.Code == "all " && prop == "pnam" {
				if parent.Text == "account" {
					return ae.List(ae.String("News")), nil
				}
				if descriptorClass(parent) == "mbxp" && parent.Fields["from"].Text == "account" {
					return ae.List(ae.List()), nil
				}
				return ae.List(), nil
			}
			if selection.Kind == "int" && selection.Number == 1 && parent.Text == "account" {
				switch prop {
				case "pnam":
					return ae.String("News"), nil
				case "mact":
					return ruleFixtureRef("account"), nil
				case "mbxc":
					return ae.Null(), nil
				}
			}
		}
		switch container.Text {
		case "account":
			if prop == "ID  " {
				return ae.String("personal-id"), nil
			}
			if prop == "pnam" {
				return ae.String("Personal"), nil
			}
		case "box":
			if prop == "mbxc" {
				return ae.Null(), nil
			}
			if prop == "mact" {
				return ruleFixtureRef("account"), nil
			}
			if prop == "pnam" {
				return ae.String("News"), nil
			}
		case "signature":
			if prop == "pnam" {
				return ae.String("世界"), nil
			}
			if prop == "ctnt" {
				return ae.String("Merci\n世界"), nil
			}
		case "condition":
			switch prop {
			case "rtyp":
				return ae.Enum("tfro"), nil
			case "rqua":
				return ae.Enum("rqco"), nil
			case "rexp":
				return ae.String("example.com"), nil
			}
		}
		if value, ok := f.fields[ruleFixtureKey(container)][prop]; ok {
			return value, nil
		}
	}
	return ae.Value{}, fmt.Errorf("unexpected rules read %+v", v)
}
func (f *rulesDescriptorFixture) send(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
	f.events = append(f.events, event)
	stats := ae.Stats{Events: 1, Attempted: true}
	switch event.Class + event.ID {
	case "coregetd":
		v, e := f.get(event.Params["----"])
		return v, stats, e
	case "corecrel":
		if event.Params["kocl"].Code == "rule" {
			ref := ruleFixtureRef("created")
			f.rules = append(f.rules, ref)
			f.fields[ref.Text] = map[string]ae.Value{}
			for code, value := range event.Params["prdt"].Fields {
				f.fields[ref.Text][code] = value
			}
			return ref, stats, nil
		}
		ref := ruleFixtureRef(fmt.Sprintf("condition-%d", len(f.conditions)+1))
		f.conditions = append(f.conditions, ref)
		f.fields[ref.Text] = event.Params["prdt"].Fields
		return ref, stats, nil
	case "coresetd":
		ref := event.Params["----"]
		prop := descriptorProperty(ref)
		if prop == "racm" && f.configureErr != nil {
			return ae.Value{}, stats, f.configureErr
		}
		key := ruleFixtureKey(ref.Fields["from"])
		f.fields[key][prop] = event.Params["data"]
		return ae.Null(), stats, nil
	case "coredelo":
		return ae.Null(), stats, f.cleanupErr
	}
	return ae.Value{}, stats, fmt.Errorf("unexpected rule event %+v", event)
}
func rulesFixtureClient(f *rulesDescriptorFixture) *Client {
	c := NewClient()
	c.shared.nativeSender = f.send
	c.shared.backend = BackendNative
	return c
}
func fixtureRuleInput() RuleInput {
	return RuleInput{Name: "世界 newsletters", Account: "Personal", FromDomain: "example.com", MoveTo: "News", Enabled: true, SubjectContains: []string{"Réponse", "世界"}, MarkRead: true}
}

func fixtureListedRule(f *rulesDescriptorFixture, key string, enabled bool) {
	f.rules = append(f.rules, ruleFixtureRef(key))
	f.fields[key] = map[string]ae.Value{
		"pnam": ae.String("世界 " + key), "isac": ae.Bool(enabled),
		"rstm": ae.Bool(false), "rdme": ae.Bool(false),
		"rmre": ae.Bool(false), "rmfl": ae.Bool(false),
	}
}

func TestNativeDescriptorRuleListReadsTypedConditionsAndActions(t *testing.T) {
	f := newRulesDescriptorFixture()
	fixtureListedRule(f, "listed", true)
	f.fields["listed"]["rstm"] = ae.Bool(true)
	f.fields["listed"]["rtme"] = ruleFixtureRef("box")
	f.fields["listed"]["rmre"] = ae.Bool(true)
	f.conditions = []ae.Value{ruleFixtureRef("condition")}
	got, err := rulesFixtureClient(f).ListRules()
	if err != nil {
		t.Fatal(err)
	}
	want := []Rule{{Name: "世界 listed", Enabled: true, Conditions: []string{"from header does contain value example.com"}, Actions: []string{"move to News", "mark read"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %+v, want %+v", got, want)
	}
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("list mutated Mail: %+v", event)
		}
	}
}

func TestNativeDescriptorRuleListGetterFailureRetainsPropertyContext(t *testing.T) {
	f := newRulesDescriptorFixture()
	fixtureListedRule(f, "listed", false)
	refused := &ae.Error{Code: -1743, Message: "automation denied"}
	f.readErrors["listed:rmre"] = refused
	_, err := rulesFixtureClient(f).ListRules()
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), "rule 1 property rmre") {
		t.Fatalf("getter failure lost context or cause: %v", err)
	}
}

func TestNativeDescriptorRuleListMissingMoveDestinationRemainsVisible(t *testing.T) {
	f := newRulesDescriptorFixture()
	fixtureListedRule(f, "obsolete", false)
	f.fields["obsolete"]["rstm"] = ae.Bool(true)
	f.fields["obsolete"]["rtme"] = ae.TypeCode("msng")
	fixtureListedRule(f, "following", true)
	got, err := rulesFixtureClient(f).ListRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Enabled || !reflect.DeepEqual(got[0].Actions, []string{"move to unavailable mailbox"}) || !got[1].Enabled {
		t.Fatalf("missing destination hid action or interrupted listing: %+v", got)
	}
	for _, event := range f.events {
		ref := event.Params["----"]
		if descriptorProperty(ref) == "pnam" && ref.Fields["from"].Code == "msng" {
			t.Fatalf("dereferenced missing destination: %+v", event)
		}
		if event.ID != "getd" {
			t.Fatalf("list mutated Mail: %+v", event)
		}
	}
}

func TestNativeDescriptorRuleListMoveGetterFailuresRemainFatal(t *testing.T) {
	for _, property := range []string{"rtme", "pnam"} {
		for _, code := range []int{-1743, -1712, -1700, -1728} {
			t.Run(fmt.Sprintf("%s/%d", property, code), func(t *testing.T) {
				f := newRulesDescriptorFixture()
				fixtureListedRule(f, "listed", false)
				f.fields["listed"]["rstm"] = ae.Bool(true)
				f.fields["listed"]["rtme"] = ruleFixtureRef("box")
				key := "listed:rtme"
				if property == "pnam" {
					key = "box:pnam"
				}
				refused := &ae.Error{Code: code, Message: "fixture getter failure"}
				f.readErrors[key] = refused
				_, err := rulesFixtureClient(f).ListRules()
				if !errors.Is(err, refused) || !strings.Contains(err.Error(), "rule 1 move destination property "+property) {
					t.Fatalf("destination getter failure suppressed or lost context: %v", err)
				}
			})
		}
	}
}

func TestNativeDescriptorRuleListMalformedMoveDestinationFailsClosed(t *testing.T) {
	for _, value := range []ae.Value{ae.TypeCode("mbxp"), ae.Int(1), ae.Bool(false), ae.String("msng"), ae.Null()} {
		t.Run(value.Kind+"/"+value.Code, func(t *testing.T) {
			f := newRulesDescriptorFixture()
			fixtureListedRule(f, "listed", false)
			f.fields["listed"]["rstm"] = ae.Bool(true)
			f.fields["listed"]["rtme"] = value
			_, err := rulesFixtureClient(f).ListRules()
			if err == nil || !strings.Contains(err.Error(), "expected mailbox reference") {
				t.Fatalf("malformed destination reinterpreted as missing: %v", err)
			}
			for _, event := range f.events {
				ref := event.Params["----"]
				if descriptorProperty(ref) == "pnam" && reflect.DeepEqual(ref.Fields["from"], value) {
					t.Fatalf("dereferenced malformed destination: %+v", event)
				}
			}
		})
	}
}

func TestNativeDescriptorRuleListPublicDictionaryLabels(t *testing.T) {
	// Public RuleType and RuleQualifier names from Mail.sdef. tsig occurs
	// twice with conflicting names, so it must remain an explicit code.
	labels := map[string]string{
		"tacc": "account", "tanr": "any recipient", "tccc": "cc header",
		"tevm": "matches every message", "tfro": "from header", "thdk": "header key",
		"tmec": "message content", "tmij": "message is junk mail",
		"tsii": "sender is in my contacts", "tsah": "sender is in my previous recipients",
		"tsim": "sender is member of group", "tsin": "sender is not in my contacts",
		"tnah": "sender is not in my previous recipients", "tsub": "subject header",
		"ttoo": "to header", "ttoc": "to or cc header", "tatt": "attachment type",
		"rqbw": "begins with value", "rqco": "does contain value", "rqdn": "does not contain value",
		"rqew": "ends with value", "rqie": "equal to value", "rqlt": "less than value",
		"rqgt": "greater than value", "rqno": "none", "tsig": "tsig", "zzzz": "zzzz",
	}
	for code, want := range labels {
		if got := nativeRuleEnum(code); got != want {
			t.Errorf("public dictionary label %q = %q, want %q", code, got, want)
		}
	}
	f := newRulesDescriptorFixture()
	fixtureListedRule(f, "listed", true)
	cases := []struct{ ruleType, qualifier, expression, want string }{
		{"ttoo", "rqco", "世界@example.com", "to header does contain value 世界@example.com"},
		{"tevm", "rqno", "", "matches every message none"},
		{"tsub", "rqbw", "Réponse", "subject header begins with value Réponse"},
		{"tsub", "rqdn", "other", "subject header does not contain value other"},
		{"tsub", "rqew", "end", "subject header ends with value end"},
		{"tacc", "rqie", "Personal", "account equal to value Personal"},
		{"tatt", "rqlt", "10", "attachment type less than value 10"},
		{"tatt", "rqgt", "20", "attachment type greater than value 20"},
		{"tsig", "rqno", "", "tsig none"},
		{"zzzz", "qqqq", "future", "zzzz qqqq future"},
	}
	want := make([]string, 0, len(cases))
	for i, tc := range cases {
		key := fmt.Sprintf("listed-condition-%d", i)
		f.conditions = append(f.conditions, ruleFixtureRef(key))
		f.fields[key] = map[string]ae.Value{"rtyp": ae.Enum(tc.ruleType), "rqua": ae.Enum(tc.qualifier), "rexp": ae.String(tc.expression)}
		want = append(want, tc.want)
	}
	got, err := rulesFixtureClient(f).ListRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Conditions, want) {
		t.Fatalf("condition labels = %+v, want %+v", got, want)
	}
}

func TestNativeDescriptorRuleListMalformedConditionEnumsFailClosed(t *testing.T) {
	for _, property := range []string{"rtyp", "rqua"} {
		for _, value := range []ae.Value{ae.String("ttoo"), ae.TypeCode("rqco"), ae.Int(1), ae.Null(), ae.Enum("")} {
			t.Run(property+"/"+value.Kind+"/"+value.Code, func(t *testing.T) {
				f := newRulesDescriptorFixture()
				fixtureListedRule(f, "listed", true)
				f.conditions = []ae.Value{ruleFixtureRef("malformed-condition")}
				f.fields["malformed-condition"] = map[string]ae.Value{"rtyp": ae.Enum("ttoo"), "rqua": ae.Enum("rqco"), "rexp": ae.String("example.com")}
				f.fields["malformed-condition"][property] = value
				_, err := rulesFixtureClient(f).ListRules()
				if err == nil || !strings.Contains(err.Error(), "rule 1 condition 1 property "+property+": expected enumerated value") {
					t.Fatalf("malformed condition displayed without typed validation: %v", err)
				}
			})
		}
	}
}
func TestNativeDescriptorRuleCreateDictionaryCodes(t *testing.T) {
	f := newRulesDescriptorFixture()
	input := fixtureRuleInput()
	result, e := rulesFixtureClient(f).CreateRule(input)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(result, ruleFromInput(input)) {
		t.Fatalf("result %+v", result)
	}
	conditions := 0
	for _, event := range f.events {
		if event.ID == "crel" {
			props := event.Params["prdt"].Fields
			switch event.Params["kocl"].Code {
			case "rule":
				if props["isac"].Boolean || props["pnam"].Text != input.Name {
					t.Fatalf("rule created enabled or renamed: %+v", event)
				}
			case "rucr":
				conditions++
				if props["rqua"].Code != "rqco" || props["rexp"].Kind != "string" {
					t.Fatalf("wrong condition codes: %+v", event)
				}
			}
		}
	}
	if conditions != 3 {
		t.Fatalf("conditions=%d", conditions)
	}
	if !f.fields["created"]["racm"].Boolean || !f.fields["created"]["rstm"].Boolean || !f.fields["created"]["rmre"].Boolean || !f.fields["created"]["isac"].Boolean {
		t.Fatalf("rule fields %+v", f.fields)
	}
}
func TestNativeDescriptorRulePartialCreationOwnedCleanup(t *testing.T) {
	f := newRulesDescriptorFixture()
	f.configureErr = errors.New("configuration refused")
	f.cleanupErr = errors.New("delete refused")
	_, e := rulesFixtureClient(f).CreateRule(fixtureRuleInput())
	var mutation *MutationError
	if !errors.As(e, &mutation) || mutation.Cleanup == "" {
		t.Fatalf("cleanup loss: %v", e)
	}
	deletes := 0
	for _, event := range f.events {
		if event.ID == "delo" {
			deletes++
			if event.Params["----"].Text != "created" {
				t.Fatalf("cleanup deleted unowned rule: %+v", event)
			}
		}
	}
	if deletes != 1 || f.fields["created"]["isac"].Boolean {
		t.Fatalf("unsafe partial rule cleanup: %+v", f.events)
	}
}
func TestNativeDescriptorRuleDuplicateNameFailsClosed(t *testing.T) {
	for _, operation := range []string{"create", "enable", "delete"} {
		t.Run(operation, func(t *testing.T) {
			f := newRulesDescriptorFixture()
			for _, key := range []string{"one", "two"} {
				f.rules = append(f.rules, ruleFixtureRef(key))
				f.fields[key] = map[string]ae.Value{"pnam": ae.String("same")}
			}
			c := rulesFixtureClient(f)
			var e error
			switch operation {
			case "create":
				input := fixtureRuleInput()
				input.Name = "same"
				_, e = c.CreateRule(input)
			case "enable":
				e = c.SetRuleEnabled("same", true)
			case "delete":
				e = c.DeleteRule("same")
			}
			if e == nil {
				t.Fatal("duplicate accepted")
			}
			for _, event := range f.events {
				if event.ID != "getd" {
					t.Fatalf("duplicate mutation %+v", event)
				}
			}
		})
	}
}
func TestNativeDescriptorSignatureUnicodeRead(t *testing.T) {
	f := newRulesDescriptorFixture()
	result, e := rulesFixtureClient(f).ListSignatures(true)
	if e != nil {
		t.Fatal(e)
	}
	if len(result) != 1 || result[0].Name != "世界" || result[0].Content != "Merci\n世界" {
		t.Fatalf("signature changed: %+v", result)
	}
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("signature write %+v", event)
		}
	}
}

func TestNativeDescriptorRuleListBatchesEightyNineRules(t *testing.T) {
	f := newRulesDescriptorFixture()
	for i := 0; i < 89; i++ {
		fixtureListedRule(f, fmt.Sprintf("listed-%d", i), true)
	}
	f.conditions = []ae.Value{ruleFixtureRef("condition")}
	got, err := rulesFixtureClient(f).ListRules()
	if err != nil || len(got) != 89 {
		t.Fatalf("rules=%d err=%v", len(got), err)
	}
	// Before batching: discovery + 89*(6 properties + conditions + 3 fields)=891.
	// Now: discovery + ceil(89*7/32) + ceil(89*3/32)=30.
	if len(f.events) != 30 {
		t.Fatalf("events=%d want30", len(f.events))
	}
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("list mutated Mail: %+v", event)
		}
		request := event.Params["----"]
		if request.Kind == "list" && len(request.Items) > 32 {
			t.Fatalf("unbounded batch: %d", len(request.Items))
		}
	}
	if !reflect.DeepEqual(got[88].Conditions, []string{"from header does contain value example.com"}) {
		t.Fatalf("last rule shifted: %+v", got[88])
	}
}

func TestNativeDescriptorRuleListMalformedBatchFailsClosed(t *testing.T) {
	for _, reply := range []ae.Value{ae.Null(), ae.List(), ae.List(ae.Bool(true))} {
		t.Run(reply.Kind+fmt.Sprint(len(reply.Items)), func(t *testing.T) {
			f := newRulesDescriptorFixture()
			fixtureListedRule(f, "listed", true)
			c := rulesFixtureClient(f)
			c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.ID == "getd" && event.Params["----"].Kind == "list" {
					f.events = append(f.events, event)
					return reply, ae.Stats{Events: 1}, nil
				}
				return f.send(ctx, event)
			}
			_, err := c.ListRules()
			if err == nil || !strings.Contains(err.Error(), "expected 7 explicit values") {
				t.Fatalf("malformed batch accepted: %v", err)
			}
			if len(f.events) != 2 {
				t.Fatalf("retried malformed batch: %d", len(f.events))
			}
		})
	}
}

func TestNativeDescriptorRuleListBatchCancellationStopsDispatch(t *testing.T) {
	f := newRulesDescriptorFixture()
	for i := 0; i < 89; i++ {
		fixtureListedRule(f, fmt.Sprintf("listed-%d", i), true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := rulesFixtureClient(f).WithContext(ctx)
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		v, stats, err := f.send(ctx, event)
		if event.Params["----"].Kind == "list" {
			cancel()
		}
		return v, stats, err
	}
	_, err := c.ListRules()
	if !errors.Is(err, context.Canceled) || len(f.events) != 2 {
		t.Fatalf("cancel error=%v events=%d", err, len(f.events))
	}
}

func TestNativeDescriptorRuleListBatchTimeoutHasPhaseAndDoesNotRetry(t *testing.T) {
	f := newRulesDescriptorFixture()
	for i := 0; i < 89; i++ {
		fixtureListedRule(f, fmt.Sprintf("listed-%d", i), true)
	}
	c := rulesFixtureClient(f)
	batches := 0
	c.shared.nativeSender = func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		if event.Params["----"].Kind == "list" {
			batches++
			if batches == 2 {
				f.events = append(f.events, event)
				return ae.Value{}, ae.Stats{Events: 1}, context.DeadlineExceeded
			}
		}
		return f.send(ctx, event)
	}
	_, err := c.ListRules()
	var timeout *AutomationTimeoutError
	if !errors.As(err, &timeout) || timeout.Trace == nil || timeout.Trace.Pending != "rules_projection_2" || timeout.Trace.LastCompleted != "rules_projection_1" {
		t.Fatalf("timeout lost batch progress: %v", err)
	}
	if len(f.events) != 3 || batches != 2 {
		t.Fatalf("timeout retried: events=%d batches=%d", len(f.events), batches)
	}
}
