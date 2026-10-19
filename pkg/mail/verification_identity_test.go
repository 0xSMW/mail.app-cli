package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

// Install into the test's existing PATH directory so its isolated sqlite shim
// remains available. Every live identity read must resolve a single local ID.
func installVerificationIdentityBridge(t *testing.T, message Message, failure string) {
	t.Helper()
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n"
	if failure != "" {
		script += "printf '%s\\n' '" + failure + "' >&2\nexit 1\n"
	} else {
		script += fmt.Sprintf(`case "$*" in
 *"const wanted ="*|*"msg.content()"*) printf 'unexpected broad or content read' >&2; exit 1 ;;
 *"mbox.messages.byId(Number("*) printf '%%s\n' '%s' ;;
 *) printf 'unexpected bridge request' >&2; exit 1 ;;
esac
`, encoded)
	}
	if err := os.WriteFile(filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "osascript"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestRFCIdentityCanonicalization(t *testing.T) {
	for _, input := range []string{"stable@example.com", "<stable@example.com>", "  stable@example.com  ", "left.part+tag@host"} {
		want := "<" + strings.Trim(strings.TrimSpace(input), "<>") + ">"
		if got := validRFCMessageID(input); got != want {
			t.Errorf("%q = %q; want %q", input, got, want)
		}
	}
	for _, input := range []string{"12345", "<12345>", "x@", "@host", "x@@host", "x y@host", "<x@host", "x@host>", "<x@host> tail", "<x@host><y@host>", "x\r\n@host", "\n<x@host>", "x\x7f@host", "x..y@host", ".x@host", "x@host."} {
		if got := validRFCMessageID(input); got != "" {
			t.Errorf("invalid %q accepted as %q", input, got)
		}
	}
}

func TestJXARFCIdentityCanonicalizationMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	values := []string{"stable@example.com", "<stable@example.com>", "12345", "x@@host", "x\n@host", "x..y@host", "<x@host> tail", "x+y@host"}
	input, _ := json.Marshal(values)
	script := jxaRFCMessageIDHelper() + "\nconsole.log(JSON.stringify(" + string(input) + ".map(value => rfcMessageId({messageId: () => value}))));"
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v: %s", err, output)
	}
	var got []string
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	for i, value := range values {
		if got[i] != validRFCMessageID(value) {
			t.Errorf("JXA %q=%q; Go=%q", value, got[i], validRFCMessageID(value))
		}
	}
}

func TestVerificationSnapshotRFCIdentityAllowsSizeChangeOnlyAfterHeaderRead(t *testing.T) {
	for _, tc := range []struct {
		name, header, failure string
		want                  int
		wantErr               bool
	}{
		{name: "bare stable header", header: "stable@example.com", want: 1},
		{name: "bracketed stable header", header: "<stable@example.com>", want: 1},
		{name: "different header", header: "other@example.com"},
		{name: "missing header", wantErr: true},
		{name: "invalid header", header: "12345", wantErr: true},
		{name: "metadata read failed", failure: "permission denied", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := snapshotSQLiteFixture(t, `insert into mailboxes values ('ews://ABC/INBOX');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,1,1,1,1787961600,26484,1,0);`)
			scope := snapshotScope("Work", "INBOX")
			scope.identities[0].MessageSize = 26476
			scope.identities[0].RFCMessageID = "stable@example.com"
			installVerificationIdentityBridge(t, Message{ID: "205", RFCMessageID: tc.header, Subject: "subject", Sender: "sender@example.com", DateSent: "2026-08-29T00:00:00Z", MessageSize: 26484}, tc.failure)
			result := client.getIdentitySnapshotForVerification([]identityVerificationScope{scope})[scope.key]
			if (result.err != nil) != tc.wantErr {
				t.Fatalf("error=%v; wantErr=%v", result.err, tc.wantErr)
			}
			if got := len(result.matches[scope.identities[0].verificationKey()]); got != tc.want {
				t.Fatalf("matches=%d; want %d", got, tc.want)
			}
		})
	}
}

func TestVerificationSnapshotNoRFCStillRequiresSize(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `insert into mailboxes values ('ews://ABC/INBOX');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,1,1,1,1787961600,43,1,0);`)
	scope := snapshotScope("Work", "INBOX")
	got := snapshotMessages(t, client.getIdentitySnapshotForVerification([]identityVerificationScope{scope}), scope)
	if len(got) != 0 {
		t.Fatalf("changed size matched without RFC: %+v", got)
	}
}

func TestVerificationSnapshotRFCCandidateCapFailsClosed(t *testing.T) {
	var data strings.Builder
	data.WriteString("insert into mailboxes values ('ews://ABC/INBOX');")
	for i := 1; i <= maxVerificationIdentityCandidates+1; i++ {
		fmt.Fprintf(&data, "insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (%d,1,1,1,1787961600,43,1,0);", i)
	}
	client, _ := snapshotSQLiteFixture(t, data.String())
	scope := snapshotScope("Work", "INBOX")
	scope.identities[0].RFCMessageID = "stable@example.com"
	result := client.getIdentitySnapshotForVerification([]identityVerificationScope{scope})[scope.key]
	if result.err == nil || !strings.Contains(result.err.Error(), "too many") {
		t.Fatalf("cap must fail before metadata reads: %v", result.err)
	}
}

func TestNativeRFCIdentitySizeChangeAndAmbiguity(t *testing.T) {
	f, c := newNativeSearchFixture(t)
	forbidNativeScripts(t)
	fields := f.properties["mssg:42"]
	fields["sndr"] = appleevents.String("Sender <sender@example.test>")
	fields["meid"] = appleevents.String("stable@example.com")
	identity := StableIdentityFromMessage(*nativeMessageFields(fields, "Personal", "INBOX"))
	fields["msze"] = appleevents.Int(int64(identity.MessageSize + 8))
	present, err := c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", identity)
	if err != nil || !present {
		t.Fatalf("changed size + matching RFC: %v %v", present, err)
	}
	noRFC := identity
	noRFC.RFCMessageID = ""
	present, err = c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", noRFC)
	if err != nil || present {
		t.Fatalf("changed size without RFC: %v %v", present, err)
	}
	fields["meid"] = appleevents.String("other@example.com")
	present, err = c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", identity)
	if err != nil || present {
		t.Fatalf("different RFC: %v %v", present, err)
	}
	fields["meid"] = appleevents.String("")
	if _, err = c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", identity); err == nil {
		t.Fatal("missing RFC treated as absence")
	}
	fields["meid"] = appleevents.String("stable@example.com")
	second := make(map[string]appleevents.Value)
	for code, value := range fields {
		second[code] = value
	}
	second["ID  "] = appleevents.Int(43)
	f.properties["mssg:43"] = second
	f.messages = append(f.messages, fixtureRef("mssg", "43"))
	if _, err = c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", identity); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate RFC accepted: %v", err)
	}
	assertNoNativeContentRead(t, f.events)
}

func TestExchangeRelocationAndReconcileVerifyChangedSizeWithRFC(t *testing.T) {
	changed := strings.Replace(fakeIdentityRow, `"MessageSize":42`, `"MessageSize":26484`, 1)
	client := fakeRelocationIndex(t, "Archive", "INBOX", "ews://ABC/Archive", "[]", "["+changed+"]", false)
	installVerificationIdentityBridge(t, Message{ID: "205", RFCMessageID: "stable@example.com", Subject: "subject", Sender: "Renamed Sender <sender@example.com>", DateSent: "2026-08-29T07:00:00+07:00", MessageSize: 26484}, "")
	item := verificationItem()
	item.SourceMailbox, item.TargetMailbox, item.Status = "Archive", "INBOX", "succeeded"
	item.Identity.RFCMessageID, item.Identity.MessageSize = "<stable@example.com>", 26476
	item.Identity.Sender = "Original Sender <sender@example.com>"
	got := VerifyMutations(client, BatchOptions{Action: "move"}, []BatchItem{item})[0]
	if got.VerifyStatus != "confirmed_destination" || got.Observed == nil || !got.Observed.DestinationPresent || got.Observed.SourcePresent {
		t.Fatalf("Exchange size-changed move: %+v", got)
	}
	reconciled, err := ReconcileMutations(client, BatchOptions{Action: "move"}, BatchResult{Action: "move", Attempted: 1, Items: []BatchItem{item}})
	if err != nil || reconciled.Unverified != 0 || reconciled.Items[0].VerifyStatus != "confirmed_destination" {
		t.Fatalf("Exchange reconcile: %+v %v", reconciled, err)
	}
}

func TestRFCEnvelopeSenderAndDateGuards(t *testing.T) {
	identity := StableIdentity{RFCMessageID: "<stable@example.com>", Sender: "Original Sender <sender@example.com>", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 26476}
	message := Message{RFCMessageID: "stable@example.com", Sender: "Renamed <SENDER@example.com>", Subject: "subject", DateSent: "2026-08-29T07:00:00.999+07:00", MessageSize: 26484}
	if !identity.matchesEnvelope(message) {
		t.Fatal("same validated address/RFC and canonical second should match")
	}
	for _, sender := range []string{"other@example.com", "Sender", "sender@example.com, other@example.com"} {
		wrong := message
		wrong.Sender = sender
		if identity.matchesEnvelope(wrong) {
			t.Errorf("sender guard accepted %q", sender)
		}
	}
	wrong := message
	wrong.DateSent = "2026-08-29T00:00:01Z"
	if identity.matchesEnvelope(wrong) {
		t.Fatal("different sent second accepted")
	}
	withoutRFC := identity
	withoutRFC.RFCMessageID = ""
	if withoutRFC.matchesEnvelope(message) {
		t.Fatal("no-RFC identity lost strict sender/size guards")
	}
}

func TestNativeRFCIdentityCandidateCap(t *testing.T) {
	f, c := newNativeSearchFixture(t)
	fields := f.properties["mssg:42"]
	fields["sndr"] = appleevents.String("sender@example.com")
	identity := StableIdentityFromMessage(*nativeMessageFields(fields, "Personal", "INBOX"))
	for i := 0; i < maxVerificationIdentityCandidates; i++ {
		f.messages = append(f.messages, fixtureRef("mssg", "42"))
	}
	if _, err := c.hasMessageIdentityWithWhoseJXA("Personal", "INBOX", identity); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("native candidate cap: %v", err)
	}
	assertNoNativeContentRead(t, f.events)
}

func TestRFCRelocationSnapshotAmbiguityIsFinal(t *testing.T) {
	client := fakeRelocationIndex(t, "Archive", "INBOX", "ews://ABC/Archive", "[]", "["+fakeIdentityRow+","+fakeIdentityRow+"]", false)
	installVerificationIdentityBridge(t, Message{ID: "205", RFCMessageID: "stable@example.com", Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}, "")
	item := verificationItem()
	item.SourceMailbox, item.TargetMailbox, item.Status = "Archive", "INBOX", "succeeded"
	item.Identity.RFCMessageID = "<stable@example.com>"
	got := VerifyMutations(client, BatchOptions{Action: "move"}, []BatchItem{item})[0]
	if got.VerifyStatus != "applied_destination_unverified" || !strings.Contains(got.VerifyError, "ambiguous") {
		t.Fatalf("RFC snapshot ambiguity was retried or accepted: %+v", got)
	}
}

func TestStableSenderAddressExchangeDuplicateDisplay(t *testing.T) {
	identity := StableIdentity{RFCMessageID: "<stable@example.com>", Sender: "sender@example.test", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}
	for _, sender := range []string{"sender@example.test", "Sender Name <sender@example.test>", "sender@example.test <Sender@Example.Test>", "  sender@example.test <Sender@Example.Test>  "} {
		t.Run(sender, func(t *testing.T) {
			if got := stableSenderAddress(sender); got != "sender@example.test" {
				t.Fatalf("normalized sender=%q", got)
			}
			message := Message{RFCMessageID: identity.RFCMessageID, Sender: sender, Subject: identity.Subject, DateSent: identity.DateSent, MessageSize: 50}
			if !identity.matchesEnvelope(message) {
				t.Fatal("equivalent Exchange sender did not match guarded RFC identity")
			}
		})
	}
	for _, sender := range []string{"sender@example.test <other@example.test>", "sender@example.test <<sender@example.test>>", "sender@example.test <sender@example.test> trailing", "sender@example.test <sender@example.test>, other@example.test", "sender@example.test\r\n <sender@example.test>"} {
		t.Run("reject "+sender, func(t *testing.T) {
			if got := stableSenderAddress(sender); got != "" {
				t.Fatalf("malformed sender accepted as %q", got)
			}
			message := Message{RFCMessageID: identity.RFCMessageID, Sender: sender, Subject: identity.Subject, DateSent: identity.DateSent, MessageSize: 50}
			if identity.matchesEnvelope(message) {
				t.Fatal("malformed sender weakened RFC identity guard")
			}
		})
	}
}
