package cmd

import (
	"reflect"
	"testing"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

func TestVIPMailboxRequestsRespectsAccountScope(t *testing.T) {
	mailboxes := []mail.Mailbox{
		{Account: "Work", Name: "VIP"},
		{Account: "Personal", Name: "VIPs"},
		{Account: "Work", Name: "INBOX"},
	}

	got := vipMailboxRequests(mailboxes, "Work", 25)
	want := []vipMailboxRequest{{AccountName: "Work", MailboxName: "VIP", Limit: 25}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vipMailboxRequests() = %#v, want %#v", got, want)
	}
}

func TestVIPMailboxRequestsLeavesUnscopedViewAcrossAccounts(t *testing.T) {
	mailboxes := []mail.Mailbox{
		{Account: "Work", Name: "VIP"},
		{Account: "Personal", Name: "VIPs"},
		{Account: "Work", Name: "INBOX"},
	}

	got := vipMailboxRequests(mailboxes, "", 25)
	want := []vipMailboxRequest{
		{AccountName: "Work", MailboxName: "VIP", Limit: 25},
		{AccountName: "Personal", MailboxName: "VIPs", Limit: 25},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vipMailboxRequests() = %#v, want %#v", got, want)
	}
}
