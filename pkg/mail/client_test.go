package mail

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestJXABool(t *testing.T) {
	if got := jxaBool(true); got != "true" {
		t.Fatalf("jxaBool(true) = %q, want %q", got, "true")
	}
	if got := jxaBool(false); got != "false" {
		t.Fatalf("jxaBool(false) = %q, want %q", got, "false")
	}
}

func TestEscapeJSString(t *testing.T) {
	input := "quote' slash\\ line\n tab\t café 😀 \u2028\u2029"
	want := `quote\' slash\\ line\n tab\t caf\u00E9 \uD83D\uDE00 \u2028\u2029`
	if got := escapeJSString(input); got != want {
		t.Fatalf("escapeJSString = %q, want %q", got, want)
	}
}

func TestJXAMailboxLookupUsesInboxAccessor(t *testing.T) {
	helper := jxaMailboxLookupHelper()
	for _, want := range []string{"function findMailbox(acc, requestedName, names)", "return acc.inbox()"} {
		if !strings.Contains(helper, want) {
			t.Fatalf("jxaMailboxLookupHelper missing %q", want)
		}
	}

	if got := jxaMailboxLookupExpression("INBOX"); got != "findMailbox(acc, requestedMailbox, [requestedMailbox])" {
		t.Fatalf("jxaMailboxLookupExpression(INBOX) = %q", got)
	}
	if got := jxaMailboxLookupExpression("Archive"); got != "findMailbox(acc, requestedMailbox, ['All Mail', 'Archive'])" {
		t.Fatalf("jxaMailboxLookupExpression(Archive) = %q", got)
	}
}

func TestArchiveAliasHelpers(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Archive", true},
		{"All Mail", true},
		{"[Gmail]/All Mail", true},
		{"gmail/all mail", true},
		{"INBOX", false},
		{"GitHub", false},
	}
	for _, tt := range tests {
		if got := isArchiveAlias(tt.name); got != tt.want {
			t.Fatalf("isArchiveAlias(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestArchiveMessageScriptMovesThenVerifiesSourceMailbox(t *testing.T) {
	script := archiveMessageScript("Work", "INBOX", "12345")
	for _, want := range []string{
		"on findMailboxByName(mailboxList, targetName)",
		"set sourceMailbox to my findMailboxByName(mailboxes of targetAccount, \"INBOX\")",
		"set archiveMailbox to my findMailboxByName(mailboxes of targetAccount, \"All Mail\")",
		"set archiveMailbox to my findMailboxByName(mailboxes of targetAccount, \"Archive\")",
		"set targetId to \"12345\" as integer",
		"set targetMessage to first message of sourceMailbox whose id is targetId",
		"move targetMessage to archiveMailbox",
		"return name of archiveMailbox",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("archiveMessageScript missing %q", want)
		}
	}
}

func TestArchiveMessageScriptEscapesInputs(t *testing.T) {
	script := archiveMessageScript(`Bob "Gmail"`, `Inbox "Primary"`, "12345")
	for _, want := range []string{
		`set targetAccount to account "Bob \"Gmail\""`,
		`set sourceMailbox to my findMailboxByName(mailboxes of targetAccount, "Inbox \"Primary\"")`,
		`Mailbox not found: Inbox \"Primary\"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("archiveMessageScript missing escaped value %q", want)
		}
	}
}

func TestDeleteFallbackMailboxes(t *testing.T) {
	if got := deleteFallbackMailboxes("Newsletter"); len(got) != 2 || got[0] != "All Mail" || got[1] != "Archive" {
		t.Fatalf("deleteFallbackMailboxes(Newsletter) = %v", got)
	}
	if got := deleteFallbackMailboxes("All Mail"); len(got) != 0 {
		t.Fatalf("deleteFallbackMailboxes(All Mail) = %v, want no fallbacks", got)
	}
	if got := deleteFallbackMailboxes("Archive"); len(got) != 0 {
		t.Fatalf("deleteFallbackMailboxes(Archive) = %v, want no fallbacks", got)
	}
}

func TestDeleteMessageResolvedRetriesAllMail(t *testing.T) {
	var attempts []string
	err := deleteMessageResolved("Newsletter", func(mailbox string) error {
		attempts = append(attempts, mailbox)
		if mailbox == "All Mail" {
			return nil
		}
		return errors.New("Error: Message not found")
	})
	if err != nil {
		t.Fatalf("deleteMessageResolved returned error: %v", err)
	}
	want := []string{"Newsletter", "All Mail"}
	if len(attempts) != len(want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	for i := range want {
		if attempts[i] != want[i] {
			t.Fatalf("attempts = %v, want %v", attempts, want)
		}
	}
}

func TestDeleteMessageResolvedDoesNotRetryInvalidMailbox(t *testing.T) {
	var attempts []string
	err := deleteMessageResolved("Newsleter", func(mailbox string) error {
		attempts = append(attempts, mailbox)
		return errors.New("Error: Mailbox not found")
	})
	if err == nil {
		t.Fatal("deleteMessageResolved returned nil")
	}
	want := []string{"Newsleter"}
	if len(attempts) != len(want) || attempts[0] != want[0] {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
}

func TestIndexMailboxURLPattern(t *testing.T) {
	if got := indexMailboxURLPattern("abc-123", "Archive"); got != "imap://abc-123/%5BGmail%5D/All%20Mail" {
		t.Fatalf("archive URL = %q", got)
	}
	if got := indexMailboxURLPattern("abc-123", "All Mail"); got != "imap://abc-123/%5BGmail%5D/All%20Mail" {
		t.Fatalf("all mail URL = %q", got)
	}
	if got := indexMailboxURLPattern("abc-123", "GitHub Updates"); got != "imap://abc-123/GitHub%20Updates" {
		t.Fatalf("regular mailbox URL = %q", got)
	}
}

func TestMailboxLeafFromURL(t *testing.T) {
	got := mailboxLeafFromURL("imap://abc/%5BGmail%5D/All%20Mail")
	if got != "All Mail" {
		t.Fatalf("mailboxLeafFromURL = %q, want All Mail", got)
	}
}

func TestSQLQuote(t *testing.T) {
	got := sqlQuote("Bob's [Gmail]")
	if got != "'Bob''s [Gmail]'" {
		t.Fatalf("sqlQuote = %q", got)
	}
}

func TestEscapeSQLLikePattern(t *testing.T) {
	got := escapeSQLLikePattern(`100%_done\ok`)
	want := `100\%\_done\\ok`
	if got != want {
		t.Fatalf("escapeSQLLikePattern = %q, want %q", got, want)
	}
}

func TestSearchTermsTokenizeQuotedMultiWordQuery(t *testing.T) {
	got := searchTerms(`"Guest post services" guest`)
	want := []string{"guest", "post", "services"}
	if len(got) != len(want) {
		t.Fatalf("searchTerms = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("searchTerms = %v, want %v", got, want)
		}
	}
}

func TestIndexMailboxMembershipCondition(t *testing.T) {
	regular := indexMailbox{ID: 42, Name: "INBOX"}
	got := indexMailboxMembershipCondition(&regular)
	for _, want := range []string{
		"m.mailbox = 42",
		"labels l",
		"l.mailbox_id = 42",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("regular membership condition %q does not contain %q", got, want)
		}
	}

	archive := indexMailbox{ID: 99, Name: "All Mail"}
	if got := indexMailboxMembershipCondition(&archive); got != "m.mailbox = 99" {
		t.Fatalf("archive membership condition = %q, want direct mailbox check", got)
	}
}

func TestDefaultSearchTargetsPreferAccountCorpusForScopedAccount(t *testing.T) {
	mailboxes := []Mailbox{
		{Name: "INBOX", Account: "Klu.ai", TotalCount: 10},
		{Name: "All Mail", Account: "Klu.ai", TotalCount: 100},
		{Name: "Spam", Account: "Klu.ai", TotalCount: 5},
		{Name: "Trash", Account: "Klu.ai", TotalCount: 3},
		{Name: "Newsletter", Account: "Klu.ai", TotalCount: 7},
		{Name: "Empty", Account: "Klu.ai", TotalCount: 0},
		{Name: "INBOX", Account: "iCloud", TotalCount: 20},
	}

	targets := defaultSearchTargetsFromMailboxes(mailboxes, "Klu.ai", nil)
	want := []searchTarget{
		{AccountName: "Klu.ai", MailboxName: "All Mail"},
		{AccountName: "Klu.ai", MailboxName: "Spam"},
		{AccountName: "Klu.ai", MailboxName: "Trash"},
		{AccountName: "Klu.ai", MailboxName: "Newsletter"},
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("targets = %v, want %v", targets, want)
		}
	}
}

func TestDefaultSearchTargetsUseArchiveAliasForScopedAccount(t *testing.T) {
	mailboxes := []Mailbox{
		{Name: "INBOX", Account: "Work", TotalCount: 10},
		{Name: "Archive", Account: "Work", TotalCount: 50},
		{Name: "Junk", Account: "Work", TotalCount: 4},
		{Name: "Clients", Account: "Work", TotalCount: 8},
	}

	targets := defaultSearchTargetsFromMailboxes(mailboxes, "Work", nil)
	want := []searchTarget{
		{AccountName: "Work", MailboxName: "Archive"},
		{AccountName: "Work", MailboxName: "Junk"},
		{AccountName: "Work", MailboxName: "Clients"},
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("targets = %v, want %v", targets, want)
		}
	}
}

func TestDefaultSearchTargetsUseCorpusForGlobalSearch(t *testing.T) {
	mailboxes := []Mailbox{
		{Name: "INBOX", Account: "Klu.ai", TotalCount: 10},
		{Name: "All Mail", Account: "Klu.ai", TotalCount: 100},
		{Name: "Junk", Account: "Klu.ai", TotalCount: 3},
		{Name: "INBOX", Account: "iCloud", TotalCount: 8},
		{Name: "Archive", Account: "iCloud", TotalCount: 40},
		{Name: "INBOX", Account: "Disabled"},
	}
	enabledAccounts := map[string]bool{"Klu.ai": true, "iCloud": true, "Disabled": false}

	targets := defaultSearchTargetsFromMailboxes(mailboxes, "", enabledAccounts)
	want := []searchTarget{
		{AccountName: "Klu.ai", MailboxName: "All Mail"},
		{AccountName: "Klu.ai", MailboxName: "Junk"},
		{AccountName: "Klu.ai", MailboxName: "INBOX"},
		{AccountName: "iCloud", MailboxName: "Archive"},
		{AccountName: "iCloud", MailboxName: "INBOX"},
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("targets = %v, want %v", targets, want)
		}
	}
}

func TestCollectSearchResultsReportsFailedMailboxesInTargetOrder(t *testing.T) {
	targets := []searchTarget{
		{AccountName: "Work", MailboxName: "All Mail"},
		{AccountName: "Personal", MailboxName: "INBOX"},
	}
	result := collectSearchResults(targets, []mailboxSearchResult{
		{
			target: targets[1],
			err:    errors.New("Mail is unavailable"),
		},
		{
			target: targets[0],
			messages: []Message{
				{ID: "older", Account: "Work", DateReceived: "2026-08-01T00:00:00Z"},
				{ID: "newer", Account: "Work", DateReceived: "2026-08-02T00:00:00Z"},
			},
		},
	}, 50)

	if result.Complete {
		t.Fatal("Complete = true, want false")
	}
	if len(result.SearchedMailboxes) != 2 || result.SearchedMailboxes[1].Mailbox != "INBOX" {
		t.Fatalf("SearchedMailboxes = %#v", result.SearchedMailboxes)
	}
	if len(result.FailedMailboxes) != 1 {
		t.Fatalf("FailedMailboxes = %#v, want one failure", result.FailedMailboxes)
	}
	failure := result.FailedMailboxes[0]
	if failure.Account != "Personal" || failure.Mailbox != "INBOX" || failure.Error != "Mail is unavailable" {
		t.Fatalf("failure = %#v", failure)
	}
	if len(result.Messages) != 2 || result.Messages[0].ID != "newer" {
		t.Fatalf("Messages = %#v, want newest first", result.Messages)
	}
}

func TestCollectSearchResultsDeduplicatesAndLimitsCompleteResults(t *testing.T) {
	targets := []searchTarget{
		{AccountName: "Work", MailboxName: "All Mail"},
		{AccountName: "Work", MailboxName: "INBOX"},
	}
	result := collectSearchResults(targets, []mailboxSearchResult{
		{target: targets[0], messages: []Message{
			{ID: "same", Account: "Work", DateReceived: "2026-08-01T00:00:00Z"},
			{ID: "old", Account: "Work", DateReceived: "2026-07-01T00:00:00Z"},
		}},
		{target: targets[1], messages: []Message{
			{ID: "same", Account: "Work", DateReceived: "2026-08-01T00:00:00Z"},
			{ID: "new", Account: "Work", DateReceived: "2026-08-03T00:00:00Z"},
		}},
	}, 2)

	if !result.Complete {
		t.Fatalf("Complete = false, failures = %#v", result.FailedMailboxes)
	}
	if len(result.FailedMailboxes) != 0 {
		t.Fatalf("FailedMailboxes = %#v, want none", result.FailedMailboxes)
	}
	if len(result.Messages) != 2 || result.Messages[0].ID != "new" || result.Messages[1].ID != "same" {
		t.Fatalf("Messages = %#v", result.Messages)
	}
}

func TestMailboxJXAFailureIsPartialAndFailsClosed(t *testing.T) {
	writeFakeOsaScript(t, `
case "$*" in
  *"mailbox search failed for"*)
    printf '%s\n' 'Mail application is unavailable' >&2
    exit 1
    ;;
  *)
    printf '%s\n' 'unexpected JXA script' >&2
    exit 2
    ;;
esac
`)

	targets := []searchTarget{
		{AccountName: "Work", MailboxName: "All Mail"},
		{AccountName: "Personal", MailboxName: "INBOX"},
	}
	_, mailboxErr := NewClient().searchMessagesInSingleMailboxJXA("invoice", targets[0].AccountName, targets[0].MailboxName, 50, "")
	if mailboxErr == nil {
		t.Fatal("searchMessagesInSingleMailboxJXA returned nil error for mailbox-level JXA failure")
	}

	result := collectSearchResults(targets, []mailboxSearchResult{
		{target: targets[0], err: mailboxErr},
		{target: targets[1], messages: []Message{{ID: "1", Account: "Personal", DateReceived: "2026-08-20T00:00:00Z"}}},
	}, 50)
	_, err := applySearchOptions(result, SearchOptions{})
	var partialErr *PartialSearchError
	if !errors.As(err, &partialErr) {
		t.Fatalf("applySearchOptions error = %v, want *PartialSearchError", err)
	}
	if partialErr.Result.Complete || len(partialErr.Result.FailedMailboxes) != 1 {
		t.Fatalf("partial result = %#v", partialErr.Result)
	}

	allowed, err := applySearchOptions(result, SearchOptions{AllowPartial: true})
	if err != nil {
		t.Fatalf("allow partial returned error: %v", err)
	}
	if allowed.Complete || len(allowed.Messages) != 1 || allowed.Messages[0].Account != "Personal" {
		t.Fatalf("allowed partial result = %#v", allowed)
	}
}

func TestArchiveWhoseQueryFailureIsPartialAndFailsClosed(t *testing.T) {
	writeFakeOsaScript(t, `
case "$*" in
  *"const subjectMatches = mbox.messages.whose"*)
    printf '%s\n' 'whose query failed' >&2
    exit 1
    ;;
  *)
    printf '%s\n' 'archive whose query was not executed' >&2
    exit 2
    ;;
esac
`)

	targets := []searchTarget{
		{AccountName: "Work", MailboxName: "All Mail"},
		{AccountName: "Personal", MailboxName: "INBOX"},
	}
	_, archiveErr := NewClient().searchArchiveMailboxWithWhoseJXA("invoice", targets[0].AccountName, targets[0].MailboxName, 50, "")
	if archiveErr == nil {
		t.Fatal("searchArchiveMailboxWithWhoseJXA returned complete empty success for a whose query failure")
	}

	result := collectSearchResults(targets, []mailboxSearchResult{
		{target: targets[0], err: archiveErr},
		{target: targets[1], messages: []Message{{ID: "1", Account: "Personal", DateReceived: "2026-08-20T00:00:00Z"}}},
	}, 50)
	_, err := applySearchOptions(result, SearchOptions{})
	var partialErr *PartialSearchError
	if !errors.As(err, &partialErr) {
		t.Fatalf("applySearchOptions error = %v, want *PartialSearchError", err)
	}
	if partialErr.Result.Complete || len(partialErr.Result.Messages) != 1 {
		t.Fatalf("partial result = %#v", partialErr.Result)
	}
}

func TestRecentMessagesSearchAndLocationUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	message := Message{
		ID:           "12345",
		Account:      "iCloud",
		Mailbox:      "INBOX",
		Subject:      "Fwd: Total Loss Paperwork TL13 - Claim 061724050-01",
		Sender:       "Sonja Walker <sonja@example.com>",
		DateReceived: "2026-07-09T12:00:00Z",
		DateSent:     "2026-07-09T12:00:00Z",
		Read:         true,
		Flagged:      true,
		MessageSize:  2048,
		Content:      "Hello Sonja Walker, as your Liberty Mutual Claims Representative...",
	}
	if err := RecordRecentMessage(message, "show"); err != nil {
		t.Fatalf("RecordRecentMessage returned error: %v", err)
	}
	if matches, err := SearchRecentMessages("Sonja Liberty Mutual", "", "", 10, ""); err != nil {
		t.Fatalf("SearchRecentMessages before query terms returned error: %v", err)
	} else if len(matches) != 0 {
		t.Fatalf("matches before query terms = %v, want none from body-only text", matches)
	}
	if err := RecordRecentSearchResults([]Message{message}, "Sonja Liberty Mutual"); err != nil {
		t.Fatalf("RecordRecentSearchResults returned error: %v", err)
	}
	if err := UpdateRecentMessageLocation("iCloud", "12345", "Archive", "archive"); err != nil {
		t.Fatalf("UpdateRecentMessageLocation returned error: %v", err)
	}
	if err := RecordRecentMessage(Message{ID: "12345", Account: "iCloud", Mailbox: "Archive"}, "archive"); err != nil {
		t.Fatalf("RecordRecentMessage minimal update returned error: %v", err)
	}

	matches, err := SearchRecentMessages("Sonja Liberty Mutual", "", "", 10, "")
	if err != nil {
		t.Fatalf("SearchRecentMessages returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %v, want one match", matches)
	}
	if matches[0].Mailbox != "Archive" {
		t.Fatalf("match mailbox = %q, want Archive", matches[0].Mailbox)
	}
	if !matches[0].Read || !matches[0].Flagged || matches[0].MessageSize != 2048 {
		t.Fatalf("match envelope flags/size = read:%v flagged:%v size:%d, want preserved", matches[0].Read, matches[0].Flagged, matches[0].MessageSize)
	}

	resolved, err := ResolveRecentMessage("Sonja Liberty Mutual", "", "")
	if err != nil {
		t.Fatalf("ResolveRecentMessage returned error: %v", err)
	}
	if resolved.ID != "12345" {
		t.Fatalf("resolved ID = %q, want 12345", resolved.ID)
	}
}

func TestRecentMessagesPreserveSearchTermsAcrossSkeletalUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	message := Message{
		ID:      "abc",
		Account: "iCloud",
		Mailbox: "INBOX",
		Subject: "Total Loss Paperwork",
		Sender:  "Sonja Walker <sonja@example.com>",
	}
	if err := RecordRecentSearchResults([]Message{message}, "Sonja Liberty Mutual"); err != nil {
		t.Fatalf("RecordRecentSearchResults returned error: %v", err)
	}
	if err := RecordRecentMessage(Message{ID: "abc", Account: "iCloud", Mailbox: "Archive"}, "archive"); err != nil {
		t.Fatalf("RecordRecentMessage returned error: %v", err)
	}
	matches, err := SearchRecentMessages("Sonja Liberty Mutual", "", "", 10, "")
	if err != nil {
		t.Fatalf("SearchRecentMessages returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %v, want one match", matches)
	}
	if matches[0].Subject != "Total Loss Paperwork" {
		t.Fatalf("subject = %q, want preserved subject", matches[0].Subject)
	}
	if matches[0].Mailbox != "Archive" {
		t.Fatalf("mailbox = %q, want Archive", matches[0].Mailbox)
	}
}

func TestRecentMessagesRejectEmptyTokenizedQuery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	message := Message{
		ID:      "12345",
		Account: "iCloud",
		Mailbox: "INBOX",
		Subject: "Total Loss Paperwork",
		Sender:  "Sonja Walker <sonja@example.com>",
	}
	if err := RecordRecentMessage(message, "show"); err != nil {
		t.Fatalf("RecordRecentMessage returned error: %v", err)
	}

	matches, err := SearchRecentMessages("!!!", "", "", 10, "")
	if err != nil {
		t.Fatalf("SearchRecentMessages returned error: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %v, want none", matches)
	}
	resolved, err := ResolveRecentMessage("12345", "", "")
	if err != nil {
		t.Fatalf("ResolveRecentMessage exact ID returned error: %v", err)
	}
	if resolved.ID != "12345" {
		t.Fatalf("resolved ID = %q, want 12345", resolved.ID)
	}
}

func TestRecentMessagesSearchExcludesLocationMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	message := Message{
		ID:      "8675309",
		Account: "Gmail",
		Mailbox: "INBOX",
		Subject: "Total Loss Paperwork",
		Sender:  "Sonja Walker <sonja@example.com>",
	}
	if err := RecordRecentMessage(message, "show"); err != nil {
		t.Fatalf("RecordRecentMessage returned error: %v", err)
	}

	for _, query := range []string{"8675309", "gmail", "inbox"} {
		matches, err := SearchRecentMessages(query, "", "", 10, "")
		if err != nil {
			t.Fatalf("SearchRecentMessages(%q) returned error: %v", query, err)
		}
		if len(matches) != 0 {
			t.Fatalf("SearchRecentMessages(%q) = %v, want none", query, matches)
		}
	}
	resolved, err := ResolveRecentMessage("inbox", "", "")
	if err != nil {
		t.Fatalf("ResolveRecentMessage location selector returned error: %v", err)
	}
	if resolved.ID != "8675309" {
		t.Fatalf("resolved ID = %q, want 8675309", resolved.ID)
	}
}

func TestRemoveRecentMessageExcludesDeletedEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	message := Message{
		ID:      "12345",
		Account: "iCloud",
		Mailbox: "Archive",
		Subject: "Total Loss Paperwork",
	}
	if err := RecordRecentMessage(message, "show"); err != nil {
		t.Fatalf("RecordRecentMessage returned error: %v", err)
	}
	if err := RemoveRecentMessage("iCloud", "12345"); err != nil {
		t.Fatalf("RemoveRecentMessage returned error: %v", err)
	}

	matches, err := SearchRecentMessages("total loss", "", "", 10, "")
	if err != nil {
		t.Fatalf("SearchRecentMessages returned error: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %v, want none", matches)
	}
	if _, err := ResolveRecentMessage("12345", "", ""); err == nil {
		t.Fatal("ResolveRecentMessage returned nil error for removed entry")
	}
}

func TestRemoveRecentMessageSerializesConcurrentCleanup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const messageCount = 20
	for i := 0; i < messageCount; i++ {
		message := Message{
			ID:      fmt.Sprintf("message-%d", i),
			Account: "iCloud",
			Mailbox: "Archive",
			Subject: "Total Loss Paperwork",
		}
		if err := RecordRecentMessage(message, "show"); err != nil {
			t.Fatalf("RecordRecentMessage returned error: %v", err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, messageCount)
	for i := 0; i < messageCount; i++ {
		messageID := fmt.Sprintf("message-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- RemoveRecentMessage("iCloud", messageID)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RemoveRecentMessage returned error: %v", err)
		}
	}

	matches, err := SearchRecentMessages("total loss", "", "", messageCount, "")
	if err != nil {
		t.Fatalf("SearchRecentMessages returned error: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %v, want none", matches)
	}
}

func TestDeleteMessageRemovesRecentEntry(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir)
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf Success\n"), 0755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}
	message := Message{
		ID:      "12345",
		Account: "iCloud",
		Mailbox: "Archive",
		Subject: "Total Loss Paperwork",
	}
	if err := RecordRecentMessage(message, "show"); err != nil {
		t.Fatalf("RecordRecentMessage returned error: %v", err)
	}

	if err := NewClient().DeleteMessage("iCloud", "Archive", "12345"); err != nil {
		t.Fatalf("DeleteMessage returned error: %v", err)
	}
	if _, err := ResolveRecentMessage("12345", "", ""); err == nil {
		t.Fatal("ResolveRecentMessage returned nil error after delete")
	}
}

func TestDeleteMessageWarnsWhenRecentCleanupFails(t *testing.T) {
	home := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir)
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf Success\n"), 0755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}
	recentDir := filepath.Join(home, ".cache", "mail-app-cli")
	if err := os.MkdirAll(recentDir, 0755); err != nil {
		t.Fatalf("create recent directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recentDir, recentMessagesFile), []byte("{"), recentMessagePermissions); err != nil {
		t.Fatalf("write corrupt recent journal: %v", err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	originalStderr := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = reader.Close()
		_ = writer.Close()
	})

	if err := NewClient().DeleteMessage("iCloud", "Archive", "12345"); err != nil {
		t.Fatalf("DeleteMessage returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	os.Stderr = originalStderr
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if !strings.Contains(string(output), "message was deleted, but recent-message history could not be updated") {
		t.Fatalf("stderr = %q, want recent cleanup warning", output)
	}
}

func TestRecentMessagesExcludeEntriesMarkedDeleted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	message := Message{
		ID:      "12345",
		Account: "iCloud",
		Mailbox: "Archive",
		Subject: "Total Loss Paperwork",
		Deleted: true,
	}
	if err := RecordRecentMessage(message, "show"); err != nil {
		t.Fatalf("RecordRecentMessage returned error: %v", err)
	}

	matches, err := SearchRecentMessages("total loss", "", "", 10, "")
	if err != nil {
		t.Fatalf("SearchRecentMessages returned error: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %v, want none", matches)
	}
	if _, err := ResolveRecentMessage("12345", "", ""); err == nil {
		t.Fatal("ResolveRecentMessage returned nil error for deleted entry")
	}
}

func TestIsSpecialMailboxNameIncludesSentItems(t *testing.T) {
	if !isSpecialMailboxName("sent", "Sent Items") {
		t.Fatal("Sent Items should be treated as a sent mailbox")
	}
}

func TestRunBulkOperations(t *testing.T) {
	t.Run("zero requests", func(t *testing.T) {
		called := false
		err := runBulkOperations([]int{}, "failed", func(int) error {
			called = true
			return nil
		})
		if err != nil {
			t.Fatalf("runBulkOperations returned error: %v", err)
		}
		if called {
			t.Fatal("runBulkOperations called callback for zero requests")
		}
	})

	t.Run("single request", func(t *testing.T) {
		var calls []int
		err := runBulkOperations([]int{7}, "failed", func(value int) error {
			calls = append(calls, value)
			return nil
		})
		if err != nil {
			t.Fatalf("runBulkOperations returned error: %v", err)
		}
		if len(calls) != 1 || calls[0] != 7 {
			t.Fatalf("callback calls = %v, want [7]", calls)
		}
	})

	t.Run("multiple request errors", func(t *testing.T) {
		err := runBulkOperations([]int{1, 2, 3}, "failed to process", func(value int) error {
			if value == 2 {
				return nil
			}
			return fmt.Errorf("request %d failed", value)
		})
		if err == nil {
			t.Fatal("runBulkOperations returned nil error")
		}

		message := err.Error()
		for _, want := range []string{
			"failed to process:",
			"request 1 failed",
			"request 3 failed",
		} {
			if !strings.Contains(message, want) {
				t.Fatalf("error %q does not contain %q", message, want)
			}
		}
	})
}

func TestSortAndSliceUsesGlobalDateOrder(t *testing.T) {
	messages := []Message{
		{ID: "1", DateReceived: "2026-06-20T10:00:00Z"},
		{ID: "2", DateReceived: "2026-06-22T10:00:00Z"},
		{ID: "3", DateReceived: "2026-06-21T10:00:00Z"},
		{ID: "4", DateReceived: "2026-06-19T10:00:00Z"},
	}

	got := sortAndSlice(messages, 1, 2)
	gotIDs := []string{got[0].ID, got[1].ID}
	wantIDs := []string{"3", "1"}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("sortAndSlice ids = %v, want %v", gotIDs, wantIDs)
		}
	}
}

func TestIsEnvelopeIndexUnavailable(t *testing.T) {
	unavailableErrors := []error{
		errors.New(`sqlite3 envelope index query failed: exit status 1 - Error: unable to open database "/Users/example/Library/Mail/V10/MailData/Envelope Index": authorization denied`),
		errors.New("ls: MailData: Operation not permitted"),
		errors.New("sqlite3: executable file not found"),
		errors.New("no such file"),
		errors.New("envelope index disabled"),
	}

	for _, err := range unavailableErrors {
		if !isEnvelopeIndexUnavailable(err) {
			t.Fatalf("isEnvelopeIndexUnavailable(%q) = false, want true", err)
		}
	}

	if isEnvelopeIndexUnavailable(errors.New("failed to parse envelope index JSON")) {
		t.Fatal("parse errors should not be treated as unavailable index")
	}
}
