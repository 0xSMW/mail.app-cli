package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// OutgoingInput composes a new message. Sender optionally selects an exact
// configured address belonging exclusively to Account.
type OutgoingInput struct {
	Account     string   `json:"account"`
	Sender      string   `json:"sender,omitempty"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	To          []string `json:"to"`
	Cc          []string `json:"cc,omitempty"`
	Bcc         []string `json:"bcc,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

// OutgoingResult records Mail's acceptance, never server delivery or routing.
type OutgoingResult struct {
	Account   string   `json:"account"`
	AccountID string   `json:"accountId"`
	Sender    string   `json:"sender"`
	Status    string   `json:"status"`
	Draft     *Message `json:"draft,omitempty"`
	Submitted bool     `json:"submitted"`
}

type outgoingEnvelope struct {
	OK        bool           `json:"ok"`
	Result    OutgoingResult `json:"result"`
	Phase     string         `json:"phase"`
	Error     string         `json:"error"`
	Cleanup   string         `json:"cleanup"`
	Attempted bool           `json:"attempted"`
	Uncertain bool           `json:"uncertain"`
	Saved     bool           `json:"saved"`
}

func (c *Client) PreviewOutgoing(input OutgoingInput) (*OutgoingResult, error) {
	return c.composeOutgoing(input, "preview")
}

func (c *Client) SendOutgoing(input OutgoingInput) (*OutgoingResult, error) {
	return c.composeOutgoing(input, "send")
}

func (c *Client) composeOutgoing(input OutgoingInput, action string) (*OutgoingResult, error) {
	if action != "send" && action != "save" && action != "preview" {
		return nil, fmt.Errorf("invalid outgoing action %q", action)
	}
	if strings.TrimSpace(input.Account) == "" {
		return nil, fmt.Errorf("account is required")
	}
	if len(input.To)+len(input.Cc)+len(input.Bcc) == 0 {
		return nil, fmt.Errorf("at least one recipient is required")
	}
	for _, list := range [][]string{input.To, input.Cc, input.Bcc} {
		for _, address := range list {
			if strings.TrimSpace(address) == "" {
				return nil, fmt.Errorf("recipient must not be empty")
			}
		}
	}
	attachments, err := ValidateDraftAttachments(input.Attachments)
	if err != nil {
		return nil, err
	}
	input.Attachments = attachments
	if c.nativeEnabled() {
		return c.composeNativeOutgoing(input, action)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	script := jxaPhaseHelper() + jxaOutgoingHelper() + "\nJSON.stringify(composeOutgoing(" + string(payload) + ", " + fmt.Sprintf("%q", action) + "));"
	output, err := c.runJXA(script)
	if err != nil {
		attempted := action != "preview"
		var lockErr *AutomationLockTimeoutError
		if errors.As(err, &lockErr) {
			attempted = false
		}
		var timeoutErr *AutomationTimeoutError
		if errors.As(err, &timeoutErr) && timeoutErr.Trace != nil {
			attempted = timeoutErr.Trace.Pending == "outgoing_"+action || timeoutErr.Trace.LastCompleted == "outgoing_"+action
		}
		cleanup := ""
		if action != "preview" && !attempted && timeoutErr != nil && timeoutErr.Trace != nil && timeoutErr.Trace.Pending == "outgoing_compose" {
			cleanup = "cleanup could not be confirmed after interrupted composition"
		}
		return nil, &MutationError{Operation: action, Phase: "automation", Account: input.Account, Sender: input.Sender, Cause: err, Attempted: attempted, Uncertain: attempted, Cleanup: cleanup}
	}
	var envelope outgoingEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return nil, &MutationError{Operation: action, Phase: "response", Account: input.Account, Sender: input.Sender, Cause: fmt.Errorf("invalid outgoing response: %w", err), Attempted: action != "preview", Uncertain: action != "preview"}
	}
	result := &envelope.Result
	if !envelope.OK {
		if envelope.Error == "" {
			envelope.Error = "Mail did not return a successful outgoing receipt"
		}
		return result, &MutationError{Operation: action, Phase: envelope.Phase, Account: input.Account, Sender: result.Sender, Cause: errors.New(envelope.Error), Cleanup: envelope.Cleanup, Attempted: envelope.Attempted, Uncertain: envelope.Uncertain, Submitted: result.Submitted, Saved: envelope.Saved}
	}
	expected := map[string]string{"send": "submitted", "save": "saved", "preview": "preview"}[action]
	if result.Account != input.Account || result.AccountID == "" || result.Sender == "" || result.Status != expected || (action == "send" && !result.Submitted) || (action != "send" && result.Submitted) || (action == "save" && (result.Draft == nil || result.Draft.ID == "" || result.Draft.Account != input.Account)) {
		return result, &MutationError{Operation: action, Phase: "response", Account: input.Account, Sender: result.Sender, Cause: errors.New("Mail returned an incomplete outgoing receipt"), Attempted: action != "preview", Uncertain: action != "preview", Submitted: result.Submitted, Saved: envelope.Saved}
	}
	if action == "save" && len(attachments) > 0 {
		if err := c.verifyDraftAttachments(result.Draft, attachments); err != nil {
			return result, &MutationError{Operation: action, Phase: "attachment_verification", Account: input.Account, Sender: result.Sender, Cause: err, Attempted: true, Uncertain: true, Saved: true}
		}
	}
	return result, nil
}

// The same helper is executed by osascript and the Node mock tests. No account
// settings are changed. Sender readback is an identity guard, not proof of SMTP routing.
func jxaOutgoingHelper() string {
	return jxaDraftMailboxHelper() + `
function composeOutgoing(input, action) {
 input.to = input.to || []; input.cc = input.cc || []; input.bcc = input.bcc || [];
 const mail = Application('Mail');
 let outgoing = null, attempted = false, accepted = false, saved = false;
 let step = 'account_resolution';
 const composeBody=action === 'save' && !input.body.endsWith('\n') ? input.body+'\n' : input.body;
 let result = {account: input.account, accountId: '', sender: '', status: '', submitted: false};
 function fail(message) { throw new Error(message); }
 function address(value) {
  const text = String(value || '').trim();
  const plain = /^[A-Za-z0-9.!#$%&'*+\/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$/;
  if (plain.test(text)) return text;
  const display = /^("(?:[^"\\]|\\.)*"|[^<>,;\r\n]+)\s*<([^<>]+)>$/.exec(text);
  if (display && plain.test(display[2])) return display[2];
  fail('Sender readback is not exactly one valid address');
 }
 function resolve() {
  const accounts = mail.accounts();
  const matching = accounts.filter(a => String(a.name()) === input.account);
  if (matching.length !== 1) fail('Account name must resolve uniquely');
  const account = matching[0], id = String(account.id() || '');
  if (!id || accounts.filter(a => String(a.id()) === id).length !== 1) fail('Account ID must resolve uniquely');
  if (account.enabled() !== true) fail('Selected account is disabled');
  const addresses = account.emailAddresses();
  if (!Array.isArray(addresses) || !addresses.length) fail('Selected account has no configured sender addresses');
  const priorStep = step;
  if (input.sender) step='sender_selection';
  const requested = address(input.sender || addresses[0]);
  const selected = addresses.filter(v => address(v).toLowerCase() === requested.toLowerCase());
  if (selected.length !== 1) fail('Sender must identify exactly one configured account address');
  const chosen = address(selected[0]);
  step=priorStep;
  const owners = accounts.filter(a => {
   const list = a.emailAddresses();
   if (!Array.isArray(list)) fail('Account sender ownership cannot be read');
   return list.some(v => address(v).toLowerCase() === chosen.toLowerCase());
  });
  if (owners.length !== 1 || String(owners[0].id()) !== id) fail('Sender belongs to multiple accounts');
  return {account: account, id: id, sender: chosen};
 }
 function guardSender() {
  const current = resolve();
  if (current.id !== result.accountId || current.sender !== result.sender) fail('Account identity changed during composition');
  if (address(outgoing.sender()).toLowerCase() !== result.sender.toLowerCase()) fail('Mail sender readback differs from selected sender');
 }
 function attachList(message) {
  return message.mailAttachments().map(a => String(a.name()).normalize('NFC')).sort();
 }
 function prepareAttachments() {
  const paths=input.attachments || [];
  // JXA cannot address the rich-text attachment element on current Mail.
  // With no files, avoid that unsupported access entirely.
  if(paths.length === 0) return;
  ObjC.import('Foundation');
  const id=String(outgoing.id());
  if(!/^[0-9]+$/.test(id)) fail('Outgoing message ID unavailable for attachment insertion');
  function quote(s) { return '"'+String(s).replace(/\\/g,'\\\\').replace(/"/g,'\\"')+'"'; }
  const lines=['tell application "Mail"','tell outgoing message id '+id,'tell content'];
  paths.forEach(path=>lines.push('make new attachment with properties {file name:(POSIX file '+quote(path)+')} at after last paragraph'));
  lines.push('repeat 50 times','if (count attachments) = '+paths.length+' then exit repeat','delay 0.1','end repeat','count attachments','end tell','end tell','end tell');
  const error=Ref();
  const value=$.NSAppleScript.alloc.initWithSource(lines.join('\n')).executeAndReturnError(error);
  if(!value) fail('Attachment insertion failed: '+JSON.stringify(ObjC.deepUnwrap(error[0])));
  if(Number(value.int32Value) !== paths.length) fail('Attachments did not finish loading');
 }
 const expectedNames = (input.attachments || []).map(p => p.split('/').pop().normalize('NFC')).sort();
 function equal(a,b) { return JSON.stringify(a) === JSON.stringify(b); }
 function recipients(message, kind) { return message[kind]().map(r => String(r.address())); }
 function matchesBody(value) {
  const body=String(value).replace(/\r\n/g,'\n');
  const expected=composeBody.replace(/\r\n/g,'\n');
  // Mail's persisted rich-text view adds one leading paragraph separator.
  // Do not trim user whitespace or accept arbitrary body differences.
  return body===input.body.replace(/\r\n/g,'\n') || body===expected || body==='\n'+expected;
 }
 function matches(message) {
  return String(message.subject()) === input.subject && matchesBody(message.content()) &&
   address(message.sender()).toLowerCase() === result.sender.toLowerCase() && equal(recipients(message,'toRecipients'),input.to) &&
   equal(recipients(message,'ccRecipients'),input.cc || []) && equal(recipients(message,'bccRecipients'),input.bcc || []) && equal(attachList(message),expectedNames);
 }
 try {
  const target = phase('outgoing_resolve',resolve);
  result.accountId = target.id; result.sender = target.sender;
  if (action === 'preview') { result.status='preview'; return {ok:true,result:result}; }
  let drafts = null, before = {};
  if (action === 'save') {
   drafts = draftMailboxForAccount(mail,target.account);
   drafts.messages().forEach(m => { before[String(m.id())] = true; });
  }
  phase('outgoing_compose',function() {
  step='compose';
  outgoing = mail.OutgoingMessage({subject:input.subject,content:composeBody,sender:result.sender,visible:false});
  mail.outgoingMessages.push(outgoing);
  outgoing.sender = result.sender;
  guardSender();
  [['to',input.to],['cc',input.cc || []],['bcc',input.bcc || []]].forEach(pair => {
   pair[1].forEach(value => outgoing[pair[0]+'Recipients'].push(mail[pair[0][0].toUpperCase()+pair[0].slice(1)+'Recipient']({address:value})));
  });
  step='recipient_verification';
  if(!equal(recipients(outgoing,'toRecipients'),input.to) || !equal(recipients(outgoing,'ccRecipients'),input.cc) || !equal(recipients(outgoing,'bccRecipients'),input.bcc)) fail('Outgoing recipients differ from requested recipients');
  step='attachments';
  prepareAttachments();
  step='sender_verification';
  guardSender();
  });
  step=action;
  if (action === 'send') {
   const response = phase('outgoing_send',function() { attempted=true; return mail.send(outgoing); });
   if (response !== true) fail('Mail did not accept send (expected true)');
   accepted=true; result.submitted=true; result.status='submitted';
   return {ok:true,result:result,attempted:true};
  }
  phase('outgoing_save',function() { attempted=true; mail.save(outgoing); mail.close(outgoing,{saving:'yes'}); saved=true; });
  step='save_verification';
  for (let i=0;i<80;i++) {
   drafts=draftMailboxForAccount(mail,target.account);
   let candidates=[];
   try { candidates=drafts.messages().filter(m => !before[String(m.id())] && matches(m)); }
   catch(e) { if(e.errorNumber !== -1728) throw e; delay(0.1); continue; }
   if (candidates.length > 1) fail('Saved draft identity is ambiguous');
   if (candidates.length === 1) {
    const m=candidates[0];
    try {
    result.status='saved';
    result.draft={id:String(m.id()),account:input.account,mailbox:String(m.mailbox().name()),subject:input.subject,sender:result.sender,content:String(m.content()),toRecipients:input.to,ccRecipients:input.cc || [],bccRecipients:input.bcc || []};
    return {ok:true,result:result,attempted:true,saved:true};
    } catch(e) { if(e.errorNumber !== -1728) throw e; }
   }
   delay(0.1);
  }
  fail('Saved draft could not be verified directly in target account Drafts');
 } catch(e) {
  let cleanup='';
  // After a send/save attempt, keep every potentially submitted/saved object.
  if (outgoing !== null && !attempted) {
   try { mail.close(outgoing,{saving:'no'}); }
   catch(cleanupError) { cleanup='cleanup failed: '+String(cleanupError); }
  }
  return {ok:false,result:result,phase:step,error:String(e && e.message ? e.message : e),attempted:attempted,uncertain:attempted&&!accepted,cleanup:cleanup,saved:saved};
 }
}
`
}
