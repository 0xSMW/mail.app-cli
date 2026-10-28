package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// DraftSendResult records Mail's submission acknowledgement separately from
// removal of the original. A submitted result must never be retried as a send.
type DraftSendResult struct {
	DraftID        string `json:"draftId"`
	Account        string `json:"account"`
	AccountID      string `json:"accountId,omitempty"`
	Sender         string `json:"sender,omitempty"`
	Status         string `json:"status"`
	Submitted      bool   `json:"submitted"`
	Sent           bool   `json:"sent"`
	CleanupPending bool   `json:"cleanupPending"`
	Uncertain      bool   `json:"uncertain"`
}

type draftSnapshot struct {
	Message *Message `json:"message"`
	Source  string   `json:"source"`
}
type draftMIMEAttachment struct {
	name string
	data []byte
}

// PreviewDraftSend reads the exact stored draft and validates the same sender,
// recipients and MIME constraints as sending, without exporting attachments or
// creating an outgoing message.
func (c *Client) PreviewDraftSend(accountName, draftID string) (*OutgoingResult, error) {
	snapshot, err := c.readDraftSnapshot(accountName, draftID)
	if err != nil {
		return nil, err
	}
	sender, err := draftSenderAddress(snapshot.Message.Sender)
	if err != nil {
		return nil, err
	}
	body, _, err := reconstructibleDraft(snapshot)
	if err != nil {
		return nil, err
	}
	draft := snapshot.Message
	return c.PreviewOutgoing(draftOutgoing(draft, sender, body))
}

// SendDraftResult supports only drafts whose raw MIME proves that rebuilding
// through Mail's outgoing-message API preserves their content. Mail's stored
// message class does not implement send, and opening a message does not expose
// a documented correspondence to a new outgoing object. Rich or special-header
// drafts therefore fail before creating any outgoing message. Ordinary file
// attachments preserve filenames and exported bytes; Mail chooses their MIME
// metadata from those files when it composes the replacement.
func (c *Client) SendDraftResult(accountName, draftID string) (*DraftSendResult, error) {
	if err := c.checkSendingAllowed(); err != nil {
		return nil, err
	}
	result := &DraftSendResult{DraftID: draftID, Account: accountName, Status: "not_submitted"}
	fail := func(phase string, err error) (*DraftSendResult, error) {
		attempted := result.Submitted
		var mutation *MutationError
		if errors.As(err, &mutation) {
			attempted = attempted || mutation.Attempted
		}
		return result, &MutationError{Operation: "draft_send", Phase: phase, Account: result.Account, Sender: result.Sender, Submitted: result.Submitted, Attempted: attempted, Uncertain: result.Uncertain, Cleanup: func() string {
			if result.CleanupPending {
				return "pending"
			}
			return ""
		}(), Cause: err}
	}
	snapshot, err := c.readDraftSnapshot(accountName, draftID)
	if err != nil {
		return fail("read_draft", err)
	}
	draft := snapshot.Message
	result.Account, result.Sender = draft.Account, draft.Sender
	sender, err := draftSenderAddress(draft.Sender)
	if err != nil {
		return fail("preserve_sender", err)
	}
	result.Sender = sender
	body, mimeAttachments, err := reconstructibleDraft(snapshot)
	if err != nil {
		return fail("preserve_content", err)
	}
	dir, err := os.MkdirTemp("", "mail-app-cli-send-draft-")
	if err != nil {
		return fail("preserve_attachments", err)
	}
	defer os.RemoveAll(dir)
	attachments, err := c.exportDraftAttachments(draft, dir)
	if err != nil {
		return fail("preserve_attachments", err)
	}
	if err := compareDraftMIMEAttachments(attachments, mimeAttachments); err != nil {
		return fail("preserve_attachments", err)
	}
	current, err := c.readDraftSnapshot(draft.Account, draft.ID)
	if err != nil {
		return fail("verify_original", err)
	}
	if !sameDraftSnapshot(snapshot, current) {
		return fail("verify_original", fmt.Errorf("draft changed while preparing submission"))
	}
	input := draftOutgoing(draft, sender, body)
	input.Attachments = attachments
	outgoing, err := c.composeOutgoing(input, "send")
	if outgoing != nil {
		result.AccountID, result.Sender, result.Submitted = outgoing.AccountID, outgoing.Sender, outgoing.Submitted
	}
	if err != nil {
		var mutation *MutationError
		if errors.As(err, &mutation) {
			result.Submitted = result.Submitted || mutation.Submitted
			result.Uncertain = mutation.Uncertain
		}
		result.Sent = result.Submitted
		result.CleanupPending = result.Submitted
		if result.Uncertain {
			result.Status = "submission_uncertain"
		} else if result.Submitted {
			result.Status = "submitted_cleanup_pending"
		}
		return fail("submit", err)
	}
	if !result.Submitted {
		return fail("submit", fmt.Errorf("Mail did not acknowledge submission"))
	}
	result.Sent = result.Submitted
	result.CleanupPending, result.Status = true, "submitted_cleanup_pending"
	if err := c.deleteUnchangedDraft(snapshot); err != nil {
		return fail("cleanup_original", err)
	}
	result.CleanupPending, result.Status = false, "submitted"
	return result, nil
}

// This read bypasses Envelope Index latency and requires one unambiguous exact
// local ID in the account-owned special Drafts mailbox. Every property read is strict.
func (c *Client) readDraftSnapshot(accountName, draftID string) (*draftSnapshot, error) {
	if c.nativeEnabled() {
		return c.readNativeDraftSnapshot(accountName, draftID)
	}
	script := jxaDraftSnapshotScript(accountName, draftID)
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var snapshot *draftSnapshot
	if err := json.Unmarshal([]byte(output), &snapshot); err != nil {
		return nil, fmt.Errorf("read draft source: %w", err)
	}
	if snapshot == nil || snapshot.Message == nil {
		return nil, notFound("draft", draftID)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return nil, err
	}
	var messageFields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["message"], &messageFields); err != nil {
		return nil, err
	}
	for _, key := range []string{"id", "account", "mailbox", "subject", "sender", "content", "toRecipients", "ccRecipients", "bccRecipients"} {
		value, ok := messageFields[key]
		if !ok || string(value) == "null" {
			return nil, fmt.Errorf("draft property %s unavailable", key)
		}
	}
	if value, ok := envelope["source"]; !ok || string(value) == "null" {
		return nil, fmt.Errorf("draft raw source unavailable")
	}
	if snapshot.Message.ID != draftID || snapshot.Message.Account == "" || snapshot.Message.Mailbox == "" || (accountName != "" && snapshot.Message.Account != accountName) {
		return nil, fmt.Errorf("draft identity readback mismatch")
	}
	return snapshot, nil
}

func jxaDraftSnapshotScript(accountName, draftID string) string {
	return fmt.Sprintf(`
const mail = Application('Mail');
const draftSnapshotRead = true;
%s
const requestedAccount = '%s';
const requestedId = '%s';
const matches = [];
for (const acc of mail.accounts()) {
 if (requestedAccount && acc.name() !== requestedAccount) continue;
 const box = draftMailboxForAccount(mail,acc,true);
 if(box === null) continue;
 {
  const msg = messageById(box,requestedId);
  if (msg === null) continue;
  matches.push({message:{id:String(msg.id()),account:acc.name(),mailbox:msg.mailbox().name(),subject:msg.subject(),sender:msg.sender(),content:msg.content(),toRecipients:msg.toRecipients().map(r=>r.address()),ccRecipients:msg.ccRecipients().map(r=>r.address()),bccRecipients:msg.bccRecipients().map(r=>r.address())},source:msg.source()});
 }
}
if (matches.length > 1) throw new Error('Draft ID is ambiguous; specify an account');
JSON.stringify(matches.length === 1 ? matches[0] : null);
`, jxaDraftMailboxHelper()+jxaDraftMessageByIDHelper(), escapeJSString(accountName), escapeJSString(draftID))
}

func sameDraftSnapshot(a, b *draftSnapshot) bool {
	return sha256.Sum256([]byte(a.Source)) == sha256.Sum256([]byte(b.Source)) && reflect.DeepEqual(a.Message, b.Message)
}

// Compare source and compose properties again inside the deletion bridge, so
// another edit between submission and cleanup cannot remove the edited draft.
func (c *Client) deleteUnchangedDraft(snapshot *draftSnapshot) error {
	if c.nativeEnabled() {
		return c.deleteNativeUnchangedDraft(snapshot)
	}
	script := jxaDraftCleanupScript(snapshot)
	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	var receipt struct {
		Deleted bool `json:"deleted"`
	}
	if err := json.Unmarshal([]byte(output), &receipt); err != nil {
		return fmt.Errorf("original draft cleanup acknowledgement: %w", err)
	}
	if !receipt.Deleted {
		return fmt.Errorf("original draft cleanup was not acknowledged")
	}
	return nil
}

func jxaDraftCleanupScript(snapshot *draftSnapshot) string {
	expected, _ := json.Marshal(snapshot)
	return fmt.Sprintf(`
const mail = Application('Mail');
const draftCleanup = true;
const expected = %s;
%s
%s
const acc = mail.accounts.byName(expected.message.account);
const accountID = acc.id();
if (typeof accountID !== 'string' || accountID.length === 0) throw new Error('Draft cleanup account identity unavailable');
function currentDraftBox() {
 const current = mail.accounts.byName(expected.message.account);
 if (current.id() !== accountID) throw new Error('Draft cleanup account identity changed');
 return draftMailboxForAccount(mail,current,false);
}
function cleanupOriginalDraft() {
 const msg = messageById(currentDraftBox(),expected.message.id);
 if (msg === null) { currentDraftBox(); return true; }
 const current = {subject:msg.subject(),sender:msg.sender(),content:msg.content(),toRecipients:msg.toRecipients().map(r=>r.address()),ccRecipients:msg.ccRecipients().map(r=>r.address()),bccRecipients:msg.bccRecipients().map(r=>r.address())};
 if (msg.source() !== expected.source || current.subject !== expected.message.subject || current.sender !== expected.message.sender || current.content !== expected.message.content || JSON.stringify(current.toRecipients) !== JSON.stringify(expected.message.toRecipients) || JSON.stringify(current.ccRecipients) !== JSON.stringify(expected.message.ccRecipients) || JSON.stringify(current.bccRecipients) !== JSON.stringify(expected.message.bccRecipients)) throw new Error('Original draft changed after preparation; retained');
 currentDraftBox();
 msg.delete();
 for (let i=0;i<30;i++) {
  try {
   if (messageById(currentDraftBox(),expected.message.id) === null) {currentDraftBox(); return true;}
  } catch(e) {
   if (e.errorNumber !== -1728) throw e;
   // A failed nested read is not absence. Fresh strict IDs in the same owned
   // account may independently establish that the exact deleted ID is gone.
   const ids=draftIDs(currentDraftBox());
   currentDraftBox();
   if (!ids.includes(expected.message.id)) return true;
  }
  delay(0.1);
 }
 throw new Error('Original draft removal could not be verified after preparation');
}
JSON.stringify({deleted:cleanupOriginalDraft()});
`, string(expected), jxaDraftMailboxHelper(), jxaDraftMessageByIDHelper())
}

// Enumerate exact IDs without swallowing bridge failures. An inaccessible
// mailbox must never be interpreted as absence during destructive cleanup.
func jxaDraftMessageByIDHelper() string {
	return `
function draftIDs(mbox) {
 const owner=mbox.account().id();
 if(typeof owner !== 'string' || owner.length === 0)throw new Error('Draft mailbox account identity unavailable');
 const ids=mbox.messages.id();
 if(!Array.isArray(ids))throw new Error('Draft mailbox IDs unavailable');
 const seen={};
 return ids.map(value=>{
  let id;
  if(typeof value==='number' && Number.isSafeInteger(value) && value>0)id=String(value);
  else if(typeof value==='string' && /^[1-9][0-9]*$/.test(value) && Number.isSafeInteger(Number(value)))id=value;
  else throw new Error('Draft mailbox contains an invalid ID');
  if(seen[id])throw new Error('Draft mailbox contains duplicate IDs');
  seen[id]=true;return id;
 });
}
function messageById(mbox, messageId) {
 if(typeof messageId !== 'string' || !/^[1-9][0-9]*$/.test(messageId) || !Number.isSafeInteger(Number(messageId)))throw new Error('Draft ID is invalid');
 const ids=draftIDs(mbox);
 if(!ids.includes(messageId))return null;
 const msg=mbox.messages.byId(Number(messageId));
 if(String(msg.id()) !== messageId)throw new Error('Draft ID changed during lookup');
 const deleted=msg.deletedStatus();
 if(typeof deleted !== 'boolean')throw new Error('Draft deleted status is not a Boolean');
 if(deleted===true)return null;
 return msg;
}

`
}

func reconstructibleDraft(snapshot *draftSnapshot) (string, []draftMIMEAttachment, error) {
	if snapshot.Source == "" || len(snapshot.Source) > 16<<20 {
		return "", nil, fmt.Errorf("draft raw source unavailable or too large for safe reconstruction")
	}
	parsed, err := mail.ReadMessage(strings.NewReader(snapshot.Source))
	if err != nil {
		return "", nil, err
	}
	allowed := map[string]bool{"From": true, "To": true, "Cc": true, "Bcc": true, "Subject": true, "Date": true, "Message-Id": true, "Mime-Version": true, "Content-Type": true, "Content-Transfer-Encoding": true, "X-Uniform-Type-Identifier": true, "X-Universally-Unique-Identifier": true}
	for key, values := range parsed.Header {
		if len(values) != 1 || (!allowed[key] && !reconstructibleAppleDraftHeader(key, values[0])) {
			return "", nil, fmt.Errorf("draft header %s cannot be preserved safely; send this draft in Mail", key)
		}
	}
	decoder := new(mime.WordDecoder)
	subject, err := decoder.DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != snapshot.Message.Subject {
		return "", nil, fmt.Errorf("draft subject does not match its raw source")
	}
	from, err := draftSenderAddress(parsed.Header.Get("From"))
	if err != nil {
		return "", nil, fmt.Errorf("draft source sender unavailable: %w", err)
	}
	sender, err := draftSenderAddress(snapshot.Message.Sender)
	if err != nil || !strings.EqualFold(from, sender) {
		return "", nil, fmt.Errorf("draft sender does not match its raw source")
	}
	for _, item := range []struct {
		key       string
		addresses []string
	}{{"To", snapshot.Message.ToRecipients}, {"Cc", snapshot.Message.CcRecipients}, {"Bcc", snapshot.Message.BccRecipients}} {
		// Mail can omit Bcc from raw source, while exposing it through the draft.
		if item.key == "Bcc" && parsed.Header.Get(item.key) == "" {
			continue
		}
		var raw []string
		if value := parsed.Header.Get(item.key); value != "" {
			list, err := mail.ParseAddressList(value)
			if err != nil {
				return "", nil, err
			}
			for _, address := range list {
				raw = append(raw, address.Address)
			}
		}
		if len(raw) != len(item.addresses) {
			return "", nil, fmt.Errorf("draft %s recipients do not match raw source", item.key)
		}
		for i, address := range raw {
			actual, err := mail.ParseAddress(item.addresses[i])
			if err != nil || !strings.EqualFold(address, actual.Address) {
				return "", nil, fmt.Errorf("draft %s recipient differs from raw source", item.key)
			}
		}
	}
	contentType := parsed.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain; charset=us-ascii"
	}
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", nil, err
	}
	if kind == "text/plain" {
		body, err := plainDraftBody(textproto.MIMEHeader(parsed.Header), parsed.Body, params)
		if err == nil {
			err = validateDraftBody(snapshot, body)
		}
		return body, nil, err
	}
	if kind == "multipart/alternative" {
		return applePlainAlternativeBody(snapshot, parsed.Body, params)
	}
	if kind != "multipart/mixed" || params["boundary"] == "" || len(params) != 1 {
		return "", nil, fmt.Errorf("draft MIME %s cannot be reconstructed without losing formatting; send this draft in Mail", kind)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	body := ""
	haveBody := false
	bodyValidated := false
	var attachments []draftMIMEAttachment
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, err
		}
		partKind, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			return "", nil, err
		}
		disposition, dispositionParams, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if part.Header.Get("Content-Disposition") == "" {
			disposition = ""
			err = nil
		}
		if err != nil {
			return "", nil, err
		}
		for key, values := range part.Header {
			if len(values) != 1 || (key != "Content-Type" && key != "Content-Disposition" && key != "Content-Transfer-Encoding") {
				return "", nil, fmt.Errorf("unsupported draft MIME part header %s", key)
			}
		}
		if !haveBody && partKind == "text/plain" && disposition == "" {
			body, err = plainDraftBody(part.Header, part, partParams)
			if err != nil {
				return "", nil, err
			}
			haveBody = true
			continue
		}
		if !haveBody && partKind == "multipart/alternative" && disposition == "" {
			var related []draftMIMEAttachment
			body, related, err = applePlainAlternativeBody(snapshot, part, partParams)
			if err != nil {
				return "", nil, err
			}
			haveBody = true
			// The alternative parser already proved the entire stored/body
			// relationship, which may canonicalize Mail's wrapper layout.
			bodyValidated = true
			attachments = append(attachments, related...)
			continue
		}
		if disposition != "attachment" || strings.HasPrefix(partKind, "multipart/") || partKind == "message/rfc822" || partKind == "message/external-body" || partKind == "application/pkcs7-mime" || partKind == "application/pgp-encrypted" || len(dispositionParams) != 1 {
			return "", nil, fmt.Errorf("inline, nested, external, signed, encrypted, or additional body content cannot be reconstructed safely")
		}
		name := dispositionParams["filename"]
		if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
			return "", nil, fmt.Errorf("unsupported attachment filename")
		}
		data, err := decodeDraftPart(part.Header, part)
		if err != nil {
			return "", nil, err
		}
		attachments = append(attachments, draftMIMEAttachment{name, data})
	}
	if !haveBody {
		return "", nil, fmt.Errorf("plain-text draft body unavailable")
	}
	if !bodyValidated {
		if err := validateDraftBody(snapshot, body); err != nil {
			return "", nil, err
		}
	}
	return body, attachments, nil
}

// These specific Mail compose-state values do not authorize any additional
// content: the MIME, body and attachment checks must still prove preservation.
// In particular, a Mail signature selection is not an S/MIME signature, and
// the remote-attachments setting does not prove that attachment bytes exist.
// Unknown Apple headers and unfamiliar values remain fail-closed.
func reconstructibleAppleDraftHeader(key, value string) bool {
	switch key {
	case "X-Apple-Mail-Signature":
		// Mail stores the selected signature's UUID, while the actual signature
		// content must already be represented by the verified message body.
		return value == "" || isAppleDraftUUID(value)
	case "X-Apple-Base-Url":
		if !strings.HasPrefix(value, "x-msg://") || !strings.HasSuffix(value, "/") {
			return false
		}
		id := strings.TrimSuffix(strings.TrimPrefix(value, "x-msg://"), "/")
		return id != "" && strings.Trim(id, "0123456789") == ""
	case "X-Apple-Windows-Friendly":
		return value == "0" || value == "1"
	case "X-Apple-Mail-Remote-Attachments":
		return value == "YES" || value == "NO"
	default:
		return false
	}
}

func plainDraftBody(header textproto.MIMEHeader, reader io.Reader, params map[string]string) (string, error) {
	for key, value := range params {
		if key != "charset" || (strings.ToLower(value) != "utf-8" && strings.ToLower(value) != "us-ascii") {
			return "", fmt.Errorf("unsupported plain-text draft parameter %s=%s", key, value)
		}
	}
	data, err := decodeDraftPart(header, reader)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("draft body is not UTF-8")
	}
	if strings.EqualFold(params["charset"], "us-ascii") {
		for _, b := range data {
			if b > 127 {
				return "", fmt.Errorf("invalid ASCII draft body")
			}
		}
	}
	return string(data), nil
}
func decodeDraftPart(header textproto.MIMEHeader, reader io.Reader) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
	case "", "7bit", "8bit", "binary":
	case "base64":
		reader = base64.NewDecoder(base64.StdEncoding, reader)
	case "quoted-printable":
		reader = quotedprintable.NewReader(reader)
	default:
		return nil, fmt.Errorf("unsupported draft transfer encoding")
	}
	data, err := io.ReadAll(io.LimitReader(reader, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("draft MIME part too large")
	}
	return data, nil
}
func compareDraftMIMEAttachments(paths []string, expected []draftMIMEAttachment) error {
	if len(paths) != len(expected) {
		return fmt.Errorf("draft attachment count differs from raw source")
	}
	matched := make([]bool, len(expected))
	for _, path := range paths {
		name := norm.NFC.String(filepath.Base(path))
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found := false
		for i, attachment := range expected {
			if !matched[i] && norm.NFC.String(attachment.name) == name && bytes.Equal(data, attachment.data) {
				matched[i] = true
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("draft attachment %q differs from raw source", filepath.Base(path))
		}
	}
	return nil
}

func draftSenderAddress(value string) (string, error) {
	if !strings.ContainsAny(value, "\r\n") {
		if parsed, err := mail.ParseAddress(value); err == nil && parsed.Address != "" {
			return parsed.Address, nil
		}
		// Exchange can expose an address as its own unquoted display name.
		// Accept only that duplicate-mailbox shape, without broadening the
		// parser to arbitrary malformed display names or multiple senders.
		trimmed := strings.TrimSpace(value)
		if strings.Count(trimmed, "<") == 1 && strings.Count(trimmed, ">") == 1 && strings.HasSuffix(trimmed, ">") {
			open := strings.IndexByte(trimmed, '<')
			display := strings.TrimSpace(trimmed[:open])
			mailbox := strings.TrimSpace(trimmed[open+1 : len(trimmed)-1])
			left, leftErr := mail.ParseAddress(display)
			right, rightErr := mail.ParseAddress(mailbox)
			if leftErr == nil && rightErr == nil && left.Name == "" && right.Name == "" && strings.EqualFold(display, left.Address) && strings.EqualFold(mailbox, right.Address) && strings.EqualFold(left.Address, right.Address) {
				return right.Address, nil
			}
		}
	}
	return "", fmt.Errorf("draft sender cannot be preserved: %q", value)
}

func validateDraftBody(snapshot *draftSnapshot, body string) error {
	normalize := func(v string) string { return strings.ReplaceAll(v, "\r\n", "\n") }
	if strings.ContainsRune(body, 0) || normalize(body) != normalize(snapshot.Message.Content) {
		return fmt.Errorf("draft body differs from its plain-text source")
	}
	return nil
}
