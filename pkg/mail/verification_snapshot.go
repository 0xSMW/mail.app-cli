package mail

import (
	"encoding/json"
	"fmt"
	"strings"
)

type verificationMailboxKey struct{ account, mailbox string }

type identityVerificationScope struct {
	key        verificationMailboxKey
	identities []StableIdentity
}

type identityVerificationResult struct {
	matches map[string][]Message
	err     error
}

// getIdentitySnapshotForVerification resolves mailbox IDs and reads all
// requested identities in one SQLite statement. Source and destination thus
// describe the same database snapshot, including label membership and new IDs.
// A missing mailbox is an error, never evidence that a message is absent.
func (c *Client) getIdentitySnapshotForVerification(scopes []identityVerificationScope) map[verificationMailboxKey]identityVerificationResult {
	results := make(map[verificationMailboxKey]identityVerificationResult, len(scopes))
	var ctes, selects []string
	var queried []identityVerificationScope
	for _, scope := range scopes {
		account, err := c.accountByName(scope.key.account)
		if err != nil {
			results[scope.key] = identityVerificationResult{err: err}
			continue
		}
		conditions, err := indexIdentityConditions(scope.identities)
		if err != nil {
			results[scope.key] = identityVerificationResult{err: err}
			continue
		}
		if len(conditions) == 0 {
			results[scope.key] = identityVerificationResult{matches: map[string][]Message{}}
			continue
		}
		index := len(queried)
		queried = append(queried, scope)
		name := fmt.Sprintf("verification_mailbox_%d", index)
		ctes = append(ctes, name+" as ("+verificationMailboxSelect(account.ID, scope.key.mailbox)+")")
		// All Mail and Archive use the backing mailbox, while other mailboxes
		// also include their labels, as in indexMailboxMembershipCondition.
		// GLOB treats the URL-encoded percent sign literally; LIKE would also
		// match custom labels such as All%20Work%20Mail as All Mail.
		membership := `(m.mailbox = box.ID or (
			lower(box.URL) not glob '*/all%20mail' and lower(box.URL) not glob '*/all mail'
			and lower(box.URL) not glob '*/archive'
			and ` + indexLabelMembershipCondition("box.ID") + "))"
		selects = append(selects, fmt.Sprintf(`select %d as VerificationScope,
		(select ID from %s) as MailboxID,
		(select URL from %s) as MailboxURL,
		(select json_group_array(json_object(
			'ID', m.ROWID, 'Subject', coalesce(s.subject, ''),
			'Sender', case when coalesce(a.comment, '') = '' then coalesce(a.address, '') else a.comment || ' <' || a.address || '>' end,
			'DateSent', coalesce(strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', m.date_sent, 'unixepoch'), ''),
			'MessageSize', m.size, 'Read', m.read))
		from messages m join subjects s on s.ROWID = m.subject
		join addresses a on a.ROWID = m.sender cross join %s box
		where %s and m.deleted = 0 and (%s)) as Matches`, index, name, name, name, membership, verificationIdentityPredicate(conditions)))
	}
	if len(queried) == 0 {
		return results
	}
	var rows []struct {
		VerificationScope int
		MailboxID         *int
		MailboxURL        string
		Matches           string
	}
	err := c.runEnvelopeIndexQuery("with "+strings.Join(ctes, ",\n")+"\n"+verificationUnion(selects)+";", &rows)
	for _, scope := range queried {
		results[scope.key] = identityVerificationResult{err: fmt.Errorf("mailbox unavailable in Envelope Index: %s", scope.key.mailbox)}
		if err != nil {
			results[scope.key] = identityVerificationResult{err: err}
		}
	}
	if err != nil {
		return results
	}
	seen := make(map[int]bool)
	for _, row := range rows {
		if row.VerificationScope < 0 || row.VerificationScope >= len(queried) || seen[row.VerificationScope] {
			// A malformed snapshot must not produce a successful relocation.
			for _, scope := range queried {
				results[scope.key] = identityVerificationResult{err: fmt.Errorf("invalid verification snapshot scope")}
			}
			return results
		}
		seen[row.VerificationScope] = true
		scope := queried[row.VerificationScope]
		if row.MailboxID == nil || row.MailboxURL == "" {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(row.Matches), "[") {
			results[scope.key] = identityVerificationResult{err: fmt.Errorf("verification snapshot messages must be an array")}
			continue
		}
		var messages []indexMessage
		if err := json.Unmarshal([]byte(row.Matches), &messages); err != nil {
			results[scope.key] = identityVerificationResult{err: fmt.Errorf("invalid verification snapshot messages: %w", err)}
			continue
		}
		matches := make(map[string][]Message)
		for _, message := range indexMessagesToMessages(messages) {
			if message.ID == "0" || !StableIdentityFromMessage(message).hasFallback() {
				results[scope.key] = identityVerificationResult{err: fmt.Errorf("verification snapshot message lacks indexed identity")}
				matches = nil
				break
			}
			message.Account, message.Mailbox = scope.key.account, mailboxLeafFromURL(row.MailboxURL)
			key := StableIdentityFromMessage(message).fallbackKey()
			matches[key] = append(matches[key], message)
		}
		if matches == nil {
			continue
		}
		results[scope.key] = identityVerificationResult{matches: matches}
	}
	return results
}

// Large identity sets must not turn a compound snapshot into a linear-depth
// expression tree. Parentheses preserve every exact tuple while bounding depth.
func verificationIdentityPredicate(conditions []string) string {
	if len(conditions) <= 32 {
		return strings.Join(conditions, " or ")
	}
	middle := len(conditions) / 2
	return "(" + verificationIdentityPredicate(conditions[:middle]) + ") or (" + verificationIdentityPredicate(conditions[middle:]) + ")"
}

// SQLite limits each compound SELECT to 500 terms. Nest bounded groups in the
// same statement so large batches retain a single consistent database snapshot.
func verificationUnion(selects []string) string {
	const termsPerGroup = 100
	if len(selects) <= termsPerGroup {
		return strings.Join(selects, "\nunion all\n")
	}
	groups := make([]string, 0, (len(selects)+termsPerGroup-1)/termsPerGroup)
	for start := 0; start < len(selects); start += termsPerGroup {
		end := min(start+termsPerGroup, len(selects))
		groups = append(groups, "select * from ("+strings.Join(selects[start:end], "\nunion all\n")+")")
	}
	return verificationUnion(groups)
}

// Preserve resolveIndexMailbox's exact/Gmail, literal Archive, nested Archive,
// then broad nested-leaf preference, resolving it afresh in every snapshot.
func verificationMailboxSelect(accountID, mailbox string) string {
	return indexMailboxSelect(accountID, mailbox, false)
}
