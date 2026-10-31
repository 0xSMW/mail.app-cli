package mail

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
)

func TestGmailMailStateUnicodeMembershipChanges(t *testing.T) {
	for _, label := range []string{"Café", "Cafe\u0301"} {
		for _, membership := range []string{"Café", "Cafe\u0301"} {
			for _, add := range []bool{true, false} {
				name := label + " vs " + membership
				if add {
					name += " add"
				} else {
					name += " remove"
				}
				t.Run(name, func(t *testing.T) {
					before := GmailLabelSnapshot{Read: true, Memberships: []string{"All Mail", "Autre e\u0301tiquette"}}
					after := GmailLabelSnapshot{Read: true, Memberships: []string{"All Mail", "Autre étiquette"}}
					if add {
						after.Memberships = append(after.Memberships, membership)
					} else {
						before.Memberships = append(before.Memberships, membership)
					}
					beforeNames := append([]string(nil), before.Memberships...)
					afterNames := append([]string(nil), after.Memberships...)
					if !gmailMailStateMatches(before, after, gmail.Label{ID: "Label_1", Name: label}, add) {
						t.Fatalf("canonical label change rejected: before=%q after=%q", before.Memberships, after.Memberships)
					}
					if !reflect.DeepEqual(before.Memberships, beforeNames) || !reflect.DeepEqual(after.Memberships, afterNames) {
						t.Fatalf("verification changed snapshot spelling: before=%q after=%q", before.Memberships, after.Memberships)
					}
				})
			}
		}
	}
}

func TestGmailMailStateUnicodeKeepsVerificationGuards(t *testing.T) {
	before := GmailLabelSnapshot{Read: true, Memberships: []string{"All Mail", "Other"}}
	for _, tc := range []struct {
		name  string
		label string
		after GmailLabelSnapshot
	}{
		{"read changed", "Café", GmailLabelSnapshot{Read: false, Memberships: []string{"All Mail", "Other", "Cafe\u0301"}}},
		{"unrelated label removed", "Café", GmailLabelSnapshot{Read: true, Memberships: []string{"All Mail", "Cafe\u0301"}}},
		{"unrelated label added", "Café", GmailLabelSnapshot{Read: true, Memberships: []string{"All Mail", "Other", "Extra", "Cafe\u0301"}}},
		{"case changed", "café", GmailLabelSnapshot{Read: true, Memberships: []string{"All Mail", "Other", "Café"}}},
		{"compatibility character", "1", GmailLabelSnapshot{Read: true, Memberships: []string{"All Mail", "Other", "①"}}},
		{"compatibility ligature", "ffi", GmailLabelSnapshot{Read: true, Memberships: []string{"All Mail", "Other", "ﬃ"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if gmailMailStateMatches(before, tc.after, gmail.Label{ID: "Label_1", Name: tc.label}, true) {
				t.Fatalf("unexpected label or read delta accepted: %+v", tc.after)
			}
		})
	}
}

func TestGmailProviderLabelIDsRemainByteExact(t *testing.T) {
	nfc, nfd := "Label_Café", "Label_Cafe\u0301"
	if sameLabels([]string{nfc}, []string{nfd}) || hasLabel([]string{nfc}, nfd) {
		t.Fatal("provider IDs matched across different byte spellings")
	}
	if got := labelSet([]string{nfc, nfd}); len(got) != 2 {
		t.Fatalf("provider IDs were collapsed: %q", got)
	}
	if got := desiredLabels([]string{nfd}, nfc, false); len(got) != 1 || got[0] != nfd {
		t.Fatalf("removing one provider ID removed another spelling: %q", got)
	}
}

func TestGmailMailStateUnicodeAmbiguityCannotHideMembershipDelta(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after []string
		label         string
		add           bool
	}{
		{"unrelated equivalent label lost", []string{"Café", "Cafe\u0301"}, []string{"Café", "Other"}, "Other", true},
		{"unrelated equivalent label added", []string{"Café", "Other"}, []string{"Café", "Cafe\u0301"}, "Other", false},
		{"one equivalent target remains", []string{"Café", "Cafe\u0301"}, []string{"Café"}, "Café", false},
		{"ambiguous noop", []string{"Café", "Cafe\u0301"}, []string{"Café", "Cafe\u0301"}, "Café", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := GmailLabelSnapshot{Read: true, Memberships: tc.before}
			after := GmailLabelSnapshot{Read: true, Memberships: tc.after}
			if gmailMailStateMatches(before, after, gmail.Label{ID: "Label_1", Name: tc.label}, tc.add) {
				t.Fatal("ambiguous canonical memberships accepted as verified")
			}
		})
	}
}

func TestGmailLabelUnicodeCatalogCollisionKeepsMailPending(t *testing.T) {
	for _, noop := range []bool{false, true} {
		t.Run(map[bool]string{false: "change", true: "noop"}[noop], func(t *testing.T) {
			w, provider, request := labelWorkflowFixture(t)
			provider.labels = []gmail.Label{
				{ID: "Label_A", Name: "Café", Type: "user"},
				{ID: "Label_B", Name: "Cafe\u0301", Type: "user"},
			}
			provider.message.LabelIDs = []string{"Label_B"}
			if noop {
				provider.message.LabelIDs = append(provider.message.LabelIDs, "Label_A")
			}
			request.Label, request.Add = "Label_A", true
			// Only B has synced to Mail. Name equivalence cannot prove A is present,
			// even though neither individual snapshot contains a name collision.
			w.snapshot = func(context.Context, string, StableIdentity) (GmailLabelSnapshot, error) {
				return GmailLabelSnapshot{LocalIDs: []string{"99"}, Read: true, Memberships: []string{"All Mail", "Cafe\u0301"}}, nil
			}
			out, err := w.Change(context.Background(), request)
			if err != nil || !out.ProviderConfirmed || out.MailStatus != "pending" {
				t.Fatalf("provider confirmation or independent Mail uncertainty lost: %+v %v", out, err)
			}
			wantModifications := 1
			if noop {
				wantModifications = 0
			}
			if provider.modifies != wantModifications || !hasLabel(out.AfterLabelIDs, "Label_A") || !hasLabel(out.AfterLabelIDs, "Label_B") {
				t.Fatalf("provider IDs changed unexpectedly: modifications=%d result=%+v", provider.modifies, out)
			}
			reconciled, err := w.Reconcile(context.Background(), provider.journal)
			if err != nil || !reconciled.ProviderConfirmed || reconciled.MailStatus != "pending" || provider.modifies != wantModifications {
				t.Fatalf("reconcile changed the uncertainty or replayed mutation: %+v %v", reconciled, err)
			}
		})
	}
}

func TestGmailLabelUnicodeReconcileUsesFreshCatalog(t *testing.T) {
	w, provider, request := labelWorkflowFixture(t)
	provider.labels = []gmail.Label{{ID: "Label_A", Name: "Café", Type: "user"}}
	provider.message.LabelIDs = []string{}
	request.Label, request.Add = "Label_A", true
	w.snapshot = func(context.Context, string, StableIdentity) (GmailLabelSnapshot, error) {
		names := []string{"All Mail"}
		if hasLabel(provider.message.LabelIDs, "Label_A") {
			names = append(names, "Cafe\u0301")
		}
		return GmailLabelSnapshot{LocalIDs: []string{"99"}, Read: true, Memberships: names}, nil
	}
	out, err := w.Change(context.Background(), request)
	if err != nil || !out.ProviderConfirmed || out.MailStatus != "verified" {
		t.Fatalf("unique canonical name was not verified: %+v %v", out, err)
	}
	beforeReceipt, err := os.ReadFile(provider.journal)
	if err != nil {
		t.Fatal(err)
	}
	provider.labels = append(provider.labels, gmail.Label{ID: "Label_B", Name: "Cafe\u0301", Type: "user"})
	out, err = w.Reconcile(context.Background(), provider.journal)
	if err != nil || !out.ProviderConfirmed || out.MailStatus != "pending" || len(out.Labels) != 2 || provider.modifies != 1 {
		t.Fatalf("fresh ambiguous catalog ignored or mutation replayed: %+v %v", out, err)
	}
	afterReceipt, err := os.ReadFile(provider.journal)
	if err != nil || string(beforeReceipt) != string(afterReceipt) {
		t.Fatalf("reconcile changed original receipt: %v", err)
	}
}
