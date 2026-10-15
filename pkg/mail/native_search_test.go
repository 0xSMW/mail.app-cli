package mail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

type nativeSearchFixture struct {
	*nativeReadFixture
	messages     []appleevents.Value
	queryError   error
	invalidReply bool
}

func (f *nativeSearchFixture) matches(test, ref appleevents.Value) bool {
	if test.Kind == "logical" {
		operator := test.Fields["logc"].Code
		for _, term := range test.Fields["term"].Items {
			match := f.matches(term, ref)
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
	case "cont":
		return strings.Contains(strings.ToLower(actual.Text), strings.ToLower(wanted.Text))
	case "=   ":
		return reflect.DeepEqual(actual, wanted)
	case ">=  ":
		return actual.Text >= wanted.Text
	}
	return false
}

func (f *nativeSearchFixture) sender(ctx context.Context, event appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
	selector := event.Params["----"]
	if event.ID == "getd" && selector.Kind == "object" && selector.Fields["want"].Code == "mssg" && selector.Fields["form"].Code == "test" {
		f.events = append(f.events, event)
		if f.queryError != nil {
			return appleevents.Value{}, appleevents.Stats{Attempted: true}, f.queryError
		}
		if f.invalidReply {
			return appleevents.String("invalid"), appleevents.Stats{Attempted: true}, nil
		}
		var matches []appleevents.Value
		for _, ref := range f.messages {
			if f.matches(selector.Fields["seld"], ref) {
				matches = append(matches, ref)
			}
		}
		return appleevents.List(matches...), appleevents.Stats{Attempted: true, Events: 1}, nil
	}
	return f.nativeReadFixture.sender(ctx, event)
}
func newNativeSearchFixture(t *testing.T) (*nativeSearchFixture, *Client) {
	f := &nativeSearchFixture{nativeReadFixture: newNativeReadFixture(), messages: []appleevents.Value{fixtureRef("mssg", "42")}}
	c := fixtureClient(t, f.nativeReadFixture)
	c.shared.nativeSender = f.sender
	return f, c
}
func forbidNativeScripts(t *testing.T) {
	t.Helper()
	writeFakeOsaScript(t, `exit 91`)
}
func assertNoNativeContentRead(t *testing.T, events []appleevents.Event) {
	t.Helper()
	var check func(appleevents.Value)
	check = func(v appleevents.Value) {
		if v.Kind == "object" && v.Fields["want"].Code == "prop" && v.Fields["seld"].Code == "ctnt" && v.Fields["from"].Kind != "examined" {
			t.Fatal("rendered content instead of predicate matching")
		}
		for _, item := range v.Items {
			check(item)
		}
		for _, item := range v.Fields {
			check(item)
		}
	}
	for _, event := range events {
		check(event.Params["----"])
	}
}

func TestNativeSearchPredicatePreservesAndOrSince(t *testing.T) {
	f, c := newNativeSearchFixture(t)
	forbidNativeScripts(t)
	for _, tc := range []struct {
		query, since string
		want         int
	}{
		{"fixture sender", "", 1}, {"fixture body", "", 1}, {"fixture missing", "", 0}, {"fixture", "1970-01-01T00:01:41Z", 0},
	} {
		messages, err := c.searchMessagesInSingleMailboxJXA(tc.query, "Personal", "INBOX", 1, tc.since)
		if err != nil || len(messages) != tc.want {
			t.Fatalf("%s: %#v %v", tc.query, messages, err)
		}
	}
	messages, err := c.SearchMessages("Fixture Body", 1)
	if err != nil || len(messages) != 0 {
		t.Fatalf("phrase OR widened into AND terms: %#v %v", messages, err)
	}
	messages, err = c.searchArchiveMailboxWithWhoseJXA("fixture body", "Personal", "INBOX", 1, "")
	if err != nil || len(messages) != 0 {
		t.Fatalf("metadata archive search rendered body: %#v %v", messages, err)
	}
	assertNoNativeContentRead(t, f.events)
}

func TestNativeSearchFailsClosedOnNativeFault(t *testing.T) {
	f, c := newNativeSearchFixture(t)
	forbidNativeScripts(t)
	permission := &appleevents.Error{Code: -1743, Message: "permission denied"}
	f.queryError = permission
	if _, err := c.nativeSearchMailbox("fixture", "Personal", "INBOX", 1, "", true); !errors.Is(err, permission) {
		t.Fatalf("permission became empty success: %v", err)
	}
	f.queryError = context.DeadlineExceeded
	if _, err := c.nativeSearchMailbox("fixture", "Personal", "INBOX", 1, "", true); !isAutomationTimeout(err) {
		t.Fatalf("deadline lost: %v", err)
	}
	f.queryError = nil
	f.invalidReply = true
	if _, err := c.nativeSearchMailbox("fixture", "Personal", "INBOX", 1, "", true); err == nil {
		t.Fatal("malformed reply became empty success")
	}
	f.invalidReply = false
	if _, err := c.nativeSearchMailbox("fixture", "Personal", "Missing", 1, "", true); !IsNotFound(err) {
		t.Fatalf("missing scope lost: %v", err)
	}
}

func TestNativeIdentityFallbackUniqueExactWithoutBody(t *testing.T) {
	f, c := newNativeSearchFixture(t)
	forbidNativeScripts(t)
	identity := StableIdentityFromMessage(*nativeMessageFields(f.properties["mssg:42"], "Personal", "INBOX"))
	present, err := c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", identity)
	if err != nil || !present {
		t.Fatalf("identity: %v %v", present, err)
	}
	wrong := identity
	wrong.RFCMessageID = "<wrong@example.test>"
	present, err = c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", wrong)
	if err != nil || present {
		t.Fatalf("wrong RFC identity: %v %v", present, err)
	}
	second := make(map[string]appleevents.Value)
	for code, value := range f.properties["mssg:42"] {
		second[code] = value
	}
	second["ID  "] = appleevents.Int(43)
	f.properties["mssg:43"] = second
	f.messages = append(f.messages, fixtureRef("mssg", "43"))
	if _, err = c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", identity); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate identity accepted: %v", err)
	}
	assertNoNativeContentRead(t, f.events)
}

func TestNativeSelectedReadsAndIdentityCaptureSkipScriptSession(t *testing.T) {
	f, c := newNativeSearchFixture(t)
	forbidNativeScripts(t)
	c.shared.bodyFromMail = true
	result := c.ReadSelectedMessages([]MessageRef{{AccountName: "Personal", MailboxName: "INBOX", MessageID: "42"}}, time.Second, 2*time.Second)
	if !result.Complete || len(result.Items) != 1 || result.Items[0].Message.Content != "Body" {
		t.Fatalf("selected read: %#v", result)
	}
	items := []BatchItem{{ID: "42", Account: "Personal", SourceMailbox: "INBOX"}, {ID: "42", Account: "Personal", SourceMailbox: "INBOX"}}
	if err := captureBatchIdentities(c, BatchOptions{Action: "move", ReuseBridge: true}, items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if !item.Identity.valid() {
			t.Fatalf("capture failed: %#v", item)
		}
	}
	if c.session != nil {
		t.Fatal("native path created JXA session")
	}
	_ = f
}

func TestNativeRecentEnvelopeAndGmailCapabilitySkipScripts(t *testing.T) {
	f, c := newNativeSearchFixture(t)
	forbidNativeScripts(t)
	message, err := c.getMessageEnvelopeJXA("Personal", "INBOX", "42")
	if err != nil || message == nil || message.ID != "42" {
		t.Fatalf("envelope: %#v %v", message, err)
	}
	gmail, err := c.isGmailAccount("Personal")
	if err != nil || !gmail {
		t.Fatalf("capability: %v %v", gmail, err)
	}
	assertNoNativeContentRead(t, f.events)
	f.fail = func(appleevents.Event) error { return fmt.Errorf("transport unavailable") }
	if _, err = c.isGmailAccount("Personal"); err == nil {
		t.Fatal("capability swallowed transport failure")
	}
}

func TestNativeSearchIndexUnavailableUsesNativeScopedRoute(t *testing.T) {
	f, c := newNativeSearchFixture(t)
	forbidNativeScripts(t)
	result, err := c.SearchMessagesJSONSinceWithOptions("fixture body", "Personal", "INBOX", 1, "", SearchOptions{})
	if err != nil || !result.Complete || len(result.Messages) != 1 || len(result.SearchedMailboxes) != 1 {
		t.Fatalf("index fallback: %#v %v", result, err)
	}
	if result.Messages[0].Account != "Personal" || result.Messages[0].Mailbox != "INBOX" {
		t.Fatalf("fallback broadened scope: %#v", result.Messages)
	}
	identity := StableIdentityFromMessage(result.Messages[0])
	identity.RFCMessageID = ""
	verified := VerifyMutations(c, BatchOptions{Action: "move", Verify: true}, []BatchItem{{ID: "42", Account: "Personal", SourceMailbox: "INBOX", TargetMailbox: "INBOX", Status: "succeeded", Identity: identity}})
	observed := verified[0].Observed
	if verified[0].VerifyStatus != "present_in_source" || observed == nil || !observed.SourcePresent || !observed.DestinationPresent {
		t.Fatalf("index identity fallback: %+v", verified[0])
	}
	if _, err := c.SearchMessagesJSONSinceWithOptions("fixture", "Personal", "", 1, "", SearchOptions{}); err == nil {
		t.Fatal("unbounded index-unavailable search permitted")
	}
	assertNoNativeContentRead(t, f.events)
}

func nativeIndexedSearchFixture(t *testing.T) (*Client, *nativeReadFixture) {
	t.Helper()
	c, _, _ := localReadFixture(t)
	c.ResetAccountCache()
	f := newNativeReadFixture()
	f.properties["mact:a"]["ID  "] = appleevents.String("ABC")
	f.properties["mact:a"]["pnam"] = appleevents.String("Work")
	c.shared.nativeSender = f.sender
	return c, f
}

func TestNativeSearchCrossMailboxPrimesOneAccountSnapshot(t *testing.T) {
	c, f := nativeIndexedSearchFixture(t)
	binary, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	forbidNativeScripts(t)
	var inserts strings.Builder
	for i := 0; i < 370; i++ {
		fmt.Fprintf(&inserts, "insert into mailboxes(url,total_count,unread_count) values('imap://ABC/Label%03d',1,0);\n", i)
	}
	if output, err := exec.Command(binary, os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), inserts.String()).CombinedOutput(); err != nil {
		t.Fatalf("synthetic mailbox fixture: %v %s", err, output)
	}
	result, err := c.SearchMessagesJSONSinceWithOptions("indexed", "Work", "", 500, "", SearchOptions{})
	if err != nil || !result.Complete || len(result.SearchedMailboxes) != 372 || len(result.Messages) != 5 {
		t.Fatalf("indexed cross-mailbox result targets=%d rows=%#v complete=%v err=%v", len(result.SearchedMailboxes), result.Messages, result.Complete, err)
	}
	if len(f.events) != 2 || c.shared.accountsLoaded || c.readAccountSnapshot != nil {
		t.Fatalf("370 mailbox resolutions repeated account reads: events=%d loaded=%v", len(f.events), c.shared.accountsLoaded)
	}
	for _, target := range result.SearchedMailboxes {
		if target.Account != "Work" {
			t.Fatalf("broadened account scope %#v", target)
		}
	}
	seen := map[string]bool{}
	for _, message := range result.Messages {
		if message.Account != "Work" || message.ID == "102" {
			t.Fatalf("cross-account index result %#v", message)
		}
		seen[message.ID] = true
	}
	for _, id := range []string{"101", "103", "104", "105", "106"} {
		if !seen[id] {
			t.Fatalf("snapshot changed indexed result IDs: %#v", seen)
		}
	}
	assertNoNativeContentRead(t, f.events)
	// A separate invocation resolves a fresh named snapshot; it does not leak
	// a partial account list into the shared full-account cache.
	if _, err := c.SearchMessagesJSONSinceWithOptions("indexed", "Work", "", 500, "", SearchOptions{}); err != nil || len(f.events) != 4 || c.shared.accountsLoaded || c.readAccountSnapshot != nil {
		t.Fatalf("invocation snapshot leaked: events=%d loaded=%v snapshot=%v err=%v", len(f.events), c.shared.accountsLoaded, c.readAccountSnapshot, err)
	}
}

func TestNativeSearchCrossMailboxSnapshotRejectsAmbiguityAndErrors(t *testing.T) {
	t.Run("ambiguity", func(t *testing.T) {
		c, f := nativeIndexedSearchFixture(t)
		other := fixtureRef("mact", "other")
		f.accounts = append(f.accounts, other)
		fields := map[string]appleevents.Value{}
		for code, value := range f.properties["mact:a"] {
			fields[code] = value
		}
		fields["ID  "] = appleevents.String("OTHER")
		f.properties["mact:other"] = fields
		result, err := c.SearchMessagesJSONSinceWithOptions("indexed", "Work", "", 500, "", SearchOptions{})
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || len(result.Messages) != 0 || len(f.events) != 1 || c.shared.accountsLoaded {
			t.Fatalf("ambiguous snapshot accepted: result=%#v events=%d err=%v", result, len(f.events), err)
		}
	})
	t.Run("permission", func(t *testing.T) {
		c, f := nativeIndexedSearchFixture(t)
		permission := &appleevents.Error{Code: -1743, Message: "permission denied"}
		f.fail = func(event appleevents.Event) error {
			if event.Params["----"].Fields["seld"].Code == "pALL" {
				return permission
			}
			return nil
		}
		result, err := c.SearchMessagesJSONSinceWithOptions("indexed", "Work", "", 500, "", SearchOptions{AllowPartial: true})
		if !errors.Is(err, permission) || len(result.Messages) != 0 || c.shared.accountsLoaded || len(f.events) != 2 {
			t.Fatalf("snapshot failure swallowed or cached: result=%#v events=%d loaded=%v err=%v", result, len(f.events), c.shared.accountsLoaded, err)
		}
	})
}

func TestNativeSearchSpecificMailboxKeepsExactAccountResolution(t *testing.T) {
	c, f := nativeIndexedSearchFixture(t)
	result, err := c.SearchMessagesJSONSinceWithOptions("indexed", "Work", "INBOX", 500, "", SearchOptions{})
	if err != nil || !result.Complete || len(result.SearchedMailboxes) != 1 || len(result.Messages) != 5 || len(f.events) != 2 || c.shared.accountsLoaded {
		t.Fatalf("scoped branch changed: result=%#v events=%d loaded=%v err=%v", result, len(f.events), c.shared.accountsLoaded, err)
	}
}

func nativeSearchOtherAccount(f *nativeReadFixture) {
	other := fixtureRef("mact", "other")
	f.accounts = append(f.accounts, other)
	fields := map[string]appleevents.Value{}
	for code, value := range f.properties["mact:a"] {
		fields[code] = value
	}
	fields["ID  "] = appleevents.String("OTHER")
	fields["pnam"] = appleevents.String("Other")
	f.properties["mact:other"] = fields
}

func TestNativeSearchNamedSnapshotIgnoresUnrelatedMalformedAccount(t *testing.T) {
	c, f := nativeIndexedSearchFixture(t)
	nativeSearchOtherAccount(f)
	f.properties["mact:other"]["unme"] = appleevents.Null()
	result, err := c.SearchMessagesJSONSinceWithOptions("indexed", "Work", "", 500, "", SearchOptions{})
	if err != nil || !result.Complete || len(result.Messages) != 5 || len(f.events) != 2 || c.shared.accountsLoaded {
		t.Fatalf("unrelated metadata blocked named scope: result=%#v events=%d loaded=%v err=%v", result, len(f.events), c.shared.accountsLoaded, err)
	}
	for _, event := range f.events {
		direct := event.Params["----"]
		if direct.Fields["want"].Code == "prop" && fixtureKey(direct.Fields["from"]) != "mact:a" {
			t.Fatalf("named snapshot read unrelated fields %#v", direct)
		}
	}
}

func TestNativeSearchGlobalPrimesFullCacheAndNamedSearchPreservesIt(t *testing.T) {
	c, f := nativeIndexedSearchFixture(t)
	nativeSearchOtherAccount(f)
	result, err := c.SearchMessagesJSONSinceWithOptions("indexed", "", "", 500, "", SearchOptions{})
	if err != nil || !result.Complete || len(result.Messages) != 7 || len(f.events) != 7 || !c.shared.accountsLoaded {
		t.Fatalf("global snapshot result=%#v events=%d loaded=%v err=%v", result, len(f.events), c.shared.accountsLoaded, err)
	}
	result, err = c.SearchMessagesJSONSinceWithOptions("indexed", "Work", "", 500, "", SearchOptions{})
	if err != nil || !result.Complete || len(result.Messages) != 5 || len(f.events) != 7 || !c.shared.accountsLoaded || len(c.shared.accounts) != 2 || c.readAccountSnapshot != nil {
		t.Fatalf("named search changed full cache: result=%#v events=%d loaded=%v err=%v", result, len(f.events), c.shared.accountsLoaded, err)
	}
}

func TestNativeSearchInvocationSnapshotClonesAndPreservesOtherAccounts(t *testing.T) {
	c, f := nativeIndexedSearchFixture(t)
	nativeSearchOtherAccount(f)
	account, err := c.accountByName("Work")
	if err != nil {
		t.Fatal(err)
	}
	searchClient := *c
	searchClient.readAccountSnapshot = account
	scoped := searchClient.WithContext(context.Background())
	copy, err := scoped.accountByName("Work")
	if err != nil || len(f.events) != 2 {
		t.Fatalf("snapshot reread after WithContext: events=%d err=%v", len(f.events), err)
	}
	copy.EmailAddresses[0] = "changed@example.test"
	if account.EmailAddresses[0] != "a@example.test" || c.shared.accountsLoaded || c.readAccountSnapshot != nil {
		t.Fatalf("returned account mutated or leaked snapshot: account=%#v loaded=%v", account, c.shared.accountsLoaded)
	}
	// The named snapshot does not mask an unrelated account lookup, nor does
	// it become a partial global cache on the original client.
	other, err := scoped.accountByName("Other")
	if err != nil || other.Name != "Other" || len(f.events) != 4 {
		t.Fatalf("snapshot masked Other: account=%#v events=%d err=%v", other, len(f.events), err)
	}
	accounts, err := c.GetAccountsJSON()
	if err != nil || len(accounts) != 2 || accounts[0].Name != "Work" || accounts[1].Name != "Other" || accounts[0].EmailAddresses[0] != "a@example.test" || len(f.events) != 11 {
		t.Fatalf("snapshot poisoned later global cache: accounts=%#v events=%d err=%v", accounts, len(f.events), err)
	}
}
