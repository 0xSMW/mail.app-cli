package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	netmail "net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// The origin is independent of the most recent extraction. Neither a new
// local ID nor a reserialized body may rewrite the original logical identity.
type readCheckpointOrigin struct {
	Account   string         `json:"account"`
	AccountID string         `json:"accountId,omitempty"`
	Identity  StableIdentity `json:"identity"`
}

type selectedReadHooks struct {
	Begin     func() func()
	AccountID func(string) (string, error)
	Recover   func(readCheckpointOrigin) (*Message, error)
	Confirm   func(MessageRef) (*Message, error)
	Revision  func(MessageRef, *StableIdentity) (string, error)
}

func secureReadIdentity(identity StableIdentity) bool {
	return validRFCMessageID(identity.RFCMessageID) != "" && identity.rfcEnvelopeKey() != "" && identity.Subject != "" && identity.DateSent != ""
}
func checkpointOriginMatches(origin readCheckpointOrigin, message *Message) bool {
	return message != nil && !message.Deleted && message.Account == origin.Account && origin.Identity.matchesEnvelope(*message)
}

// recoverCheckpointMessage uses the index only to narrow candidates. Every
// candidate is confirmed by an exact, read-only live lookup, and all matches
// must fit within the candidate cap before uniqueness can be established.
func (c *Client) recoverCheckpointMessage(origin readCheckpointOrigin) (*Message, error) {
	if !secureReadIdentity(origin.Identity) {
		return nil, fmt.Errorf("checkpoint has no corroborated RFC identity for relocation or changed content")
	}
	account, err := c.accountByName(origin.Account)
	if err != nil {
		return nil, err
	}
	if origin.AccountID != "" && account.ID != origin.AccountID {
		return nil, fmt.Errorf("checkpoint account identity changed")
	}
	if account.ID == "" {
		return nil, fmt.Errorf("checkpoint account identity is unavailable")
	}
	identity := origin.Identity
	query := fmt.Sprintf(`select m.ROWID as ID, mb.url as URL,
 `+indexSubjectExpression+` as Subject,
 case when coalesce(a.comment,'')='' then coalesce(a.address,'') else a.comment || ' <' || a.address || '>' end as Sender,
 coalesce(strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ',m.date_sent,'unixepoch'),'') as DateSent,
 m.size as MessageSize
 from messages m join mailboxes mb on mb.ROWID=m.mailbox
 join subjects s on s.ROWID=m.subject join addresses a on a.ROWID=m.sender
 where m.deleted=0 and %s and lower(coalesce(a.address,''))=%s
 and `+indexSubjectExpression+`=%s and coalesce(strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ',m.date_sent,'unixepoch'),'')=%s
 order by m.ROWID limit %d;`, indexAccountCondition(account.ID), sqlQuote(stableSenderAddress(identity.Sender)), sqlQuote(identity.Subject), sqlQuote(canonicalIdentityDate(identity.DateSent)), maxVerificationIdentityCandidates+1)
	var rows []struct {
		indexMessage
		URL string
	}
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, fmt.Errorf("locate checkpoint identity: %w", err)
	}
	if len(rows) > maxVerificationIdentityCandidates {
		return nil, fmt.Errorf("checkpoint identity exceeds %d candidate limit", maxVerificationIdentityCandidates)
	}
	seen := map[string]bool{}
	var matches []*Message
	for _, row := range rows {
		if err := c.Done(); err != nil {
			return nil, err
		}
		if row.ID <= 0 || indexMailboxAccountID(row.URL) != account.ID {
			return nil, fmt.Errorf("checkpoint candidate has invalid account or local ID")
		}
		id := strconv.FormatInt(row.ID, 10)
		if seen[id] {
			continue
		}
		seen[id] = true
		path, err := checkpointMailboxPath(row.URL)
		if err != nil {
			return nil, err
		}
		indexed := row.indexMessage.message()
		if StableIdentityFromMessage(indexed).rfcEnvelopeKey() != identity.rfcEnvelopeKey() {
			return nil, fmt.Errorf("checkpoint candidate metadata differs from indexed predicate")
		}
		actual, err := c.checkpointExactMetadata(MessageRef{AccountName: origin.Account, MailboxName: path, MessageID: id})
		if err != nil {
			return nil, fmt.Errorf("confirm checkpoint candidate %s: %w", id, err)
		}
		if actual == nil || actual.ID != id || actual.Account != origin.Account || actual.Deleted {
			return nil, fmt.Errorf("checkpoint candidate %s disappeared or changed account", id)
		}
		actualIdentity := StableIdentityFromMessage(*actual)
		if actualIdentity.rfcEnvelopeKey() != identity.rfcEnvelopeKey() || actualIdentity.RFCMessageID == "" {
			return nil, fmt.Errorf("checkpoint candidate %s has inconsistent live identity", id)
		}
		if origin.Identity.matchesEnvelope(*actual) {
			matches = append(matches, actual)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("checkpoint identity must match exactly one message in original account (found %d)", len(matches))
	}
	return matches[0], nil
}

func checkpointMailboxPath(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid checkpoint mailbox URL")
	}
	var parts []string
	for _, part := range strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/") {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.Contains(decoded, "/") {
			return "", fmt.Errorf("invalid checkpoint physical mailbox path")
		}
		parts = append(parts, decoded)
	}
	return strings.Join(parts, "/"), nil
}

// checkpointExactMetadata preserves the complete physical mailbox path, even
// when another nested mailbox or special alias has the same leaf name.
func (c *Client) checkpointExactMetadata(ref MessageRef) (*Message, error) {
	if c.nativeEnabled() {
		var result *Message
		err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
			account, err := n.account(ref.AccountName)
			if err != nil {
				return err
			}
			box, err := n.mailboxExact(account, ref.MailboxName)
			if err != nil {
				return err
			}
			message, err := n.message(box, ref.MessageID)
			if err != nil {
				return err
			}
			result, err = n.messageMetadata(message, ref.AccountName, ref.MailboxName)
			return err
		})
		return result, err
	}
	script := jxaTrashMailboxHelper() + jxaRFCMessageIDHelper() + fmt.Sprintf(`
 const mail=Application('Mail');
 const account=uniqueTrashAccount(mail,'%s');
 const box=deleteSourceMailbox(account,'%s',true);
 const candidates=box.messages.whose({id:Number('%s')})();
 if(candidates.length!==1) throw new Error('checkpoint candidate not found uniquely in exact mailbox');
 const msg=candidates[0];
 JSON.stringify({%s});`, escapeJSString(ref.AccountName), escapeJSString(ref.MailboxName), escapeJSString(ref.MessageID), jxaMessageProjection{ID: "String(msg.id())", Size: "msg.messageSize()", Mailbox: jsQuote(ref.MailboxName), Account: "account.name()", RFCMessageID: "rfcMessageId(msg)", Content: "deleted: msg.deletedStatus(),"}.fields())
	return c.runJXAMessage(script, defaultAutomationTimeout, "checkpoint exact metadata")
}

func jsQuote(value string) string { return "'" + escapeJSString(value) + "'" }

// checkpointContentRevision hashes only complete RFC bytes, excluding Mail's
// mutable plist trailer. Scoped membership and the file's own headers guard
// against attaching a digest to an unrelated file with a recycled local ID.
func (c *Client) checkpointContentRevision(ref MessageRef, expected *StableIdentity) (string, error) {
	if err := c.Done(); err != nil {
		return "", err
	}
	path, absent, err := c.scopedLocalMessageFile(ref.AccountName, ref.MailboxName, ref.MessageID)
	if err != nil {
		return "", err
	}
	if absent || path == "" || strings.HasSuffix(path, ".partial.emlx") {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxMessageFileSize {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(contextReadBuffer{ctx: c.Context(), reader: file}, maxMessageFileSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxMessageFileSize {
		return "", nil
	}
	newline := bytes.IndexByte(data, '\n')
	if newline < 0 {
		return "", nil
	}
	size, err := strconv.Atoi(strings.TrimSpace(string(data[:newline])))
	if err != nil || size <= 0 || size > len(data)-newline-1 {
		return "", nil
	}
	raw := data[newline+1 : newline+1+size]
	parsed, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", nil
	}
	if expected != nil {
		message := Message{MessageSize: expected.MessageSize, Sender: strings.TrimSpace(parsed.Header.Get("From"))}
		sent, err := netmail.ParseDate(parsed.Header.Get("Date"))
		if err != nil {
			return "", nil
		}
		message.DateSent = sent.UTC().Format(time.RFC3339)
		if err := populateMIMEEnvelope(&message, parsed.Header); err != nil {
			return "", nil
		}
		if !expected.matchesEnvelope(message) {
			return "", fmt.Errorf("local content headers differ from checkpoint identity")
		}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type contextReadBuffer struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReadBuffer) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Keep operation timeouts tied to the remaining item deadline, including time
// already spent locating candidates and reading revisions.
func checkpointRemainingTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return max(min(time.Until(deadline), fallback), time.Nanosecond)
	}
	return fallback
}

func resumeCheckpointBody(saved MessageReadResult, origin readCheckpointOrigin, savedRevision string, located MessageRef, hooks selectedReadHooks, metadata func(MessageRef) (*Message, error), read func(MessageRef, int) (*Message, *PhaseTrace, error), index int, mailBody bool) (*Message, *PhaseTrace, string, error) {

	// Resume from the most recently confirmed address, retaining the original
	// numeric selection key in the outer result. A fresh locator can improve
	// its mailbox when it still refers to that same local ID and account.
	ref := MessageRef{AccountName: origin.Account, MailboxName: saved.Message.Mailbox, MessageID: saved.Message.ID}
	if located.AccountName == origin.Account && located.MessageID == ref.MessageID {
		ref.MailboxName = located.MailboxName
	}
	fresh, metadataErr := metadata(ref)
	recovered := false
	recover := func() error {
		if !secureReadIdentity(origin.Identity) || hooks.Recover == nil {
			return fmt.Errorf("checkpoint account or stable identity cannot be confirmed; no secure RFC recovery available")
		}
		var err error
		fresh, err = hooks.Recover(origin)
		if err != nil {
			return err
		}
		if !checkpointOriginMatches(origin, fresh) || fresh.ID == "" || fresh.Mailbox == "" {
			return fmt.Errorf("recovered message differs from original logical identity")
		}
		ref = MessageRef{AccountName: origin.Account, MailboxName: fresh.Mailbox, MessageID: fresh.ID}
		recovered = true
		return nil
	}
	if metadataErr != nil || !checkpointOriginMatches(origin, fresh) || fresh.ID != ref.MessageID {
		// Cancellation cannot be converted into a second recovery attempt.
		if metadataErr != nil && (errors.Is(metadataErr, context.Canceled) || errors.Is(metadataErr, context.DeadlineExceeded)) {
			return nil, nil, "", metadataErr
		}
		if err := recover(); err != nil {
			return nil, nil, "", err
		}
	}
	revision := ""
	var err error
	if !mailBody && hooks.Revision != nil {
		revision, err = hooks.Revision(ref, &origin.Identity)
		if err != nil {
			return nil, nil, "", err
		}
	}
	extractionValid := saved.Message.ContentError == "" && (saved.Message.BodyDiagnostics == nil || !saved.Message.BodyDiagnostics.PartialFile && saved.Message.BodyDiagnostics.ExtractionError == "")
	if saved.Message.ContentSource == BodySourceDisk && extractionValid && savedRevision != "" && revision == savedRevision && fresh.MessageSize == saved.Message.MessageSize {
		message, err := cachedBodyWithFreshMetadata(*saved.Message, *fresh)
		return message, nil, revision, err
	}
	if !secureReadIdentity(origin.Identity) {
		return nil, nil, "", fmt.Errorf("content revision changed or is unavailable; strict identity without corroborated RFC cannot authorize replacement content")
	}
	if !recovered {
		if err := recover(); err != nil {
			return nil, nil, "", err
		}
	}
	// Recheck the exact recovered address for the revision used to bracket
	// extraction. A post-read literal lookup below also guards alias-aware body
	// transports from confirming a different physical mailbox or message.
	before := ""
	if !mailBody && hooks.Revision != nil {
		before, err = hooks.Revision(ref, &origin.Identity)
		if err != nil {
			return nil, nil, "", err
		}
	}
	message, trace, err := read(ref, index)
	if err != nil {
		return nil, trace, "", err
	}
	if message == nil || message.ContentError != "" {
		return nil, trace, "", fmt.Errorf("refreshed body unavailable")
	}
	if !checkpointOriginMatches(origin, message) || message.ID != ref.MessageID {
		return nil, trace, "", fmt.Errorf("refreshed extraction differs from original account or logical identity")
	}
	confirm := metadata
	if hooks.Confirm != nil {
		confirm = hooks.Confirm
	}
	after, err := confirm(ref)
	if err != nil {
		return nil, trace, "", fmt.Errorf("confirm extraction address: %w", err)
	}
	if !checkpointOriginMatches(origin, after) || after.ID != ref.MessageID || after.MessageSize != message.MessageSize {
		return nil, trace, "", fmt.Errorf("message changed identity or size during extraction")
	}
	revision = ""
	if message.ContentSource == BodySourceDisk && !mailBody && hooks.Revision != nil {
		afterRevision, err := hooks.Revision(ref, &origin.Identity)
		if err != nil {
			return nil, trace, "", err
		}
		if before != "" && afterRevision != "" {
			if before != afterRevision {
				return nil, trace, "", fmt.Errorf("content changed during extraction")
			}
			revision = afterRevision
		}
	}
	merged, err := cachedBodyWithFreshMetadata(*message, *after)
	return merged, trace, revision, err
}

// Read the account's live ID independently of the client's account cache, so
// deletion/recreation or reassignment of a display name cannot rebind origin.
func (c *Client) checkpointAccountID(name string) (string, error) {
	var id string
	if c.nativeEnabled() {
		err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
			account, err := n.account(name)
			if err != nil {
				return err
			}
			id, err = n.stringProperty("ID  ", account)
			return err
		})
		if err != nil {
			return "", err
		}
	} else {
		var result struct {
			ID string `json:"id"`
		}
		script := jxaTrashMailboxHelper() + fmt.Sprintf(`const mail=Application('Mail');const account=uniqueTrashAccount(mail,'%s');JSON.stringify({id:trashAccountID(account)});`, escapeJSString(name))
		if err := c.runJXAJSON(script, defaultAutomationTimeout, "checkpoint account identity", &result); err != nil {
			return "", err
		}
		id = result.ID
	}
	if id == "" {
		return "", fmt.Errorf("checkpoint account identity unavailable")
	}
	return id, nil
}
