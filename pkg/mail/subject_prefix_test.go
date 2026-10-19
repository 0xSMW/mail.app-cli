package mail

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The prefix is literal text from messages, including its own spacing. The
// shared base subject intentionally makes lost prefixes visible to identity tests.
func subjectPrefixFixture(t *testing.T) (*Client, *indexMailbox, map[string]string) {
	t.Helper()
	client, _, _ := localReadFixture(t)
	expected := map[string]string{}
	var setup strings.Builder
	setup.WriteString("update subjects set subject='topic'; update messages set deleted=1;\n")
	for i, prefix := range []*string{nil, subjectPrefixText(""), subjectPrefixText("Re: "), subjectPrefixText("Fwd: "), subjectPrefixText("Re: Re: "), subjectPrefixText(`Wild%_\: `), subjectPrefixText("Re:"), subjectPrefixText("Re:  ")} {
		id := strconv.Itoa(201 + i)
		sqlPrefix, subject := "null", "topic"
		if prefix != nil {
			sqlPrefix = sqlQuote(*prefix)
			subject = *prefix + subject
		}
		expected[id] = subject
		fmt.Fprintf(&setup, "insert into messages(ROWID,subject_prefix,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values (%s,%s,1,1,1,1,1690000000,1690000000,0,0,0,1234);\n", id, sqlPrefix)
	}
	if out, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), setup.String()).CombinedOutput(); err != nil {
		t.Fatalf("subject prefix fixture: %v: %s", err, out)
	}
	mailbox, ok, err := client.resolveIndexMailbox("Work", "All Mail")
	if err != nil || !ok {
		t.Fatalf("subject prefix mailbox: %+v, %v, %v", mailbox, ok, err)
	}
	return client, mailbox, expected
}

func subjectPrefixText(value string) *string { return &value }

func TestIndexSubjectPrefixProjectionSearchAndIdentityParity(t *testing.T) {
	client, mailbox, expected := subjectPrefixFixture(t)
	messages, err := client.getMessagesFromIndex("Work", mailbox, 100, 0, false, false, "")
	if err != nil || len(messages) != len(expected) {
		t.Fatalf("prefixed listing: %+v, %v", messages, err)
	}
	var ids []string
	for _, message := range messages {
		if message.Subject != expected[message.ID] {
			t.Fatalf("subject text changed: ID %s got %q want %q", message.ID, message.Subject, expected[message.ID])
		}
		ids = append(ids, message.ID)
		envelope, err := client.getMessageEnvelopeFromIndex("Work", mailbox, message.ID)
		if err != nil || envelope == nil || envelope.Subject != message.Subject {
			t.Fatalf("envelope prefix parity: %+v, %v", envelope, err)
		}
		identity := StableIdentityFromMessage(message)
		count, err := client.countMessageIdentityInMailbox("Work", "All Mail", identity)
		want := 1
		if message.Subject == "topic" {
			want = 2
		}
		if err != nil || count != want {
			t.Fatalf("exact prefixed identity %q: %d, %v; want %d", message.Subject, count, err, want)
		}
		scope := identityVerificationScope{key: verificationMailboxKey{"Work", "All Mail"}, identities: []StableIdentity{identity}}
		result := client.getIdentitySnapshotForVerification([]identityVerificationScope{scope})[scope.key]
		matches := result.matches[identity.fallbackKey()]
		if result.err != nil || len(matches) != want {
			t.Fatalf("snapshot prefixed identity %q: %+v, %v", message.Subject, matches, result.err)
		}
		for _, match := range matches {
			if match.Subject != message.Subject {
				t.Fatalf("snapshot returned base-only or other prefix: %+v", match)
			}
		}
	}
	located, err := client.LocateMessages(ids)
	if err != nil || len(located) != len(expected) {
		t.Fatalf("located prefixed messages: %+v, %v", located, err)
	}
	verified, err := client.getMessageEnvelopesForVerification("Work", "All Mail", ids)
	if err != nil || len(verified) != len(expected) {
		t.Fatalf("verification envelope prefixes: %+v, %v", verified, err)
	}
	for id, want := range expected {
		if located[id].Envelope.Subject != want || verified[id].Subject != want {
			t.Fatalf("subject %s lost across projections: locate=%q verification=%q want=%q", id, located[id].Envelope.Subject, verified[id].Subject, want)
		}
	}
	for _, tc := range []struct {
		query string
		want  map[string]bool
	}{
		{"re:", map[string]bool{"203": true, "205": true, "207": true, "208": true}},
		{"fwd:", map[string]bool{"204": true}},
		{"wild", map[string]bool{"206": true}},
		{"unobserved-prefix:", map[string]bool{}},
	} {
		got, err := client.searchMessagesFromIndex(tc.query, "Work", mailbox, 100, "")
		if err != nil || len(got) != len(tc.want) {
			t.Fatalf("prefixed search %q: %+v, %v", tc.query, got, err)
		}
		for _, message := range got {
			if !tc.want[message.ID] || message.Subject != expected[message.ID] {
				t.Fatalf("prefixed search %q returned %+v", tc.query, message)
			}
		}
	}
	// Search tokenization intentionally accepts alphanumeric words; exercise
	// the lower-level SQL LIKE escaping separately with literal punctuation.
	conditions := indexSearchTermConditions([]string{"%", "_", `\`})
	var wildcardMatches []indexMessage
	query := buildIndexMessageSelect("Work", mailbox.Name) + " where m.deleted = 0 and " + strings.Join(conditions, " and ") + ";"
	if err := client.runEnvelopeIndexQuery(query, &wildcardMatches); err != nil || len(wildcardMatches) != 1 || wildcardMatches[0].ID != 206 {
		t.Fatalf("literal prefix wildcards: %+v, %v", wildcardMatches, err)
	}
}

func TestIndexSubjectPrefixMissingColumnFailsClosed(t *testing.T) {
	client, mailbox, _ := subjectPrefixFixture(t)
	if out, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), "alter table messages drop column subject_prefix;").CombinedOutput(); err != nil {
		t.Fatalf("remove prefix test schema column: %v: %s", err, out)
	}
	identity := StableIdentity{Subject: "Re: topic", Sender: "Sender <sender@example.com>", DateSent: "2023-07-22T04:26:40Z", MessageSize: 1234}
	if messages, err := client.getMessagesFromIndex("Work", mailbox, 100, 0, false, false, ""); err == nil || len(messages) != 0 {
		t.Fatalf("missing prefix schema became a successful listing: %+v, %v", messages, err)
	}
	if messages, err := client.searchMessagesFromIndex("Re:", "Work", mailbox, 100, ""); err == nil || len(messages) != 0 {
		t.Fatalf("missing prefix schema became a successful search: %+v, %v", messages, err)
	}
	if count, err := client.countMessageIdentityInMailbox("Work", "All Mail", identity); err == nil || count != 0 {
		t.Fatalf("missing prefix schema weakened identity: %d, %v", count, err)
	}
	scope := identityVerificationScope{key: verificationMailboxKey{"Work", "All Mail"}, identities: []StableIdentity{identity}}
	if result := client.getIdentitySnapshotForVerification([]identityVerificationScope{scope})[scope.key]; result.err == nil || result.matches != nil {
		t.Fatalf("missing prefix schema became verified absence: %+v", result)
	}
	if messages, err := client.LocateMessages([]string{"203"}); err == nil || len(messages) != 0 {
		t.Fatalf("missing prefix schema became successful location: %+v, %v", messages, err)
	}
}
