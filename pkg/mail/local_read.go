package mail

import (
	"fmt"
	"io"
	"mime"
	netmail "net/mail"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
)

func indexAccountCondition(accountID string) string {
	if accountID == "" {
		return "0"
	}
	// Physical owners use the account authority of their indexed URL. Keep
	// this exact and case-sensitive without assuming the provider's scheme.
	authority := "substr(mb.url, instr(mb.url, '://') + 3)"
	return fmt.Sprintf("instr(mb.url, '://') > 1 and substr(%s, 1, instr(%s, '/') - 1) = %s collate binary",
		authority, authority, sqlQuote(accountID))
}

// resolveLocalMessageScope checks membership separately from physical ownership.
// An empty condition means the mailbox could not be resolved locally.
func (c *Client) resolveLocalMessageScope(accountName, mailboxName string) (*indexMailbox, string, error) {
	mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName)
	if err != nil || !ok {
		return nil, "", err
	}
	account, err := c.accountByName(accountName)
	if err != nil {
		return nil, "", err
	}
	return mbox, indexMailboxMembershipCondition(mbox) + " and " + indexAccountCondition(account.ID), nil
}

func (c *Client) nativeDiskAccessError(err error) error {
	if err != nil && c.nativeEnabled() && (os.IsPermission(err) || strings.Contains(strings.ToLower(err.Error()), "operation not permitted")) {
		return &nativeIndexError{err}
	}
	return nil
}

// getMessageDetailsFromDisk returns handled=true only after the requested
// membership is observed and a complete local MIME file is decoded. A missing
// index member leaves absence to a scoped live lookup because the index may lag.
// A partial/missing/undecodable file returns handled=false, retaining index
// metadata and diagnostics for callers that ask Mail for unavailable content.
func (c *Client) getMessageDetailsFromDisk(accountName, mailboxName, messageID string) (*Message, bool, error) {
	return c.getMessageDetailsFromDiskMode(accountName, mailboxName, messageID, !c.shared.bodyFromMail)
}

func (c *Client) getMessageDetailsFromDiskMode(accountName, mailboxName, messageID string, enabled bool) (*Message, bool, error) {
	if !enabled {
		return nil, false, nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64)
	if err != nil || id <= 0 {
		return nil, false, nil
	}
	mbox, membership, err := c.resolveLocalMessageScope(accountName, mailboxName)
	if err != nil || mbox == nil {
		return nil, false, err
	}
	selectSQL := strings.Replace(buildIndexMessageSelect(accountName, mbox.Name), "\nfrom messages m", ",\nmb.url as OwnerURL\nfrom messages m", 1)
	query := selectSQL + fmt.Sprintf(" where m.ROWID = %d and m.deleted = 0 and %s limit 1;", id, membership)
	var rows []struct {
		indexMessage
		OwnerURL string
	}
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	message := rows[0].indexMessage.message()
	path, err := messageFilePathForMailbox(id, rows[0].OwnerURL)
	if err != nil {
		if denied := c.nativeDiskAccessError(err); denied != nil {
			return &message, false, denied
		}
		return &message, false, nil
	}
	// Keep the indexed envelope on a native access failure, as before.
	hydrated := message
	complete, err := c.hydrateLocalMessage(&hydrated, path)
	if err != nil {
		return &message, false, err
	}
	return &hydrated, complete, nil
}

// hydrateLocalMessage enriches a known physical file without rendering in Mail.
// Decode failures remain content diagnostics; only native access denial is fatal.
func (c *Client) hydrateLocalMessage(message *Message, path string) (bool, error) {
	rfc, err := readEMLXMessage(path)
	if err != nil {
		message.BodyDiagnostics = failedEMLXDiagnostics(path, err)
		message.ContentError = err.Error()
		return false, c.nativeDiskAccessError(err)
	}
	envelopeErr := populateMIMEEnvelope(message, rfc.Header)
	body, diagnostics, err := extractEMLXBody(rfc, path)
	populateDiskLinks(message, diagnostics, err)
	trimBodyAlternatives(diagnostics, c.shared.bodyAlternatives)
	message.BodyDiagnostics = diagnostics
	if envelopeErr != nil {
		message.ContentError = envelopeErr.Error()
		message.LinkStatus.Complete = false
		message.LinkStatus.Warnings = append(message.LinkStatus.Warnings, "Envelope metadata: "+envelopeErr.Error())
		return false, nil
	}
	if err != nil {
		message.ContentError = err.Error()
		return false, nil
	}
	message.Content, message.ContentSource = body, BodySourceDisk
	if diagnostics.PartialFile {
		message.ContentError = errPartialBodyOnDisk.Error()
		return false, nil
	}
	return true, nil
}

func failedEMLXDiagnostics(path string, err error) *BodyDiagnostics {
	diagnostics := &BodyDiagnostics{PartialFile: strings.HasSuffix(path, ".partial.emlx"), ExtractionError: err.Error()}
	diagnostics.finish()
	return diagnostics
}

func headerWordDecoder() *mime.WordDecoder {
	return &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		encoding, err := htmlindex.Get(charset)
		if err != nil {
			return nil, err
		}
		return encoding.NewDecoder().Reader(input), nil
	}}
}

func populateMIMEEnvelope(message *Message, header netmail.Header) error {
	populateUnsubscribe(message, header)
	decoder := headerWordDecoder()
	message.InReplyTo = header.Get("In-Reply-To")
	message.References = header.Get("References")
	if subject := header.Get("Subject"); subject != "" {
		decoded, err := decoder.DecodeHeader(subject)
		if err != nil {
			return fmt.Errorf("decode Subject: %w", err)
		}
		message.Subject = decoded
	}
	parser := &netmail.AddressParser{WordDecoder: decoder}
	for _, field := range []struct {
		name   string
		target *[]string
	}{
		{"To", &message.ToRecipients}, {"Cc", &message.CcRecipients}, {"Bcc", &message.BccRecipients},
	} {
		*field.target = []string{}
		// Preserve repeated recipient headers instead of taking only Header.Get.
		for _, raw := range header[field.name] {
			addresses, err := parser.ParseList(raw)
			if err != nil {
				return fmt.Errorf("parse %s recipients: %w", field.name, err)
			}
			for _, address := range addresses {
				*field.target = append(*field.target, address.Address)
			}
		}
	}
	if raw := strings.TrimSpace(header.Get("Message-ID")); len(raw) > 2 && raw[0] == '<' && raw[len(raw)-1] == '>' && !strings.ContainsAny(raw[1:len(raw)-1], "<> \t\r\n") {
		message.RFCMessageID = raw
	}
	return nil
}

func trimBodyAlternatives(diagnostics *BodyDiagnostics, include bool) {
	if diagnostics == nil || include {
		return
	}
	for i := range diagnostics.Alternatives {
		diagnostics.Alternatives[i].Text = ""
	}
}

func extractEMLXBody(rfc *netmail.Message, path string) (content string, diagnostics *BodyDiagnostics, extractionErr error) {
	localBodyExtractions.Add(1)
	started := time.Now()
	diagnostics = &BodyDiagnostics{PartialFile: strings.HasSuffix(path, ".partial.emlx")}
	if diagnostics.PartialFile {
		localPartialBodies.Add(1)
	}
	defer func() {
		localBodyNanoseconds.Add(int64(time.Since(started)))
		if extractionErr != nil {
			localExtractionErrors.Add(1)
			diagnostics.ExtractionError = extractionErr.Error()
		}
		diagnostics.finish()
	}()
	header := map[string][]string(rfc.Header)
	text, err := mimeBodyTextWithDiagnostics(func(key string) string { return firstHeader(header, key) }, rfc.Body, false, 0, "1", diagnostics)
	if err != nil {
		return "", diagnostics, err
	}
	text = normalizeBodyText(text)
	if text == "" {
		return "", diagnostics, fmt.Errorf("message file holds no text body")
	}
	diagnostics.ExtractedCharCount = utf8.RuneCountInString(text)
	return text, diagnostics, nil
}

// messageFilePathsForMessages resolves physical ownership in one scoped query
// for the whole listing. Gmail labels establish membership; file locations
// always come from the row's owning mailbox, never from the requested label.
func (c *Client) messageFilePathsForMessages(accountName, mailboxName string, messages []Message) (map[string]string, error) {
	type scope struct{ account, mailbox string }
	groups := make(map[scope][]string)
	for _, message := range messages {
		a, m := message.Account, message.Mailbox
		if accountName != "" {
			a = accountName
		}
		if mailboxName != "" {
			m = mailboxName
		}
		id, err := strconv.ParseInt(message.ID, 10, 64)
		if err == nil && id > 0 && a != "" && m != "" {
			groups[scope{a, m}] = append(groups[scope{a, m}], strconv.FormatInt(id, 10))
		}
	}
	paths := make(map[string]string)
	var predicates []string
	for group, ids := range groups {
		_, membership, err := c.resolveLocalMessageScope(group.account, group.mailbox)
		if err != nil {
			return nil, err
		}
		if membership != "" {
			predicates = append(predicates, "(m.ROWID in ("+strings.Join(ids, ",")+") and "+membership+")")
		}
	}
	if len(predicates) == 0 {
		return paths, nil
	}
	query := "select m.ROWID as ID, mb.url as URL from messages m join mailboxes mb on mb.ROWID = m.mailbox where m.deleted = 0 and (" + strings.Join(predicates, " or ") + ");"
	var rows []struct {
		ID  int64
		URL string
	}
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if path, err := messageFilePathForMailbox(row.ID, row.URL); err == nil {
			paths[strconv.FormatInt(row.ID, 10)] = path
		} else if denied := c.nativeDiskAccessError(err); denied != nil {
			return nil, denied
		}
	}
	return paths, nil
}

// enrichMessagesFromDisk limits simultaneous allocations to four bounded
// message files. Each worker owns a distinct output element; diagnostics are
// kept even when a partial file needs a subsequent Mail content request.
func (c *Client) enrichMessagesFromDisk(accountName, mailboxName string, messages []Message) ([]bool, error) {
	complete := make([]bool, len(messages))
	if c.shared.bodyFromMail {
		return complete, nil
	}
	paths, err := c.messageFilePathsForMessages(accountName, mailboxName, messages)
	if err != nil {
		return complete, err
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	var accessMu sync.Mutex
	var accessErr error
	for worker := 0; worker < 4 && worker < len(messages); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if c.Done() != nil {
					continue
				}
				path := paths[messages[i].ID]
				if path == "" {
					continue
				}
				var err error
				complete[i], err = c.hydrateLocalMessage(&messages[i], path)
				if err != nil {
					accessMu.Lock()
					if accessErr == nil {
						accessErr = err
					}
					accessMu.Unlock()
				}
			}
		}()
	}
	for i := range messages {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	if accessErr != nil {
		return complete, accessErr
	}
	return complete, c.Done()
}
