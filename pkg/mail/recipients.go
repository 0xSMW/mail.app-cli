package mail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	netmail "net/mail"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

const maxRecipientHeaderBytes = 256 << 10
const recipientReadBudget = 15 * time.Second

// Available means the stored field was inspected, including when absent. It
// does not reconstruct envelope recipients or Bcc removed before delivery.
type RecipientFieldStatus struct {
	State  string `json:"state"`
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}
type RecipientStatus struct {
	To          RecipientFieldStatus `json:"to"`
	Cc          RecipientFieldStatus `json:"cc"`
	Bcc         RecipientFieldStatus `json:"bcc"`
	DeliveredTo RecipientFieldStatus `json:"deliveredTo"`
	OriginalTo  RecipientFieldStatus `json:"originalTo"`
}

func (s RecipientStatus) withDefaults() RecipientStatus {
	for _, field := range []*RecipientFieldStatus{&s.To, &s.Cc, &s.Bcc, &s.DeliveredTo, &s.OriginalTo} {
		if field.State == "" {
			field.State = "not-requested"
		}
	}
	return s
}
func (s RecipientStatus) complete() bool {
	for _, field := range []RecipientFieldStatus{s.To, s.Cc, s.Bcc, s.DeliveredTo, s.OriginalTo} {
		if field.State != "available" {
			return false
		}
	}
	return true
}

type recipientField struct {
	name   string
	values *[]string
	status *RecipientFieldStatus
}

func recipientFields(m *Message) []recipientField {
	return []recipientField{{"To", &m.ToRecipients, &m.RecipientStatus.To}, {"Cc", &m.CcRecipients, &m.RecipientStatus.Cc}, {"Bcc", &m.BccRecipients, &m.RecipientStatus.Bcc}, {"Delivered-To", &m.DeliveredTo, &m.RecipientStatus.DeliveredTo}, {"X-Original-To", &m.OriginalTo, &m.RecipientStatus.OriginalTo}}
}
func unavailableRecipients(m *Message, err error) {
	for _, field := range recipientFields(m) {
		if field.status.State != "available" {
			if field.status.State == "partial" && len(*field.values) > 0 {
				continue
			}
			*field.values = nil
			if field.status.State != "unavailable" || field.status.Error == "" {
				*field.status = RecipientFieldStatus{State: "unavailable", Error: err.Error()}
			}
		}
	}
}

// Each field parses independently: a malformed Cc cannot erase a valid To.
func populateRecipientHeaders(m *Message, header netmail.Header, source string) {
	parser := &netmail.AddressParser{WordDecoder: headerWordDecoder()}
	for _, field := range recipientFields(m) {
		if field.status.State == "available" {
			continue
		}
		values := []string{}
		var parseErr error
		for _, raw := range header[field.name] {
			// Empty Bcc is valid; empty destination fields carry no observed addresses.
			if strings.TrimSpace(raw) == "" {
				continue
			}
			addresses, err := parseRecipientAddressList(parser, raw)
			if err != nil {
				parseErr = fmt.Errorf("parse %s recipients: %w", field.name, err)
				continue
			}
			for _, address := range addresses {
				values = append(values, address.Address)
			}
		}
		if parseErr != nil {
			// Preserve validated addresses from other repetitions of this field.
			appendRecipientFieldEvidence(field, values, source)
			recipientFieldFailure(field, source, parseErr)
		} else {
			*field.values = values
			*field.status = RecipientFieldStatus{State: "available", Source: source}
		}
	}
}

// readEMLXRecipientHeaders stops at the header terminator. It accepts complete
// headers in partial files and never parses attachments, the body, or the plist.
func readEMLXRecipientHeaders(path string) (netmail.Header, error) {
	localFilesRead.Add(1)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("message file is not regular")
	}
	reader := bufio.NewReader(file)
	lengthLine, err := reader.ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("read message length: %w", err)
	}
	length, err := strconv.ParseInt(strings.TrimSpace(string(lengthLine)), 10, 64)
	if err != nil || length <= 0 {
		return nil, fmt.Errorf("message file has an invalid length line")
	}
	limit := length
	if limit > maxRecipientHeaderBytes+1 {
		limit = maxRecipientHeaderBytes + 1
	}
	bounded := bufio.NewReader(io.LimitReader(reader, limit))
	var raw bytes.Buffer
	for {
		line, err := bounded.ReadString('\n')
		if raw.Len()+len(line) > maxRecipientHeaderBytes {
			return nil, fmt.Errorf("message headers exceed %d bytes", maxRecipientHeaderBytes)
		}
		if err != nil {
			return nil, fmt.Errorf("message headers have no complete terminator")
		}
		if line != "\n" && line != "\r\n" && !recipientHeaderLine(line, raw.Len() > 0) {
			return parseRecipientHeaderPrefix(raw.Bytes())
		}
		raw.WriteString(line)
		if line == "\n" || line == "\r\n" {
			message, err := netmail.ReadMessage(bytes.NewReader(raw.Bytes()))
			if err != nil {
				return nil, fmt.Errorf("parse message headers: %w", err)
			}
			return message.Header, nil
		}
	}
}

// EnrichMessagesWithRecipients reads fresh recipient evidence for each selected
// row. It preserves envelope/body data and records failures on the affected
// fields; callers need not fetch content to inspect recipient availability.
func (c *Client) EnrichMessagesWithRecipients(accountName, mailboxName string, messages []Message) []Message {
	return c.enrichMessagesWithRecipients(accountName, mailboxName, messages, "")
}

// A known positive exact match needs no further Mail reads. Index evidence
// remains partial and cannot establish absence or a complete recipient list.
func (c *Client) enrichMessagesWithRecipients(accountName, mailboxName string, messages []Message, recipient string) []Message {
	if len(messages) == 0 {
		return messages
	}
	ctx, cancel := context.WithTimeout(c.Context(), recipientReadBudget)
	defer cancel()
	client := c.WithContext(ctx)
	// Copy the slice so enriching cached or journal envelopes cannot mutate the
	// caller's original inventory. Discard old evidence before a fresh read.
	messages = append([]Message(nil), messages...)
	for i := range messages {
		messages[i].ToRecipients, messages[i].CcRecipients, messages[i].BccRecipients = nil, nil, nil
		messages[i].DeliveredTo, messages[i].OriginalTo = nil, nil
		messages[i].RecipientStatus = RecipientStatus{}
	}
	paths, pathErr := client.messageFilePathsForMessages(accountName, mailboxName, messages)
	if pathErr == nil {
		jobs := make(chan int)
		var workers sync.WaitGroup
		for worker := 0; worker < 4 && worker < len(messages); worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for i := range jobs {
					if ctx.Err() != nil {
						continue
					}
					if path := paths[messages[i].ID]; path != "" {
						header, err := readEMLXRecipientHeaders(path)
						if err == nil {
							populateRecipientHeaders(&messages[i], header, "disk")
						} else if header != nil {
							populateRecipientHeaderPrefix(&messages[i], header, "disk", err)
						} else {
							unavailableRecipients(&messages[i], err)
						}
					}
				}
			}()
		}
		for i := range messages {
			jobs <- i
		}
		close(jobs)
		workers.Wait()
	}
	// The optional recipient index can preserve positive addressed evidence when
	// cached headers are missing. Unsupported schemas still use scoped Mail reads.
	_ = client.enrichRecipientIndexEvidence(accountName, mailboxName, messages)
	pending := []int{}
	for i := range messages {
		if recipient != "" {
			if matched, _ := MatchRecipient(messages[i], recipient); matched {
				continue
			}
		}
		if !messages[i].RecipientStatus.complete() {
			pending = append(pending, i)
		}
	}
	// Resolve addressed recipients before spending the bounded Mail budget on
	// optional delivery-chain fields that cannot establish an exact match.
	addressed, delivery := []int{}, []int{}
	for _, i := range pending {
		needsAddress := false
		for _, field := range recipientFields(&messages[i])[:3] {
			if field.status.State != "available" {
				needsAddress = true
			}
		}
		if needsAddress {
			addressed = append(addressed, i)
		} else {
			delivery = append(delivery, i)
		}
	}
	pending = append(addressed, delivery...)
	if len(pending) > 0 {
		var err error
		attempted := make(map[int]bool)
		completed := make(map[int]bool)
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if client.nativeEnabled() {
			err = client.withNativeAutomation(recipientReadBudget, func(n *nativeMail) error {
				refs := newRecipientReferenceCache()

				for _, i := range pending {
					if n.ctx.Err() != nil {
						return n.ctx.Err()
					}
					m := &messages[i]
					account, mailbox := recipientScope(*m, accountName, mailboxName)
					attempted[i] = true
					e := n.readMessageRecipientsWithCache(account, mailbox, m, refs)
					if e != nil {
						// Permission/transport failures must not be replayed for every row.
						if recipientSystemicError(e) {
							return e
						}
						unavailableRecipients(m, e)
					}
					completed[i] = true
				}
				return nil
			})
		} else {
			for _, i := range pending {
				if ctx.Err() != nil {
					err = ctx.Err()
					break
				}
				m := &messages[i]
				account, mailbox := recipientScope(*m, accountName, mailboxName)
				attempted[i] = true
				e := client.readLegacyMessageRecipients(account, mailbox, m)
				if e != nil {
					if recipientSystemicError(e) {
						err = e
						break
					}
					unavailableRecipients(m, e)
				}
				completed[i] = true
			}
		}
		if err != nil {
			for _, i := range pending {
				if completed[i] {
					continue
				}
				rowErr := err
				if !attempted[i] {
					rowErr = fmt.Errorf("recipient lookup not attempted because shared batch stopped: %w", err)
				}
				// Retain the disk failure while explaining whether Mail tried
				// this row. Partial and observed fields keep their evidence.
				for _, field := range recipientFields(&messages[i]) {
					if field.status.State == "unavailable" && field.status.Error != "" && !strings.Contains(field.status.Error, rowErr.Error()) {
						field.status.Error += "; " + rowErr.Error()
					}
				}
				unavailableRecipients(&messages[i], rowErr)
			}
		}
	}
	unavailable := 0
	for i := range messages {
		// Every requested field must finish in an explicit observed or unknown state.
		unavailableRecipients(&messages[i], fmt.Errorf("recipient headers unavailable"))
		if !messages[i].RecipientStatus.complete() {
			unavailable++
		}
	}
	if unavailable > 0 {
		c.warn(fmt.Sprintf("recipient fields incomplete for %d of %d messages; inspect recipientStatus", unavailable, len(messages)))
	}
	return messages
}
func recipientScope(m Message, account, mailbox string) (string, string) {
	if account == "" {
		account = m.Account
	}
	if mailbox == "" {
		mailbox = m.Mailbox
	}
	return account, mailbox
}

// Resolve collection membership explicitly: Mail can resolve a by-ID specifier
// globally even when it carries a different mailbox container.
func (n *nativeMail) recipientMessageReference(account, mailbox, messageID string) (ae.Value, error) {
	return n.recipientMessageReferenceWithCache(account, mailbox, messageID, nil)
}

type recipientMailboxScope struct{ account, mailbox string }

// References are reused only inside one serialized recipient invocation.
// Membership remains a fresh, scoped observation for every selected message.
type recipientReferenceCache struct {
	accounts  map[string]ae.Value
	mailboxes map[recipientMailboxScope]ae.Value
}

func newRecipientReferenceCache() *recipientReferenceCache {
	return &recipientReferenceCache{accounts: make(map[string]ae.Value), mailboxes: make(map[recipientMailboxScope]ae.Value)}
}

func (n *nativeMail) recipientMessageReferenceWithCache(account, mailbox, messageID string, refs *recipientReferenceCache) (ae.Value, error) {
	var id int64
	if err := n.phase("recipient_scope", func() error {
		if strings.TrimSpace(account) == "" || strings.TrimSpace(mailbox) == "" {
			return fmt.Errorf("recipient read needs account and mailbox")
		}
		var err error
		id, err = strconv.ParseInt(messageID, 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("invalid message ID")
		}
		return nil
	}); err != nil {
		return ae.Value{}, err
	}
	var acc, box ae.Value
	if refs != nil {
		acc = refs.accounts[account]
		box = refs.mailboxes[recipientMailboxScope{account, mailbox}]
	}
	if acc.Kind == "" {
		if err := n.phase("recipient_account", func() error {
			var err error
			acc, err = n.account(account)
			return err
		}); err != nil {
			return ae.Value{}, err
		}
		if refs != nil {
			refs.accounts[account] = acc
		}
	}
	if box.Kind == "" {
		if err := n.phase("recipient_mailbox", func() error {
			var err error
			box, err = n.mailbox(acc, mailbox)
			return err
		}); err != nil {
			return ae.Value{}, err
		}
		if refs != nil {
			refs.mailboxes[recipientMailboxScope{account, mailbox}] = box
		}
	}
	var ref ae.Value
	err := n.phase("recipient_membership", func() error {
		selector := ae.ByTest("mssg", ae.Comparison("=   ", ae.Property("ID  ", ae.Examined()), ae.Int(id)), box)
		value, err := n.get(ae.Property("ID  ", selector))
		if err != nil {
			return err
		}
		ids, err := nativeList(value, "scoped message IDs")
		if err != nil {
			return err
		}
		if len(ids) != 1 || ids[0].Kind != "int" || ids[0].Number != id {
			return notFound("message", messageID)
		}
		ref, err = n.message(box, messageID)
		return err
	})
	return ref, err
}

func (n *nativeMail) readMessageRecipients(account, mailbox string, m *Message) error {
	return n.readMessageRecipientsWithCache(account, mailbox, m, nil)
}

func (n *nativeMail) readMessageRecipientsWithCache(account, mailbox string, m *Message, refs *recipientReferenceCache) error {
	ref, err := n.recipientMessageReferenceWithCache(account, mailbox, m.ID, refs)
	if err != nil {
		return err
	}
	var headers string
	err = n.phase("recipient_headers", func() error {
		var err error
		headers, err = n.stringProperty("alhe", ref)
		return err
	})
	if err == nil && strings.TrimSpace(headers) != "" {
		if len(headers) > maxRecipientHeaderBytes {
			err = fmt.Errorf("Mail message headers exceed %d bytes", maxRecipientHeaderBytes)
		} else {
			var header netmail.Header
			header, err = headerTextMessage(headers)
			if err == nil {
				populateRecipientHeaders(m, header, "mail")
			}
		}
	}
	if err != nil {
		if recipientSystemicError(err) {
			return err
		}
	}
	for i, class := range []string{"trcp", "crcp", "brcp"} {
		field := recipientFields(m)[i]
		if field.status.State == "available" {
			continue
		}
		var values []string
		e := n.phase("recipient_"+strings.ToLower(field.name), func() error {
			var err error
			values, err = n.recipients(ref, class)
			return err
		})
		if e != nil {
			if recipientSystemicError(e) {
				return e
			}
			recipientFieldFailure(field, "mail", e)
		} else {
			populateMailRecipientCollection(field, values)
		}
	}
	if err != nil {
		unavailableRecipients(m, err)
	}
	return nil
}

func recipientSystemicError(err error) bool {
	var lock *AutomationLockTimeoutError
	return nativeAppleError(err, -1743, -1712, -609, -600, -10004) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &lock)
}

func (c *Client) readLegacyMessageRecipients(account, mailbox string, m *Message) error {
	id, err := strconv.ParseInt(m.ID, 10, 64)
	if err != nil || id <= 0 || strings.TrimSpace(account) == "" || strings.TrimSpace(mailbox) == "" {
		return fmt.Errorf("recipient read needs a scoped numeric message ID")
	}
	script := fmt.Sprintf(`
const mail=Application('Mail');
const requestedMailbox='%s';
%s
const result={headers:null,to:null,cc:null,bcc:null,errors:{},failure:null};
try {
 const acc=mail.accounts.byName('%s');acc.name();
 const mbox=%s;
 const matches=mbox.messages.whose({id:{_equals:%d}})();
 if(matches.length!==1 || Number(matches[0].id())!==%d) throw new Error('message not found in requested recipient scope');
 const msg=matches[0];
 function systemic(e) {return isTransportError(e) || e.errorNumber===-1743 || e.errorNumber===-10004;}
 try {result.headers=msg.allHeaders();} catch(e) {if(systemic(e)) throw e;result.errors.headers=String(e);}
 // Return collections alongside headers so Go can recover an individual
 // malformed field without fetching content or launching another process.
 for(const [key,property] of [['to','toRecipients'],['cc','ccRecipients'],['bcc','bccRecipients']]) {
  try {result[key]=msg[property]().map(r=>r.address());} catch(e) {if(systemic(e)) throw e;result.errors[key]=String(e);}
 }
} catch(e) {result.failure={number:e.errorNumber || 0,message:String(e)};}
JSON.stringify(result);`, escapeJSString(mailbox), jxaMailboxLookupHelper(), escapeJSString(account), jxaMailboxLookupExpression(mailbox), id, id)
	var result struct {
		Headers     *string `json:"headers"`
		To, Cc, Bcc *[]string
		Errors      map[string]string `json:"errors"`
		Failure     *struct {
			Number  int    `json:"number"`
			Message string `json:"message"`
		} `json:"failure"`
	}
	if err := c.runJXAJSON(script, recipientReadBudget, "recipients", &result); err != nil {
		return err
	}
	// A later getter may fail after headers or other collections succeeded.
	// Preserve those observations before propagating the systemic error.
	var failureErr error
	if result.Failure != nil {
		if strings.Contains(result.Failure.Message, "message not found") {
			failureErr = notFound("message", m.ID)
		} else {
			failureErr = &ae.Error{Code: result.Failure.Number, Message: result.Failure.Message}
		}
	}
	headerError := result.Errors["headers"]
	if headerError == "" && failureErr != nil {
		headerError = failureErr.Error()
	}
	if result.Headers != nil && strings.TrimSpace(*result.Headers) != "" {
		if len(*result.Headers) > maxRecipientHeaderBytes {
			headerError = fmt.Sprintf("Mail message headers exceed %d bytes", maxRecipientHeaderBytes)
		} else {
			header, err := headerTextMessage(*result.Headers)
			if err != nil {
				headerError = err.Error()
			} else {
				populateRecipientHeaders(m, header, "mail")
			}
		}
	}
	for i, values := range []*[]string{result.To, result.Cc, result.Bcc} {
		field := recipientFields(m)[i]
		if field.status.State == "available" {
			continue
		}
		if values != nil {
			populateMailRecipientCollection(field, *values)
		} else {
			detail := result.Errors[[]string{"to", "cc", "bcc"}[i]]
			if detail == "" {
				detail = field.status.Error
			}
			if detail == "" {
				detail = headerError
			}
			if detail == "" {
				detail = "Mail did not expose this recipient field"
			}
			recipientFieldFailure(field, "mail", fmt.Errorf("%s", detail))
		}
	}
	for _, field := range recipientFields(m)[3:] {
		if field.status.State != "available" {
			detail := field.status.Error
			if detail == "" {
				detail = headerError
			}
			if detail == "" {
				detail = "Mail did not expose delivery headers"
			}
			recipientFieldFailure(field, "mail", fmt.Errorf("%s", detail))
		}
	}
	return failureErr
}

// Merge newly inspected fields without losing successful evidence from another
// label or earlier checkpoint page when the later attempt was unavailable.
func mergeRecipientEvidence(target *Message, observed Message) {
	fields := recipientFields(target)
	rank := func(state string) int {
		if state == "available" {
			return 2
		}
		if state == "partial" {
			return 1
		}
		return 0
	}
	for i, field := range recipientFields(&observed) {
		if rank(field.status.State) < rank(fields[i].status.State) {
			continue
		}
		if field.status.State == "partial" && fields[i].status.State == "partial" {
			appendRecipientFieldEvidence(fields[i], *field.values, field.status.Source)
			continue
		}
		*fields[i].values = *field.values
		*fields[i].status = *field.status
	}
}

// GetMessageMetadataJSON reads the full envelope without requesting body or
// recipient collections. Unlike mutation-state reads it retains display fields.
func (c *Client) GetMessageMetadataJSON(account, mailbox, id string) (*Message, error) {
	number, err := strconv.ParseInt(id, 10, 64)
	if err != nil || number <= 0 {
		return nil, fmt.Errorf("invalid message ID")
	}
	if c.nativeEnabled() {
		var message *Message
		err := c.withNativeAutomation(recipientReadBudget, func(n *nativeMail) error {
			ref, err := n.recipientMessageReference(account, mailbox, id)
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			message, err = n.messageMetadata(ref, account, mailbox)
			return err
		})
		return message, err
	}
	script := fmt.Sprintf(`
const mail=Application('Mail');
const requestedMailbox='%s';
%s
const acc=mail.accounts.byName('%s');acc.name();
const mbox=%s;
const matches=mbox.messages.whose({id:{_equals:%d}})();
if(matches.length>1) throw new Error('message ID is ambiguous');
let result=null;
if(matches.length===1) {
 const msg=matches[0];
 if(Number(msg.id())!==%d) throw new Error('message ID changed');
 result={%s};
}
JSON.stringify(result);`, escapeJSString(mailbox), jxaMailboxLookupHelper(), escapeJSString(account), jxaMailboxLookupExpression(mailbox), number, number, jxaMessageProjection{ID: "String(msg.id())", Size: "msg.messageSize()", Mailbox: "mbox.name()", Account: "acc.name()"}.fields())
	return c.runJXAMessage(script, recipientReadBudget, "message metadata")
}
