package mail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

type nativeReadFixture struct {
	accounts   []appleevents.Value
	messages   map[string][]appleevents.Value
	boxes      map[string][]appleevents.Value
	properties map[string]map[string]appleevents.Value
	events     []appleevents.Event
	fail       func(appleevents.Event) error
}

func fixtureRef(class, id string) appleevents.Value {
	return appleevents.ByID(class, appleevents.String(id), appleevents.Null())
}
func fixtureKey(ref appleevents.Value) string {
	if ref.Kind == "opaque" {
		return ref.Data
	}
	if ref.Kind != "object" {
		return "app"
	}
	if ref.Fields["want"].Code == "prop" {
		return fixtureKey(ref.Fields["from"])
	}
	return ref.Fields["want"].Code + ":" + ref.Fields["seld"].Text
}
func newNativeReadFixture() *nativeReadFixture {
	account := fixtureRef("mact", "a")
	box := fixtureRef("mbxp", "inbox")
	archive := fixtureRef("mbxp", "archive")
	return &nativeReadFixture{messages: map[string][]appleevents.Value{"mbxp:inbox": {fixtureRef("mssg", "42")}}, accounts: []appleevents.Value{account}, boxes: map[string][]appleevents.Value{"mact:a": {box, archive}}, properties: map[string]map[string]appleevents.Value{
		"app":          {"vers": appleevents.String("1.0")},
		"mact:a":       {"ID  ": appleevents.String("a"), "pnam": appleevents.String("Personal"), "emad": appleevents.List(appleevents.String("a@example.test"), appleevents.String("other@example.test")), "unme": appleevents.String("a"), "isac": appleevents.Bool(true), "atyp": appleevents.Enum("etim")},
		"mbxp:inbox":   {"pnam": appleevents.String("INBOX"), "mbuc": appleevents.Int(1), "mact": account},
		"mbxp:archive": {"pnam": appleevents.String("All Mail"), "mbuc": appleevents.Int(0), "mact": account},
		"mssg:42":      {"ID  ": appleevents.Int(42), "meid": appleevents.String("<fixture@example.test>"), "subj": appleevents.String("Fixture"), "sndr": appleevents.String("Sender"), "rdrc": appleevents.Date(time.Unix(100, 0)), "drcv": appleevents.Date(time.Unix(99, 0)), "isrd": appleevents.Bool(false), "isfl": appleevents.Bool(true), "isdl": appleevents.Bool(false), "msze": appleevents.Int(100), "ctnt": appleevents.String("Body"), "alhe": appleevents.String("Message-ID: <fixture@example.test>\r\nIn-Reply-To: <parent@example.test>\r\nReferences: <ancestor@example.test> <parent@example.test>\r\n")},
		"attc:att":     {"pnam": appleevents.String("fixture.txt"), "atsz": appleevents.Int(4), "attp": appleevents.String("text/plain")},
	}, events: nil, fail: nil}
}

func (f *nativeReadFixture) key(ref appleevents.Value) string {
	if ref.Kind == "object" && ref.Fields["want"].Code == "mbxp" && ref.Fields["form"].Code == "indx" && ref.Fields["seld"].Kind == "int" {
		children := f.boxes[f.key(ref.Fields["from"])]
		index := int(ref.Fields["seld"].Number) - 1
		if index >= 0 && index < len(children) {
			return f.key(children[index])
		}
		return "missing-index"
	}
	return fixtureKey(ref)
}

func (f *nativeReadFixture) predicate(test, ref appleevents.Value) bool {
	if test.Kind == "logical" {
		operator := test.Fields["logc"].Code
		for _, term := range test.Fields["term"].Items {
			match := f.predicate(term, ref)
			if operator == "AND " && !match {
				return false
			}
			if operator == "OR  " && match {
				return true
			}
		}
		return operator == "AND "
	}
	left := test.Fields["obj1"]
	actual, err := f.evaluate(appleevents.Property(left.Fields["seld"].Code, ref))
	if err != nil {
		return false
	}
	wanted := test.Fields["obj2"]
	switch test.Fields["relo"].Code {
	case "=   ":
		return reflect.DeepEqual(actual, wanted)
	case ">=  ":
		return actual.Text >= wanted.Text
	}
	return false
}

func (f *nativeReadFixture) evaluate(v appleevents.Value) (appleevents.Value, error) {
	if v.Kind == "list" {
		out := make([]appleevents.Value, len(v.Items))
		for i, item := range v.Items {
			result, err := f.evaluate(item)
			if err != nil {
				return appleevents.Value{}, err
			}
			out[i] = result
		}
		return appleevents.List(out...), nil
	}
	if v.Kind != "object" {
		return v, nil
	}
	class := v.Fields["want"].Code
	if class == "mbxp" && v.Fields["form"].Code == "indx" && v.Fields["seld"].Kind == "int" {
		return appleevents.Value{Kind: "opaque", Data: f.key(v)}, nil
	}

	if class == "mssg" && v.Fields["form"].Code == "rang" {
		refs := f.messages[f.key(v.Fields["from"])]
		bounds := v.Fields["seld"].Fields
		start := int(bounds["star"].Fields["seld"].Number) - 1
		end := int(bounds["stop"].Fields["seld"].Number)
		if start < 0 || end > len(refs) || start >= end {
			return appleevents.Value{}, &appleevents.Error{Code: -1728, Message: "range changed"}
		}
		return appleevents.List(refs[start:end]...), nil
	}
	if class == "mssg" && v.Fields["form"].Code == "test" {
		refs := []appleevents.Value{}
		for _, ref := range f.messages[f.key(v.Fields["from"])] {
			if f.predicate(v.Fields["seld"], ref) {
				refs = append(refs, ref)
			}
		}
		return appleevents.List(refs...), nil
	}
	if class == "mact" && v.Fields["form"].Code == "test" {
		name := v.Fields["seld"].Fields["obj2"].Text
		refs := []appleevents.Value{}
		for _, ref := range f.accounts {
			if f.properties[fixtureKey(ref)]["pnam"].Text == name {
				refs = append(refs, ref)
			}
		}
		return appleevents.List(refs...), nil
	}

	if class == "prop" {
		container := v.Fields["from"]
		code := v.Fields["seld"].Code
		if container.Kind == "object" && (container.Fields["seld"].Code == "all " || container.Fields["form"].Code == "test" || container.Fields["form"].Code == "rang") {
			refs, err := f.evaluate(container)
			if err != nil {
				return appleevents.Value{}, err
			}
			values := make([]appleevents.Value, 0, len(refs.Items))
			for _, ref := range refs.Items {
				if ref.Kind == "list" {
					nested := []appleevents.Value{}
					for _, child := range ref.Items {
						value, err := f.evaluate(appleevents.Property(code, child))
						if err != nil {
							return appleevents.Value{}, err
						}
						nested = append(nested, value)
					}
					values = append(values, appleevents.List(nested...))
					continue
				}
				value, err := f.evaluate(appleevents.Property(code, ref))
				if err != nil {
					return appleevents.Value{}, err
				}
				values = append(values, value)
			}
			return appleevents.List(values...), nil
		}
		key := f.key(container)
		if classOf := container.Fields["want"].Code; classOf == "mssg" && container.Fields["seld"].Kind == "int" {
			key = fmt.Sprintf("mssg:%d", container.Fields["seld"].Number)
		}
		if properties, ok := f.properties[key]; ok {
			if code == "pALL" {
				return appleevents.Record(properties), nil
			}
			if value, ok := properties[code]; ok {
				return value, nil
			}
		}
		return appleevents.Value{}, &appleevents.Error{Code: -1728, Message: "missing fixture property"}
	}
	if v.Fields["seld"].Code == "all " {

		container := v.Fields["from"]
		if container.Kind == "object" && container.Fields["seld"].Code == "all " {
			parents, err := f.evaluate(container)
			if err != nil {
				return appleevents.Value{}, err
			}
			nested := []appleevents.Value{}
			for _, parent := range parents.Items {
				children, err := f.evaluate(appleevents.All(class, parent))
				if err != nil {
					return appleevents.Value{}, err
				}
				nested = append(nested, children)
			}
			return appleevents.List(nested...), nil
		}
		key := f.key(v.Fields["from"])
		switch class {
		case "mact":
			return appleevents.List(f.accounts...), nil
		case "mbxp":
			return appleevents.List(f.boxes[key]...), nil
		case "mssg":
			return appleevents.List(f.messages[key]...), nil

		case "attc":
			return appleevents.List(fixtureRef("attc", "att")), nil
		case "trcp", "crcp", "brcp":
			return appleevents.List(), nil
		}
	}
	return v, nil
}
func (f *nativeReadFixture) sender(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
	f.events = append(f.events, event)
	if f.fail != nil {
		if err := f.fail(event); err != nil {
			return appleevents.Value{}, appleevents.Stats{Attempted: true}, err
		}
	}
	switch event.ID {
	case "getd":
		v, err := f.evaluate(event.Params["----"])
		return v, appleevents.Stats{Attempted: true, Events: 1}, err
	case "cnte":
		v, err := f.evaluate(event.Params["----"])
		return appleevents.Int(int64(len(v.Items))), appleevents.Stats{Attempted: true, Events: 1}, err
	case "save":
		err := os.WriteFile(event.Params["kfil"].Text, []byte("data"), 0600)
		return appleevents.Null(), appleevents.Stats{Attempted: true, Events: 1}, err
	case "setd", "delo", "move", "syac", "chma":
		return appleevents.Null(), appleevents.Stats{Attempted: true, Events: 1}, nil
	}
	return appleevents.Value{}, appleevents.Stats{}, fmt.Errorf("unexpected fixture event %s/%s", event.Class, event.ID)
}
func fixtureClient(t *testing.T, f *nativeReadFixture) *Client {
	t.Helper()
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	c := NewClient()
	c.shared.backend = BackendNative
	c.shared.nativeSender = f.sender
	return c
}

func TestNativeReadAccountsCacheAndMailboxScope(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	accounts, err := c.GetAccountsJSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != "a" || accounts[0].AccountType != "imap" || len(accounts[0].EmailAddresses) != 2 {
		t.Fatalf("accounts: %#v", accounts)
	}
	count := len(f.events)
	accounts[0].EmailAddresses[0] = "changed"
	cached, err := c.GetAccountsJSON()
	if err != nil || len(f.events) != count || cached[0].EmailAddress != "a@example.test" || cached[0].EmailAddresses[0] != "a@example.test" {
		t.Fatalf("bad cache: %#v, %v", cached, err)
	}
	mailboxes, err := c.nativeMailboxes("Personal")
	if err != nil || len(mailboxes) != 2 || mailboxes[0].TotalCount != 1 {
		t.Fatalf("mailboxes: %#v %v", mailboxes, err)
	}
	// A second account with the same named mailbox cannot supply the first
	// account's lookup. It is deliberately unreachable under mact:a.
	f.boxes["mact:other"] = []appleevents.Value{fixtureRef("mbxp", "other")}
	if err := c.MarkMessageAsRead("Personal", "Other", "42", true); !IsNotFound(err) {
		t.Fatalf("cross-account mailbox accepted: %v", err)
	}
}

func TestNativeReadLookupAmbiguityAndExactID(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	f.accounts = append(f.accounts, fixtureRef("mact", "duplicate"))
	f.properties["mact:duplicate"] = map[string]appleevents.Value{"pnam": appleevents.String("Personal")}
	if err := c.MarkMessageAsRead("Personal", "INBOX", "42", true); err == nil {
		t.Fatal("ambiguous account accepted")
	}
	f.accounts = f.accounts[:1]
	f.boxes["mbxp:archive"] = []appleevents.Value{fixtureRef("mbxp", "nested")}
	f.properties["mbxp:nested"] = map[string]appleevents.Value{"pnam": appleevents.String("INBOX"), "mact": fixtureRef("mact", "a"), "mbxc": fixtureRef("mbxp", "archive")}
	if err := c.MarkMessageAsRead("Personal", "INBOX", "42", true); err == nil {
		t.Fatal("ambiguous mailbox accepted")
	}
	if err := c.MarkMessageAsRead("Personal", "All Mail/INBOX", "42", true); err != nil {
		t.Fatal(err)
	}
	f.boxes["mbxp:archive"] = nil
	f.properties["mssg:42"]["ID  "] = appleevents.Int(99)
	if err := c.MarkMessageAsRead("Personal", "INBOX", "42", true); err == nil {
		t.Fatal("wrong exact ID accepted")
	}
}

func TestNativeReadVerificationNoRendererAndMissingDistinction(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	message, err := c.GetMessageDetailsForVerificationJSON("Personal", "INBOX", "42")
	if err != nil || message == nil || message.RFCMessageID != "<fixture@example.test>" {
		t.Fatalf("message %#v %v", message, err)
	}
	for _, event := range f.events {
		if reflect.DeepEqual(event.Params["----"], appleevents.Property("ctnt", appleevents.ByID("mssg", appleevents.Int(42), fixtureRef("mbxp", "inbox")))) {
			t.Fatal("verification rendered body")
		}
	}
	delete(f.properties, "mssg:42")
	message, err = c.GetMessageStateForVerificationJSON("Personal", "INBOX", "42")
	if err != nil || message != nil {
		t.Fatalf("missing message %#v %v", message, err)
	}
	permission := &appleevents.Error{Code: -1743, Message: "permission denied"}
	f.fail = func(event appleevents.Event) error { return permission }
	if _, err = c.GetMessageStateForVerificationJSON("Personal", "INBOX", "42"); !errors.Is(err, permission) {
		t.Fatalf("permission became absence: %v", err)
	}
}

func TestNativeReadListFiltersOffsetAndContent(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	messages, err := c.nativeMessages("Personal", "INBOX", 1, 0, true, true, true, "")
	if err != nil || len(messages) != 1 || messages[0].Content != "Body" {
		t.Fatalf("messages %#v %v", messages, err)
	}
	messages, err = c.nativeMessages("Personal", "INBOX", 1, 1, true, true, false, "")
	if err != nil || len(messages) != 0 {
		t.Fatalf("offset %#v %v", messages, err)
	}
	f.properties["mssg:42"]["isrd"] = appleevents.Bool(true)
	messages, err = c.nativeMessages("Personal", "INBOX", 1, 0, true, true, false, "")
	if err != nil || len(messages) != 0 {
		t.Fatalf("filter %#v %v", messages, err)
	}
}

func TestNativeReadActionsDescriptorsAndNoReplay(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	for _, action := range []func() error{func() error { return c.MarkMessageAsRead("Personal", "INBOX", "42", true) }, func() error { return c.FlagMessage("Personal", "INBOX", "42", false) }, func() error { return c.MoveMessage("Personal", "INBOX", "42", "All Mail") }} {
		if err := action(); err != nil {
			t.Fatal(err)
		}
	}
	var writes []appleevents.Event
	for _, event := range f.events {
		if event.ID == "setd" || event.ID == "move" {
			writes = append(writes, event)
		}
	}
	if len(writes) != 3 || writes[0].Params["----"].Fields["seld"].Code != "isrd" || writes[1].Params["----"].Fields["seld"].Code != "isfl" || writes[2].Params["insh"].Kind != "opaque" || writes[2].Params["insh"].Data != "mbxp:archive" {
		t.Fatalf("writes %#v", writes)
	}
	f.events = nil
	failure := &appleevents.Error{Code: -609, Message: "transport lost", Attempted: true}
	f.fail = func(event appleevents.Event) error {
		if event.ID == "setd" {
			return failure
		}
		return nil
	}
	if err := c.MarkMessageAsRead("Personal", "INBOX", "42", true); !errors.Is(err, failure) {
		t.Fatalf("lost failure: %v", err)
	}
	count := 0
	for _, event := range f.events {
		if event.ID == "setd" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("mutation replayed %d times", count)
	}
}

func TestNativeReadAttachmentExactIDFileAndAmbiguity(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	path := filepath.Join(t.TempDir(), "fixture.txt")
	attachments, err := c.GetAttachmentsJSON("Personal", "INBOX", "42")
	if err != nil || len(attachments) != 1 || attachments[0].Name != "fixture.txt" {
		t.Fatalf("attachments %#v %v", attachments, err)
	}
	if err = c.SaveAttachmentByIndex("Personal", "INBOX", "42", "fixture.txt", 0, path); err != nil {
		t.Fatal(err)
	}
	last := f.events[len(f.events)-1]
	if last.ID != "save" || last.Params["kfil"].Kind != "file" || filepath.Dir(filepath.Dir(last.Params["kfil"].Text)) != filepath.Dir(path) || last.Params["kfil"].Text == path {
		t.Fatalf("save %#v", last)
	}
	if err = c.SaveAttachmentByIndex("Personal", "INBOX", "42", "wrong", 0, filepath.Join(t.TempDir(), "wrong.txt")); err == nil {
		t.Fatal("wrong attachment name accepted")
	}
	if err = c.SaveAttachmentByIndex("Personal", "INBOX", "42", "fixture.txt", 1, filepath.Join(t.TempDir(), "missing.txt")); !IsNotFound(err) {
		t.Fatalf("index %v", err)
	}
	for _, event := range f.events {
		direct := event.Params["----"]
		if direct.Kind == "object" && direct.Fields["want"].Code == "prop" && direct.Fields["seld"].Code == "ID  " && direct.Fields["from"].Fields["seld"].Code == "all " {
			t.Fatal("attachment enumerated all message IDs")
		}
	}
}

func TestNativeReadSyncHealthAndContentBatch(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	if err := c.SyncAccount("Personal"); err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, event := range f.events {
		if event.Class == "emal" {
			commands = append(commands, event.ID)
		}
	}
	if !reflect.DeepEqual(commands, []string{"syac", "chma"}) {
		t.Fatalf("sync commands %#v", commands)
	}
	if err := c.CheckAutomationAccess(time.Second); err != nil {
		t.Fatal(err)
	}
	if count, err := c.CheckAccountAccess(time.Second); err != nil || count != 1 {
		t.Fatalf("health %d %v", count, err)
	}
	if err := c.RunLiveProbe(time.Second); err != nil {
		t.Fatal(err)
	}
	content, err := c.nativeMessageContentBatch("Personal", "INBOX", []string{"42"}, time.Second)
	if err != nil || content["42"] != "Body" {
		t.Fatalf("content %#v %v", content, err)
	}
	destination, err := c.ResolveArchiveMailbox("Personal")
	if err != nil || destination != "All Mail" {
		t.Fatalf("archive %s %v", destination, err)
	}
}

func TestNativeReadMalformedRequiredDescriptorsFail(t *testing.T) {
	for _, kind := range []string{"account", "message", "attachment"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeReadFixture()
			c := fixtureClient(t, f)
			var err error
			switch kind {
			case "account":
				f.properties["mact:a"]["emad"] = appleevents.String("wrong")
				_, err = c.GetAccountsJSON()
			case "message":
				f.properties["mssg:42"]["isrd"] = appleevents.Null()
				_, err = c.GetMessageStateForVerificationJSON("Personal", "INBOX", "42")
			case "attachment":
				f.properties["attc:att"]["atsz"] = appleevents.String("wrong")
				_, err = c.GetAttachmentsJSON("Personal", "INBOX", "42")
			}
			if err == nil {
				t.Fatalf("malformed %s became a successful model", kind)
			}
		})
	}
}

func TestNativeReadTimeoutIsFailureWithPhase(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	f.fail = func(event appleevents.Event) error {
		direct := event.Params["----"]
		if direct.Kind == "object" && direct.Fields["want"].Code == "prop" && direct.Fields["seld"].Code == "ID  " && direct.Fields["from"].Fields["want"].Code == "mssg" {
			return context.DeadlineExceeded
		}
		return nil
	}
	message, trace, err := c.nativeMessageDetails("Personal", "INBOX", "42", time.Second, false, true)
	var timeout *AutomationTimeoutError
	if message != nil || !errors.As(err, &timeout) || trace == nil || trace.Pending != "lookup_by_id" {
		t.Fatalf("timeout became missing %#v %#v %v", message, trace, err)
	}
}

func TestNativeReadDeleteFallbackStopsAtUncertainFailure(t *testing.T) {
	var attempts []string
	transport := &appleevents.Error{Code: -609, Message: "transport lost", Attempted: true}
	err := deleteMessageResolved("Newsletter", func(mailbox string) error {
		attempts = append(attempts, mailbox)
		if mailbox == "Newsletter" {
			return notFound("message", "42")
		}
		return transport
	})
	if !errors.Is(err, transport) || !reflect.DeepEqual(attempts, []string{"Newsletter", "All Mail"}) {
		t.Fatalf("uncertain mutation retried: %#v %v", attempts, err)
	}
}

func TestNativeReadSpecificAccountAvoidsGlobalMetadataAndPreservesFallback(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	account, err := c.accountByName("Personal")
	if err != nil || account.ID != "a" || len(f.events) != 2 {
		t.Fatalf("specific account %#v events=%d err=%v", account, len(f.events), err)
	}
	if c.shared.accountsLoaded {
		t.Fatal("specific account primed partial global cache")
	}
	if f.events[0].Params["----"].Fields["form"].Code != "test" || f.events[1].Params["----"].Fields["seld"].Code != "pALL" {
		t.Fatalf("specific account events %#v", f.events)
	}
	f.events = nil
	f.fail = func(event appleevents.Event) error {
		if event.Params["----"].Fields["seld"].Code == "pALL" {
			return &appleevents.Error{Code: -10000, Message: "unsupported account bag"}
		}
		return nil
	}
	account, err = c.accountByName("Personal")
	if err != nil || account.AccountType != "imap" || len(f.events) != 8 {
		t.Fatalf("fallback account %#v events=%d err=%v", account, len(f.events), err)
	}
	f.events = nil
	permission := &appleevents.Error{Code: -1743, Message: "permission denied"}
	f.fail = func(event appleevents.Event) error {
		if event.Params["----"].Fields["seld"].Code == "pALL" {
			return permission
		}
		return nil
	}
	if _, err = c.accountByName("Personal"); !errors.Is(err, permission) || len(f.events) != 2 {
		t.Fatalf("permission fallback events=%d err=%v", len(f.events), err)
	}
}

func TestNativeReadListFailsClosedOnIDReorder(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	idReads := 0
	c.shared.nativeSender = func(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
		direct := event.Params["----"]
		if event.ID == "getd" && direct.Kind == "object" && direct.Fields["want"].Code == "prop" && direct.Fields["seld"].Code == "ID  " && direct.Fields["from"].Fields["want"].Code == "mssg" && direct.Fields["from"].Fields["seld"].Code == "all " {
			idReads++
			if idReads == 2 {
				return appleevents.List(appleevents.Int(99)), appleevents.Stats{Attempted: true}, nil
			}
		}

		return f.sender(ctx, event)
	}
	messages, err := c.nativeMessages("Personal", "INBOX", 1, 0, false, false, false, "")
	if err == nil || len(messages) != 0 {
		t.Fatalf("reordered IDs produced inconsistent metadata: %#v %v", messages, err)
	}
}

func TestNativeReadBulkMailboxLookupDedupeAndScope(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	// One physical child is exposed both at account level and beneath Archive.
	child := fixtureRef("mbxp", "child")
	f.boxes["mact:a"] = append(f.boxes["mact:a"], child)
	f.boxes["mbxp:archive"] = []appleevents.Value{child}
	f.properties["mbxp:child"] = map[string]appleevents.Value{"pnam": appleevents.String("Child"), "mact": fixtureRef("mact", "a"), "mbxc": fixtureRef("mbxp", "archive")}
	if err := c.MarkMessageAsRead("Personal", "Child", "42", true); err != nil {
		t.Fatalf("same physical mailbox became ambiguous: %v", err)
	}
	if err := c.MarkMessageAsRead("Personal", "All Mail/Child", "42", true); err != nil {
		t.Fatalf("canonical path failed: %v", err)
	}
	// A second Child under a distinct container is a real ambiguity.
	other := fixtureRef("mbxp", "other")
	f.boxes["mbxp:inbox"] = []appleevents.Value{other}
	f.properties["mbxp:other"] = map[string]appleevents.Value{"pnam": appleevents.String("Child"), "mact": fixtureRef("mact", "a"), "mbxc": fixtureRef("mbxp", "inbox")}
	if err := c.MarkMessageAsRead("Personal", "Child", "42", true); err == nil {
		t.Fatal("distinct same-leaf mailboxes accepted")
	}
	f.boxes["mbxp:inbox"] = nil
	f.properties["mact:other"] = map[string]appleevents.Value{"ID  ": appleevents.String("other")}
	f.properties["mbxp:child"]["mact"] = fixtureRef("mact", "other")
	if err := c.MarkMessageAsRead("Personal", "Child", "42", true); err == nil {
		t.Fatal("cross-account mailbox accepted")
	}
}

func TestNativeReadAttachmentOptionalMIMEAndExclusivePublication(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	delete(f.properties["attc:att"], "attp")
	attachments, err := c.GetAttachmentsJSON("Personal", "INBOX", "42")
	if err != nil || len(attachments) != 1 || attachments[0].MimeType != "unknown" {
		t.Fatalf("optional MIME %#v %v", attachments, err)
	}
	path := filepath.Join(t.TempDir(), "attachment.txt")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	f.events = nil
	if err := c.SaveAttachmentByIndex("Personal", "INBOX", "42", "fixture.txt", 0, path); err == nil {
		t.Fatal("existing target accepted")
	}
	for _, event := range f.events {
		if event.ID == "save" {
			t.Fatal("save dispatched for existing target")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatalf("overwritten target %q %v", data, err)
	}
	path = filepath.Join(t.TempDir(), "racing.txt")
	f.fail = func(event appleevents.Event) error {
		if event.ID == "save" {
			return os.WriteFile(path, []byte("racer"), 0600)
		}
		return nil
	}
	if err := c.SaveAttachmentByIndex("Personal", "INBOX", "42", "fixture.txt", 0, path); err == nil {
		t.Fatal("racing target accepted")
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "racer" {
		t.Fatalf("overwritten racing target %q %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging leaked %#v %v", entries, err)
	}
	permission := &appleevents.Error{Code: -1743, Message: "permission denied"}
	f.fail = func(event appleevents.Event) error {
		if event.Params["----"].Fields["seld"].Code == "attp" {
			return permission
		}
		return nil
	}
	if _, err := c.GetAttachmentsJSON("Personal", "INBOX", "42"); !errors.Is(err, permission) {
		t.Fatalf("optional MIME suppressed permission: %v", err)
	}
}

func fixtureMessages(f *nativeReadFixture, count int, alternatingRead bool) {
	template := f.properties["mssg:42"]
	f.messages["mbxp:inbox"] = nil
	for id := int64(1); id <= int64(count); id++ {
		row := map[string]appleevents.Value{}
		for code, value := range template {
			row[code] = value
		}
		row["ID  "] = appleevents.Int(id)
		if alternatingRead {
			row["isrd"] = appleevents.Bool(id%2 == 0)
		}
		f.properties[fmt.Sprintf("mssg:%d", id)] = row
		f.messages["mbxp:inbox"] = append(f.messages["mbxp:inbox"], fixtureRef("mssg", fmt.Sprint(id)))
	}
}

// Inspect property specs inside both individual and batched read events.
func fixtureMessageProperties(events []appleevents.Event, visit func(string, appleevents.Value)) {
	var walk func(appleevents.Value)
	walk = func(value appleevents.Value) {
		if value.Kind == "object" && value.Fields["want"].Code == "prop" && value.Fields["from"].Fields["want"].Code == "mssg" {
			visit(value.Fields["seld"].Code, value.Fields["from"])
		}
		for _, item := range value.Items {
			walk(item)
		}
		for _, field := range value.Fields {
			walk(field)
		}
	}
	for _, event := range events {
		walk(event.Params["----"])
	}
}

func TestNativeReadListMetadataReusesFiltersAfterOffset(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	fixtureMessages(f, 2000, true)
	messages, err := c.nativeMessages("Personal", "INBOX", 5, 10, true, true, false, "1970-01-01T00:01:40Z")
	if err != nil || len(messages) != 5 {
		t.Fatalf("filtered page %#v %v", messages, err)
	}
	for i, message := range messages {
		if message.ID != fmt.Sprint(21+2*i) || message.Read || !message.Flagged || message.MessageSize != 0 || message.RFCMessageID != "" {
			t.Fatalf("page row %d %#v", i, message)
		}
	}
	columns := map[string]int{}
	fixtureMessageProperties(f.events, func(code string, selector appleevents.Value) {
		if selector.Fields["seld"].Code != "all " {
			t.Fatalf("moderate mailbox metadata used repeated selector %#v", selector)
		}
		if code == "ctnt" || code == "raso" || code == "pALL" || code == "msze" || code == "meid" {
			t.Fatalf("ordinary listing requested %s", code)
		}
		columns[code]++
	})
	for _, code := range nativeListMessageCodes {
		want := 1
		if code == "ID  " {
			want = 2
		}
		if columns[code] != want {
			t.Fatalf("column %s read %d times, want %d; %#v", code, columns[code], want, columns)
		}
	}
}

func TestNativeReadExactIDChangedAfterMetadataFails(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	c.shared.nativeSender = func(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
		value, stats, err := f.sender(ctx, event)
		direct := event.Params["----"]
		if direct.Kind == "list" && len(direct.Items) > 0 && direct.Items[0].Fields["from"].Fields["want"].Code == "mssg" && direct.Items[0].Fields["from"].Fields["form"].Code == "ID  " {
			f.properties["mssg:42"]["ID  "] = appleevents.Int(99)
		}
		return value, stats, err
	}
	messages, err := c.nativeMessages("Personal", "INBOX", 1, 0, false, false, false, "")
	if err == nil || len(messages) != 0 {
		t.Fatalf("changed ID produced metadata %#v %v", messages, err)
	}
}

func TestNativeReadBulkPageRejectsArrivalAndReorder(t *testing.T) {
	for _, change := range []string{"arrival", "reorder"} {
		t.Run(change, func(t *testing.T) {
			f := newNativeReadFixture()
			c := fixtureClient(t, f)
			fixtureMessages(f, 100, false)
			changed := false
			c.shared.nativeSender = func(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
				value, stats, err := f.sender(ctx, event)
				direct := event.Params["----"]
				if direct.Fields["seld"].Code == "isfl" && direct.Fields["from"].Fields["seld"].Code == "all " {
					changed = true
					if change == "arrival" {
						f.messages["mbxp:inbox"] = append(f.messages["mbxp:inbox"], fixtureRef("mssg", "101"))
						f.properties["mssg:101"] = map[string]appleevents.Value{"ID  ": appleevents.Int(101)}
					} else {
						refs := f.messages["mbxp:inbox"]
						refs[0], refs[1] = refs[1], refs[0]
					}
				}
				return value, stats, err
			}
			messages, err := c.nativeMessages("Personal", "INBOX", 5, 10, false, false, false, "")
			if !changed || err == nil || len(messages) != 0 {
				t.Fatalf("changed collection accepted changed=%v rows=%#v err=%v", changed, messages, err)
			}
		})
	}
}

func TestNativeReadLargeMailboxUsesOnlySelectedExactMetadata(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	fixtureMessages(f, nativeBulkMetadataMessageLimit+1, false)
	messages, err := c.nativeMessages("Personal", "INBOX", 2, 10, false, false, false, "")
	if err != nil || len(messages) != 2 || messages[0].ID != "11" || messages[1].ID != "12" {
		t.Fatalf("large mailbox page %#v %v", messages, err)
	}
	exactIDs := map[int64]bool{}
	columns := map[string]int{}
	fixtureMessageProperties(f.events, func(code string, selector appleevents.Value) {
		if selector.Fields["seld"].Code == "all " {
			if code != "ID  " && code != "isdl" {
				t.Fatalf("large mailbox amplified %s", code)
			}
			columns[code]++
			return
		}
		if selector.Fields["form"].Code != "ID  " {
			t.Fatalf("expensive message selector %#v", selector)
		}
		exactIDs[selector.Fields["seld"].Number] = true
	})
	if len(exactIDs) != 2 || !exactIDs[11] || !exactIDs[12] || columns["ID  "] != 2 || columns["isdl"] != 1 {
		t.Fatalf("unexpected selected metadata IDs=%#v columns=%#v", exactIDs, columns)
	}
}

func TestNativeReadBulkMetadataFailureDoesNotReplayPermissions(t *testing.T) {
	for _, code := range []int{-1743, -1712, -1728, -1700} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newNativeReadFixture()
			c := fixtureClient(t, f)
			fixtureMessages(f, 10, false)
			failure := &appleevents.Error{Code: code, Message: "fixture column failure"}
			f.fail = func(event appleevents.Event) error {
				direct := event.Params["----"]
				if direct.Fields["seld"].Code == "subj" && direct.Fields["from"].Fields["seld"].Code == "all " {
					return failure
				}
				return nil
			}
			messages, err := c.nativeMessages("Personal", "INBOX", 2, 0, false, false, false, "")
			exactReads := 0
			fixtureMessageProperties(f.events, func(_ string, selector appleevents.Value) {
				if selector.Fields["form"].Code == "ID  " {
					exactReads++
				}
				if selector.Fields["form"].Code == "test" || selector.Fields["form"].Code == "rang" {
					t.Fatal("fallback used expensive projection")
				}
			})
			if code != -1700 {
				if !errors.Is(err, failure) || len(messages) != 0 || exactReads != 0 {
					t.Fatalf("permission replay rows=%#v exact=%d err=%v", messages, exactReads, err)
				}
			} else if err != nil || len(messages) != 2 || messages[0].ID != "1" || messages[1].ID != "2" || exactReads == 0 {
				t.Fatalf("unsupported column fallback rows=%#v exact=%d err=%v", messages, exactReads, err)
			}
		})
	}
}

func TestNativeReadEmptyPageChecksFinalCollection(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	f.properties["mssg:42"]["isrd"] = appleevents.Bool(true)
	messages, err := c.nativeMessages("Personal", "INBOX", 5, 0, true, false, false, "")
	if err != nil || messages == nil || len(messages) != 0 {
		t.Fatalf("empty page %#v %v", messages, err)
	}
	idReads := 0
	fixtureMessageProperties(f.events, func(code string, _ appleevents.Value) {
		if code == "ID  " {
			idReads++
		}
		if code != "ID  " && code != "isdl" && code != "isrd" {
			t.Fatalf("empty page requested %s", code)
		}
	})
	if idReads != 2 {
		t.Fatalf("empty page skipped final IDs: %d", idReads)
	}
}

func TestNativeReadBulkMetadataRejectsMalformedColumns(t *testing.T) {
	for _, shape := range []string{"count", "selected_field"} {
		t.Run(shape, func(t *testing.T) {
			f := newNativeReadFixture()
			c := fixtureClient(t, f)
			fixtureMessages(f, 10, false)
			c.shared.nativeSender = func(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
				value, stats, err := f.sender(ctx, event)
				direct := event.Params["----"]
				if direct.Fields["seld"].Code == "subj" && direct.Fields["from"].Fields["seld"].Code == "all " {
					if shape == "count" {
						value.Items = value.Items[:9]
					} else {
						value.Items[0] = appleevents.Null()
					}
				}
				return value, stats, err
			}
			messages, err := c.nativeMessages("Personal", "INBOX", 2, 0, false, false, false, "")
			if err == nil || len(messages) != 0 {
				t.Fatalf("malformed %s accepted %#v %v", shape, messages, err)
			}
		})
	}
}

func TestNativeReadReplyHeadersWithoutRendering(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	message := &Message{}
	err := c.withNativeAutomation(time.Second, func(n *nativeMail) error {
		ref, err := n.scopedMessage("Personal", "INBOX", "42")
		if err != nil {
			return err
		}
		return n.messageHeaders(ref, message)
	})
	if err != nil || message.InReplyTo != "<parent@example.test>" || message.References != "<ancestor@example.test> <parent@example.test>" {
		t.Fatalf("headers %#v %v", message, err)
	}
	assertNoNativeContentRead(t, f.events)
	delete(f.properties["mssg:42"], "alhe")
	err = c.withNativeAutomation(time.Second, func(n *nativeMail) error {
		ref, err := n.scopedMessage("Personal", "INBOX", "42")
		if err != nil {
			return err
		}
		return n.messageHeaders(ref, &Message{})
	})
	if err != nil {
		t.Fatalf("optional missing headers %v", err)
	}
	permission := &appleevents.Error{Code: -1743, Message: "permission denied"}
	f.fail = func(event appleevents.Event) error {
		if event.Params["----"].Fields["seld"].Code == "alhe" {
			return permission
		}
		return nil
	}
	err = c.withNativeAutomation(time.Second, func(n *nativeMail) error {
		ref, err := n.scopedMessage("Personal", "INBOX", "42")
		if err != nil {
			return err
		}
		return n.messageHeaders(ref, &Message{})
	})
	if !errors.Is(err, permission) {
		t.Fatalf("permission suppressed for headers %v", err)
	}
}

func TestNativeReadResolvedMailboxSurvivesIndexReorder(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	reordered := false
	c.shared.nativeSender = func(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
		value, stats, err := f.sender(ctx, event)
		direct := event.Params["----"]
		if !reordered && event.ID == "getd" && direct.Kind == "object" && direct.Fields["want"].Code == "mbxp" && direct.Fields["form"].Code == "indx" && direct.Fields["seld"].Kind == "int" {
			reordered = true
			f.boxes["mact:a"][0], f.boxes["mact:a"][1] = f.boxes["mact:a"][1], f.boxes["mact:a"][0]
		}
		return value, stats, err
	}
	if err := c.MarkMessageAsRead("Personal", "INBOX", "42", true); err != nil {
		t.Fatal(err)
	}
	if !reordered {
		t.Fatal("fixture never changed index ordering")
	}
	last := f.events[len(f.events)-1]
	box := last.Params["----"].Fields["from"].Fields["from"]
	if last.ID != "setd" || box.Kind != "opaque" || box.Data != "mbxp:inbox" {
		t.Fatalf("mutation retained drifting mailbox index %#v", last)
	}
}

func TestNativeReadResolvedWrongCanonicalMailboxStopsWrite(t *testing.T) {
	f := newNativeReadFixture()
	c := fixtureClient(t, f)
	c.shared.nativeSender = func(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
		direct := event.Params["----"]
		if event.ID == "getd" && direct.Kind == "object" && direct.Fields["want"].Code == "mbxp" && direct.Fields["form"].Code == "indx" && direct.Fields["seld"].Kind == "int" {
			return appleevents.Value{Kind: "opaque", Data: "mbxp:archive"}, appleevents.Stats{Attempted: true}, nil
		}
		return f.sender(ctx, event)
	}
	if err := c.MarkMessageAsRead("Personal", "INBOX", "42", true); err == nil {
		t.Fatal("resolved wrong canonical mailbox accepted")
	}
	for _, event := range f.events {
		if event.ID == "setd" || event.ID == "delo" || event.ID == "move" {
			t.Fatal("write dispatched after mailbox identity changed")
		}
	}
}

func TestNativeReadListProjectionAvoidsStoredIdentityAndSize(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			f := newNativeReadFixture()
			c := fixtureClient(t, f)
			row := map[string]appleevents.Value{}
			for code, value := range f.properties["mssg:42"] {
				row[code] = value
			}
			row["ID  "] = appleevents.Int(43)
			f.properties["mssg:43"] = row
			f.messages["mbxp:inbox"] = append(f.messages["mbxp:inbox"], fixtureRef("mssg", "43"))
			expensive := errors.New("stored identity or size was requested")
			var expensiveProperty func(appleevents.Value) bool
			expensiveProperty = func(value appleevents.Value) bool {
				if value.Kind == "object" && value.Fields["want"].Code == "prop" && (value.Fields["seld"].Code == "meid" || value.Fields["seld"].Code == "msze") {
					return true
				}
				for _, item := range value.Items {
					if expensiveProperty(item) {
						return true
					}
				}
				for _, field := range value.Fields {
					if expensiveProperty(field) {
						return true
					}
				}
				return false
			}
			f.fail = func(event appleevents.Event) error {
				if expensiveProperty(event.Params["----"]) {
					return expensive
				}
				return nil
			}
			messages, err := c.nativeMessages("Personal", "INBOX", limit, 0, false, false, false, "")
			if err != nil || len(messages) != limit {
				t.Fatalf("light listing %#v %v", messages, err)
			}
			for _, message := range messages {
				if message.RFCMessageID != "" || message.MessageSize != 0 {
					t.Fatalf("legacy list projection changed: %#v", message)
				}
			}
			// Verification still requests the complete identity rather than silently
			// taking the lightweight listing model as sufficient mutation evidence.
			if _, err := c.GetMessageStateForVerificationJSON("Personal", "INBOX", "42"); !errors.Is(err, expensive) {
				t.Fatalf("verification lost full identity projection: %v", err)
			}
			f.fail = nil
			message, err := c.GetMessageStateForVerificationJSON("Personal", "INBOX", "42")
			if err != nil || message == nil || message.RFCMessageID != "<fixture@example.test>" || message.MessageSize != 100 {
				t.Fatalf("full verification metadata %#v %v", message, err)
			}
		})
	}
}

func TestNativeReadAccountProjectionPreservesIdentityBracket(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	for _, scenario := range []string{"changed ID", "final shape", "duplicate ID"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNativeReadFixture()
			c := fixtureClient(t, f)
			if scenario == "duplicate ID" {
				f.accounts = append(f.accounts, f.accounts[0])
			}
			idReads := 0
			c.shared.nativeSender = func(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
				object := event.Params["----"]
				if object.Fields["seld"].Code == "ID  " {
					idReads++
					if idReads == 2 && scenario != "duplicate ID" {
						f.events = append(f.events, event)
						value := appleevents.List(appleevents.String("changed"))
						if scenario == "final shape" {
							value = appleevents.String("a")
						}
						return value, appleevents.Stats{}, nil
					}
				}
				return f.sender(ctx, event)
			}
			_, err := c.GetAccountsJSON()
			want := "account collection changed during metadata read"
			if scenario == "duplicate ID" {
				want = `Mail returned duplicate account ID "a"`
			}
			if err == nil || err.Error() != want || c.shared.accountsLoaded {
				t.Fatalf("identity failure accepted: error=%v cached=%v", err, c.shared.accountsLoaded)
			}
			codes := []string{"ID  ", "pnam", "emad", "unme", "isac", "atyp", "ID  "}
			if len(f.events) != len(codes) {
				t.Fatalf("projection event count=%d expected=%d", len(f.events), len(codes))
			}
			for i, event := range f.events {
				if event.ID != "getd" || event.Params["----"].Fields["seld"].Code != codes[i] {
					t.Fatalf("projection event %d changed: %+v", i, event)
				}
			}
		})
	}
}
