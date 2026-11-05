package mail

import (
	"errors"
	"fmt"
	netmail "net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRecipientFilterExactAddress(t *testing.T) {
	for input, want := range map[string]string{"": "", " User.Name+tag@EXAMPLE.com ": "user.name+tag@example.com", "user@[127.0.0.1]": "user@[127.0.0.1]"} {
		got, err := NormalizeRecipientFilter(input)
		if err != nil || got != want {
			t.Errorf("normalize %q = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"person", "@example.com", "a@", "Display Name <a@example.com>", "a@example.com,b@example.com", "a@example.com\nBcc: b@example.com", "a@example.com\r\n"} {
		if _, err := NormalizeRecipientFilter(input); err == nil {
			t.Errorf("accepted invalid single address %q", input)
		}
	}
}

func queryRecipientMessage(id, date, to string) Message {
	m := scanRecipientMessage(id, to)
	m.DateReceived = date
	return m
}

func TestMatchRecipientUsesOnlyKnownExactAddressCollections(t *testing.T) {
	base := queryRecipientMessage("1", "", "other@example.com")
	base.Sender = "target@example.com"
	base.Subject = "target@example.com"
	base.Content = "target@example.com"
	base.DeliveredTo = []string{"target@example.com"}
	base.OriginalTo = []string{"target@example.com"}
	for name, m := range map[string]Message{
		"delivery sender subject body": base,
		"domain mention":               scanRecipientMessage("1", "other@target.example.com"),
		"plus preserved":               scanRecipientMessage("1", "target+tag@example.com"),
		"dots preserved":               scanRecipientMessage("1", "tar.get@example.com"),
		"available empty":              scanRecipientMessage("1", ""),
	} {
		t.Run(name, func(t *testing.T) {
			if match, known := MatchRecipient(m, "target@example.com"); match || !known {
				t.Fatalf("match=%t known=%t", match, known)
			}
		})
	}
	for _, field := range []string{"To", "Cc", "Bcc"} {
		t.Run(field, func(t *testing.T) {
			m := base
			switch field {
			case "To":
				m.ToRecipients = []string{"TARGET@EXAMPLE.COM"}
			case "Cc":
				m.CcRecipients = []string{"TARGET@EXAMPLE.COM"}
				m.RecipientStatus.To = RecipientFieldStatus{}
			case "Bcc":
				m.BccRecipients = []string{"TARGET@EXAMPLE.COM"}
				m.RecipientStatus.To = RecipientFieldStatus{}
			}
			if match, known := MatchRecipient(m, "target@example.com"); !match || !known {
				t.Fatalf("match=%t known=%t", match, known)
			}
		})
	}
	for i := 0; i < 3; i++ {
		m := base
		*recipientFields(&m)[i].status = RecipientFieldStatus{State: "unavailable"}
		if match, known := MatchRecipient(m, "target@example.com"); match || known {
			t.Errorf("unknown field %d: match=%t known=%t", i, match, known)
		}
	}
}

func TestRecipientQueryMatchesParsedHeaderAddressesLiterally(t *testing.T) {
	message := Message{}
	populateRecipientHeaders(&message, netmail.Header{
		"To":  {"Display Name <USER.Name+tag@example.com>"},
		"Cc":  {"Network <user@[127.0.0.1]>"},
		"Bcc": {"Hidden <hidden@example.com>"},
	}, "disk")
	for address, want := range map[string]bool{
		"user.name+tag@EXAMPLE.com": true,
		"user@[127.0.0.1]":          true,
		"hidden@example.com":        true,
		"user.name@example.com":     false,
		"username+tag@example.com":  false,
		"user@127.0.0.1":            false,
		"Display Name":              false,
	} {
		matched, known := MatchRecipient(message, address)
		if matched != want || !known {
			t.Errorf("match %q = %t known=%t; want %t", address, matched, known, want)
		}
	}
}

func TestRecipientQueryPreservesQuotedLocalPartWhitespace(t *testing.T) {
	message := Message{}
	populateRecipientHeaders(&message, netmail.Header{"To": {`" user"@example.test`}}, "disk")
	for input, want := range map[string]bool{
		`" user"@example.test`: true,
		`user@example.test`:    false,
	} {
		recipient, err := NormalizeRecipientFilter(input)
		if err != nil {
			t.Fatal(err)
		}
		matched, known := MatchRecipient(message, recipient)
		if matched != want || !known {
			t.Errorf("match %q = %t known=%t; want %t (parsed To=%q)", input, matched, known, want, message.ToRecipients)
		}
	}
}

func TestRecipientQueryPagesConfirmedMatchesBeforeOffset(t *testing.T) {
	req := RecipientQueryRequest{Recipient: "target@example.com", Scopes: []SearchMailbox{{"Work", "INBOX"}}, Offset: 1, Limit: 1}
	reads := 0
	result := collectRecipientMessages(req, func(_ SearchMailbox, after *ScanPosition) (scanPage, error) {
		reads++
		if reads == 1 {
			messages := make([]Message, 200)
			for i := range messages {
				messages[i] = scanRecipientMessage(fmt.Sprint(500-i), "other@example.com")
			}
			return scanPage{Messages: messages, Remaining: 2, Last: &ScanPosition{ID: 301, ReceivedUnix: 301}}, nil
		}
		if reads != 2 || after == nil || after.ID != 301 {
			t.Fatalf("lost candidate boundary: read=%d after=%+v", reads, after)
		}
		return scanPage{Messages: []Message{queryRecipientMessage("300", "2026-02-02", "target@example.com"), queryRecipientMessage("299", "2026-02-01", "target@example.com")}}, nil
	})
	if reads != 2 || !result.Complete || len(result.Messages) != 1 || result.Messages[0].ID != "299" {
		t.Fatalf("result=%+v reads=%d", result, reads)
	}
}

func TestRecipientQueryEmptyFilteredPageRetainsBoundary(t *testing.T) {
	reads := 0
	result := collectRecipientMessages(RecipientQueryRequest{Recipient: "target@example.com", Limit: 1, Scopes: []SearchMailbox{{"Work", "INBOX"}}}, func(_ SearchMailbox, after *ScanPosition) (scanPage, error) {
		reads++
		if reads == 1 {
			return scanPage{Remaining: 1, Last: &ScanPosition{ID: 20, ReceivedUnix: 20}}, nil
		}
		if reads != 2 || after == nil || after.ID != 20 {
			t.Fatalf("after=%+v reads=%d", after, reads)
		}
		return scanPage{Messages: []Message{scanRecipientMessage("19", "target@example.com")}}, nil
	})
	if !result.Complete || len(result.Messages) != 1 || reads != 2 {
		t.Fatalf("result=%+v reads=%d", result, reads)
	}
}

func TestRecipientQueryGlobalOrderingAndDuplicateLabels(t *testing.T) {
	req := RecipientQueryRequest{Recipient: "target@example.com", Limit: 2, Offset: 1, Scopes: []SearchMailbox{{"A", "INBOX"}, {"A", "Archive"}, {"B", "INBOX"}, {"A", "INBOX"}}}
	reads := map[SearchMailbox]int{}
	result := collectRecipientMessages(req, func(scope SearchMailbox, _ *ScanPosition) (scanPage, error) {
		reads[scope]++
		if reads[scope] > 1 {
			t.Fatal("unneeded or duplicate scope read")
		}
		if scope.Account == "B" {
			return scanPage{Messages: []Message{queryRecipientMessage("9", "2026-03-03", "target@example.com"), queryRecipientMessage("7", "2026-03-01", "target@example.com")}}, nil
		}
		return scanPage{Messages: []Message{queryRecipientMessage("9", "2026-03-02", "target@example.com"), queryRecipientMessage("8", "2026-02-28", "target@example.com"), queryRecipientMessage("7", "2026-02-27", "target@example.com")}, Remaining: 100, Last: &ScanPosition{ID: 7}}, nil
	})
	if !result.Complete || len(result.Messages) != 2 || result.Messages[0].Account != "A" || result.Messages[0].ID != "9" || result.Messages[1].Account != "B" || result.Messages[1].ID != "7" || len(reads) != 3 {
		t.Fatalf("result=%+v reads=%v", result, reads)
	}
}

func TestRecipientQueryIncompleteEvidencePreservesConfirmedMatches(t *testing.T) {
	for _, failure := range []string{"unknown", "index error", "missing boundary", "repeated boundary", "backward boundary"} {
		t.Run(failure, func(t *testing.T) {
			reads := 0
			result := collectRecipientMessages(RecipientQueryRequest{Recipient: "target@example.com", Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Other", "INBOX"}}}, func(scope SearchMailbox, after *ScanPosition) (scanPage, error) {
				if scope.Account == "Other" {
					return scanPage{Messages: []Message{scanRecipientMessage("3", "target@example.com")}}, nil
				}
				reads++
				if reads > 2 {
					t.Fatal("nonadvancing traversal did not stop")
				}
				if failure == "index error" && reads == 2 {
					return scanPage{}, errors.New("index unreadable")
				}
				page := scanPage{Messages: []Message{scanRecipientMessage("2", "target@example.com")}, Remaining: 3, Last: &ScanPosition{ID: 2, ReceivedUnix: 2}}
				if failure == "unknown" {
					page.Messages = append(page.Messages, Message{ID: "1"})
				}
				if failure == "missing boundary" {
					page.Last = nil
				}
				if failure == "backward boundary" && after != nil {
					page.Last.ReceivedUnix = 3
				}
				return page, nil
			})
			if result.Complete || len(result.Messages) != 2 || len(result.FailedMailboxes) != 1 || len(result.SearchedMailboxes) != 2 {
				t.Fatalf("result=%+v", result)
			}
			f := result.FailedMailboxes[0]
			if f.Account != "Work" || f.Error == "" || f.ScannedMessages == 0 {
				t.Fatalf("failure=%+v", f)
			}
			if failure == "unknown" && (f.ReadFailures != 1 || f.ScannedMessages != 2) {
				t.Fatalf("unknown coverage=%+v", f)
			}
		})
	}
}

func TestRecipientQueryValidatesBeforeDiscoveryOrAutomation(t *testing.T) {
	for _, invalid := range []RecipientQueryRequest{{Recipient: "Display <a@example.com>"}, {Recipient: "a@example.com", Limit: -1}, {Recipient: "a@example.com", Offset: -1}, {Recipient: "a@example.com", Since: "invalid date"}} {
		f := newNativeReadFixture()
		c := fixtureClient(t, f)
		calls := []func() error{
			func() error { _, err := c.RecipientMessages(invalid); return err },
			func() error { _, err := c.SearchRecipientMessages("", "", invalid); return err },
			func() error { _, err := c.UnifiedRecipientMessages("inbox", invalid); return err },
		}
		for _, call := range calls {
			if err := call(); err == nil {
				t.Fatalf("invalid request accepted: %+v", invalid)
			}
		}
		if len(f.events) != 0 {
			t.Fatalf("invalid request dispatched %d automation events", len(f.events))
		}
	}
}

func TestRecipientQueryIndexAndCachedHeadersBeyondFirstPage(t *testing.T) {
	c, root, events := localReadFixture(t)
	var sql strings.Builder
	sql.WriteString("insert into addresses(ROWID,address,comment) values (2,'other@example.com','Other');\n")
	owner := filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID")
	for i := 0; i < 205; i++ {
		id, sender := 1000+i, 2
		if i >= 200 {
			sender = 1
		}
		fmt.Fprintf(&sql, "insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values (%d,1,%d,1,1,%d,%d,0,1,0,100);\n", id, sender, 1800000000-i, 1800000000-i)
		writeEMLX(t, filepath.Join(owner, messageDataDirectory(int64(id)), "Messages", fmt.Sprintf("%d.emlx", id)), "To: other@example.com\nBcc: TARGET@example.com\nContent-Type: text/plain\n\nselected body")
	}
	// Newer exact matches must be excluded by each envelope predicate before
	// the confirmed-match offset is applied.
	sql.WriteString("insert into subjects(ROWID,subject) values (2,'unrelated topic');\n")
	for _, row := range []struct{ id, subject, read, flagged, received int }{
		{900, 1, 1, 1, 1800001000},
		{901, 1, 0, 0, 1800001000},
		{902, 2, 0, 1, 1800001000},
		{903, 1, 0, 1, 1600000000},
	} {
		fmt.Fprintf(&sql, "insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values (%d,%d,1,1,1,%d,%d,%d,%d,0,100);\n", row.id, row.subject, row.received, row.received, row.read, row.flagged)
		writeEMLX(t, filepath.Join(owner, messageDataDirectory(int64(row.id)), "Messages", fmt.Sprintf("%d.emlx", row.id)), "To: target@example.com\nContent-Type: text/plain\n\nexcluded body")
	}
	if out, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), sql.String()).CombinedOutput(); err != nil {
		t.Fatalf("extend fixture: %v %s", err, out)
	}
	req := RecipientQueryRequest{Scopes: []SearchMailbox{{"Work", "All Mail"}}, Recipient: "TARGET@EXAMPLE.COM", Query: "indexed subject", Since: "2024-01-01", Unread: true, Flagged: true, Sender: "sender@example.com", Offset: 1, Limit: 2}
	result, err := c.RecipientMessages(req)
	if err != nil || !result.Complete || len(result.Messages) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := []string{result.Messages[0].ID, result.Messages[1].ID}; !reflect.DeepEqual(got, []string{"1201", "1202"}) {
		t.Fatalf("confirmed slice=%v", got)
	}
	for _, m := range result.Messages {
		if m.Content != "" || m.BodyDiagnostics != nil || m.RecipientStatus.Bcc.Source != "disk" {
			t.Fatalf("header-only query=%+v", m)
		}
	}
	for _, change := range []func(*RecipientQueryRequest){func(r *RecipientQueryRequest) { r.Query = "absent keyword" }, func(r *RecipientQueryRequest) { r.Since = "2030-01-01" }, func(r *RecipientQueryRequest) { r.Sender = "absent@example.com" }} {
		filtered := req
		change(&filtered)
		got, err := c.RecipientMessages(filtered)
		if err != nil || !got.Complete || len(got.Messages) != 0 {
			t.Fatalf("metadata filter=%+v err=%v", got, err)
		}
	}
	req.WithContent = true
	withBody, err := c.RecipientMessages(req)
	if err != nil || len(withBody.Messages) != 2 {
		t.Fatalf("with-content=%+v err=%v", withBody, err)
	}
	for _, m := range withBody.Messages {
		if m.Content != "selected body" || m.ContentSource != "disk" {
			t.Fatalf("matched content=%+v", m)
		}
	}
	if events.Load() != 0 {
		t.Fatalf("cached recipient queries dispatched %d Mail events", events.Load())
	}
}

func TestRecipientQuerySearchRetainsMissingAccountDiscoveryFailure(t *testing.T) {
	c, _, events := localReadFixture(t)
	c.shared.accounts = []Account{
		{ID: "ABC", Name: "Work", Enabled: true},
		{ID: "MISSING", Name: "Missing", Enabled: true},
	}
	req := RecipientQueryRequest{Recipient: "li@example.com", Flagged: true}
	result, err := c.SearchRecipientMessages("", "", req)
	if err != nil || result.Complete || len(result.Messages) != 1 || result.Messages[0].ID != "101" || result.Messages[0].Account != "Work" {
		t.Fatalf("all-account search=%+v err=%v", result, err)
	}
	if len(result.FailedMailboxes) != 1 || result.FailedMailboxes[0].Account != "Missing" || result.FailedMailboxes[0].Mailbox != "(mailbox discovery)" || result.FailedMailboxes[0].Error == "" {
		t.Fatalf("missing account failure=%+v", result.FailedMailboxes)
	}
	// Explicit account discovery is independent of other accounts, including
	// when the explicitly requested account is disabled in unified selection.
	c.shared.accounts[0].Enabled = false
	result, err = c.SearchRecipientMessages("Work", "", req)
	if err != nil || !result.Complete || len(result.FailedMailboxes) != 0 || len(result.Messages) != 1 || result.Messages[0].ID != "101" {
		t.Fatalf("named-account search=%+v err=%v", result, err)
	}
	// Default all-account discovery excludes disabled accounts entirely.
	c.shared.accounts[0].Enabled, c.shared.accounts[1].Enabled = true, false
	result, err = c.SearchRecipientMessages("", "", req)
	if err != nil || !result.Complete || len(result.FailedMailboxes) != 0 || len(result.Messages) != 1 {
		t.Fatalf("enabled-account search=%+v err=%v", result, err)
	}
	if events.Load() != 0 {
		t.Fatalf("index discovery dispatched %d Mail events", events.Load())
	}
}

func TestRecipientQueryUnifiedSpecialRetainsMissingAccountDiscoveryFailure(t *testing.T) {
	c, root, events := localReadFixture(t)
	// Special-mailbox discovery retains the historical all-account selection,
	// including disabled accounts.
	c.shared.accounts = []Account{
		{ID: "ABC", Name: "Work", Enabled: false},
		{ID: "MISSING", Name: "Missing", Enabled: false},
	}
	sql := `insert into mailboxes(ROWID,url,total_count,unread_count) values (5,'imap://ABC/Sent',1,0);
insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values (107,1,1,1,5,1690000000,1690000000,1,0,0,100);`
	if out, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), sql).CombinedOutput(); err != nil {
		t.Fatalf("sent fixture: %v %s", err, out)
	}
	writeEMLX(t, filepath.Join(root, "ABC", "Sent.mbox", "UUID", "Data", "Messages", "107.emlx"), "To: target@example.com\n\nsent body")
	result, err := c.UnifiedRecipientMessages("sent", RecipientQueryRequest{Recipient: "target@example.com"})
	if err != nil || result.Complete || len(result.Messages) != 1 || result.Messages[0].ID != "107" || result.Messages[0].Account != "Work" || result.Messages[0].Mailbox != "Sent" {
		t.Fatalf("unified sent=%+v err=%v", result, err)
	}
	if len(result.FailedMailboxes) != 1 || result.FailedMailboxes[0].Account != "Missing" || result.FailedMailboxes[0].Mailbox != "(mailbox discovery)" || result.FailedMailboxes[0].Error == "" {
		t.Fatalf("missing account failure=%+v", result.FailedMailboxes)
	}
	if events.Load() != 0 {
		t.Fatalf("special-mailbox discovery dispatched %d Mail events", events.Load())
	}
}

func TestRecipientQueryNullAndEpochDatesUseIndexIDTieBreak(t *testing.T) {
	result := collectRecipientMessages(RecipientQueryRequest{Recipient: "target@example.com", Limit: 1, Scopes: []SearchMailbox{{"Work", "INBOX"}}}, func(SearchMailbox, *ScanPosition) (scanPage, error) {
		return scanPage{Messages: []Message{queryRecipientMessage("9", "", "target@example.com"), queryRecipientMessage("8", "1970-01-01T00:00:00Z", "target@example.com")}}, nil
	})
	if !result.Complete || len(result.Messages) != 1 || result.Messages[0].ID != "9" {
		t.Fatalf("index date ordering=%+v", result)
	}
}

func TestRecipientQueryDiscoveryRetainsDistinctNestedMailboxPaths(t *testing.T) {
	for _, withRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("root=%t", withRoot), func(t *testing.T) {
			c, root, events := localReadFixture(t)
			c.shared.accounts = []Account{{ID: "ABC", Name: "Work", Enabled: true}}
			paths := []string{"Clients/Projects", "Internal/Projects"}
			if withRoot {
				paths = append(paths, "Projects")
			}
			var sql strings.Builder
			sql.WriteString("delete from labels; delete from messages; delete from mailboxes;\n")
			sql.WriteString("insert into mailboxes(ROWID,url,total_count,unread_count) values (200,'imap://ABC/INBOX',0,0);\n")
			wantIDs := map[string]bool{}
			for i, path := range paths {
				id := 201 + i
				fmt.Fprintf(&sql, "insert into mailboxes(ROWID,url,total_count,unread_count) values (%d,'imap://ABC/%s',1,0);\n", id, escapeMailboxURLPath(path))
				fmt.Fprintf(&sql, "insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values (%d,1,1,1,%d,1690000000,1690000000,1,0,0,100);\n", id, id)
				recipient := "other@example.com"
				if withRoot || i == 1 {
					recipient = "target@example.com"
					wantIDs[fmt.Sprint(id)] = true
				}
				owner := filepath.Join(root, "ABC")
				for _, component := range strings.Split(path, "/") {
					owner = filepath.Join(owner, component+".mbox")
				}
				writeEMLX(t, filepath.Join(owner, "UUID", "Data", "Messages", fmt.Sprintf("%d.emlx", id)), "To: "+recipient+"\n\nbody")
			}
			if out, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), sql.String()).CombinedOutput(); err != nil {
				t.Fatalf("nested fixture: %v %s", err, out)
			}
			result, err := c.SearchRecipientMessages("Work", "", RecipientQueryRequest{Recipient: "target@example.com"})
			if err != nil || !result.Complete || len(result.Messages) != len(wantIDs) || len(result.SearchedMailboxes) != len(paths)+1 {
				t.Fatalf("discovered paths=%v result=%+v err=%v", paths, result, err)
			}
			for _, message := range result.Messages {
				if !wantIDs[message.ID] {
					t.Fatalf("unexpected recipient match: %+v", message)
				}
				delete(wantIDs, message.ID)
			}
			if len(wantIDs) != 0 || events.Load() != 0 {
				t.Fatalf("missing IDs=%v Mail events=%d", wantIDs, events.Load())
			}
		})
	}
}

func TestRecipientQueryDiscoveryRejectsPhysicalMailboxAliasReplacement(t *testing.T) {
	c, _, events := localReadFixture(t)
	c.shared.accounts = []Account{{ID: "ABC", Name: "Work", Enabled: true}}
	sql := `delete from labels; delete from messages; delete from mailboxes;
insert into mailboxes(ROWID,url,total_count,unread_count) values
(1,'imap://ABC/%5BGmail%5D/All%20Mail',1,0),(2,'imap://ABC/Archive',1,0),(3,'imap://ABC/INBOX',0,0);`
	if out, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), sql).CombinedOutput(); err != nil {
		t.Fatalf("alias fixture: %v %s", err, out)
	}
	result, err := c.SearchRecipientMessages("Work", "", RecipientQueryRequest{Recipient: "target@example.com"})
	if err != nil || result.Complete || len(result.Messages) != 0 || len(result.FailedMailboxes) != 1 || result.FailedMailboxes[0].Mailbox != "Archive" || !strings.Contains(result.FailedMailboxes[0].Error, "aliasing") {
		t.Fatalf("literal Archive silently replaced: result=%+v err=%v", result, err)
	}
	if !reflect.DeepEqual(result.SearchedMailboxes, []SearchMailbox{{Account: "Work", Mailbox: "[Gmail]/All Mail"}, {Account: "Work", Mailbox: "INBOX"}}) || events.Load() != 0 {
		t.Fatalf("physical scopes=%v Mail events=%d", result.SearchedMailboxes, events.Load())
	}
}

func TestRecipientQueryDiscoveryRequiresRootInbox(t *testing.T) {
	for _, path := range []string{"Projects", "Nested/INBOX"} {
		t.Run(path, func(t *testing.T) {
			c, _, events := localReadFixture(t)
			c.shared.accounts = []Account{{ID: "ABC", Name: "Work", Enabled: true}}
			sql := "delete from labels; delete from messages; delete from mailboxes;" +
				"insert into mailboxes(ROWID,url,total_count,unread_count) values (1," + sqlQuote("imap://ABC/"+escapeMailboxURLPath(path)) + ",1,0);"
			if out, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), sql).CombinedOutput(); err != nil {
				t.Fatalf("missing inbox fixture: %v %s", err, out)
			}
			result, err := c.SearchRecipientMessages("Work", "", RecipientQueryRequest{Recipient: "target@example.com"})
			if err != nil || result.Complete || len(result.Messages) != 0 || len(result.FailedMailboxes) == 0 {
				t.Fatalf("missing root INBOX accepted: result=%+v err=%v", result, err)
			}
			foundScope, foundFailure := false, false
			for _, scope := range result.SearchedMailboxes {
				foundScope = foundScope || scope.Account == "Work" && scope.Mailbox == "INBOX"
			}
			for _, failure := range result.FailedMailboxes {
				foundFailure = foundFailure || failure.Account == "Work" && failure.Mailbox == "INBOX" && failure.Error != ""
			}
			if !foundScope || !foundFailure || events.Load() != 0 {
				t.Fatalf("missing root coverage: result=%+v Mail events=%d", result, events.Load())
			}
		})
	}
}
