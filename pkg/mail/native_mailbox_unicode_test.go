package mail

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func TestNativeMailboxUnicodeLookup(t *testing.T) {
	for _, tc := range []struct {
		name, live, requested string
	}{
		{"NFD caller NFC live", "Entwürfe", "Entwu\u0308rfe"},
		{"NFC caller NFD live", "Entwu\u0308rfe", "Entwürfe"},
		{"NFD caller NFD live", "Entwu\u0308rfe", "Entwu\u0308rfe"},
		{"NFC caller NFC live", "Entwürfe", "Entwürfe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeReadFixture()
			f.properties["mbxp:inbox"]["pnam"] = appleevents.String(tc.live)
			n := &nativeMail{ctx: context.Background(), sender: f.sender}
			ref, err := n.mailboxExact(f.accounts[0], tc.requested)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.key(ref); got != "mbxp:inbox" {
				t.Fatalf("resolved %s, want mbxp:inbox", got)
			}
		})
	}
}

func TestNativeMailboxRawIndexUnicodeRoundTrip(t *testing.T) {
	const rawName = "Entwu\u0308rfe"
	requested := mailboxLeafFromURL("imap://a/Entwu%CC%88rfe")
	if requested != rawName {
		t.Fatalf("index spelling changed: got %q, want %q", requested, rawName)
	}
	f := newNativeReadFixture()
	f.properties["mbxp:inbox"]["pnam"] = appleevents.String(rawName)
	n := &nativeMail{ctx: context.Background(), sender: f.sender}
	if _, err := n.mailboxExact(f.accounts[0], requested); err != nil {
		t.Fatal(err)
	}
}

func TestNativeMailboxNestedUnicodeLookupPreservesIdentity(t *testing.T) {
	parents := []string{"Projets été", "Projets e\u0301te\u0301"}
	leaves := []string{"Café", "Cafe\u0301"}
	for _, liveParent := range parents {
		for _, liveLeaf := range leaves {
			for _, requestedParent := range parents {
				for _, requestedLeaf := range leaves {
					t.Run(liveParent+"/"+liveLeaf+" to "+requestedParent+"/"+requestedLeaf, func(t *testing.T) {
						f := newNativeReadFixture()
						f.properties["mbxp:archive"]["pnam"] = appleevents.String(liveParent)
						child := fixtureRef("mbxp", "unicode-child")
						f.boxes["mbxp:archive"] = []appleevents.Value{child}
						f.properties["mbxp:unicode-child"] = map[string]appleevents.Value{
							"pnam": appleevents.String(liveLeaf),
							"mact": f.accounts[0],
							"mbxc": fixtureRef("mbxp", "archive"),
						}
						n := &nativeMail{ctx: context.Background(), sender: f.sender}
						ref, err := n.mailboxExact(f.accounts[0], requestedParent+"/"+requestedLeaf)
						if err != nil {
							t.Fatal(err)
						}
						if got := f.key(ref); got != "mbxp:unicode-child" {
							t.Fatalf("resolved %s, want unicode child", got)
						}
						identity, path, err := n.canonicalMailbox(nativeMailboxRef{ref: ref, name: liveLeaf}, "a")
						if err != nil {
							t.Fatal(err)
						}
						if path != liveParent+"/"+liveLeaf || !strings.Contains(identity, liveParent) || !strings.Contains(identity, liveLeaf) {
							t.Fatalf("raw mailbox identity changed: identity=%q path=%q", identity, path)
						}
						if _, err := n.mailboxExact(f.accounts[0], "Other/"+requestedLeaf); !errors.Is(err, ErrNotFound) {
							t.Fatalf("matching leaf bypassed the requested parent: %v", err)
						}
					})
				}
			}
		}
	}
}

func TestNativeMailboxUnicodeAmbiguityPreventsMutation(t *testing.T) {
	for _, requested := range []string{"Café", "Cafe\u0301"} {
		t.Run(requested, func(t *testing.T) {
			f := newNativeReadFixture()
			f.properties["mbxp:inbox"]["pnam"] = appleevents.String("Café")
			f.properties["mbxp:archive"]["pnam"] = appleevents.String("Cafe\u0301")
			c := fixtureClient(t, f)
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			err := c.MarkMessageAsRead("Personal", requested, "42", true)
			if err == nil || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("distinct canonical-equivalent mailboxes accepted: %v", err)
			}
			for _, event := range f.events {
				if event.ID != "getd" {
					t.Fatalf("ambiguous lookup dispatched %s/%s", event.Class, event.ID)
				}
			}
		})
	}
}

func TestNativeMailboxUnicodeLookupKeepsLiteralDistinctions(t *testing.T) {
	for _, tc := range []struct{ live, requested string }{
		{"Café", "café"},
		{"①", "1"},
		{"ﬃ", "ffi"},
		{"Café", "Cafe"},
	} {
		t.Run(tc.live+" vs "+tc.requested, func(t *testing.T) {
			f := newNativeReadFixture()
			f.properties["mbxp:inbox"]["pnam"] = appleevents.String(tc.live)
			n := &nativeMail{ctx: context.Background(), sender: f.sender}
			if _, err := n.mailboxExact(f.accounts[0], tc.requested); !errors.Is(err, ErrNotFound) {
				t.Fatalf("distinct names matched or lookup failed unexpectedly: %v", err)
			}
		})
	}
}
