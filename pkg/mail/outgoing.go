package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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
	var err error
	input, err = prepareOutgoing(input)
	if err != nil {
		return nil, err
	}
	if c.nativeEnabled() {
		return c.composeNativeOutgoing(input, action)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	script := jxaPhaseHelper() + jxaOutgoingHelper() + "\nJSON.stringify(composeOutgoing(" + string(payload) + ", " + fmt.Sprintf("%q", action) + "));"
	output, trace, err := c.runOutgoingJXA(script)
	if err != nil {
		attempted := action != "preview"
		var lockErr *AutomationLockTimeoutError
		if errors.As(err, &lockErr) {
			attempted = false
		}
		if trace != nil {
			attempted = trace.Pending == "outgoing_"+action || trace.LastCompleted == "outgoing_"+action
			for _, phase := range trace.Phases {
				if phase.Name == "outgoing_"+action {
					attempted = true
				}
			}
		}
		preparedSaved := false
		if action == "send" && trace != nil {
			for _, phase := range trace.Phases {
				if phase.Name == "outgoing_prepare_save" && phase.Error == "" {
					preparedSaved = true
				}
			}
		}
		cleanup := ""
		if action != "preview" && !attempted && trace != nil && trace.Pending == "outgoing_compose" {
			cleanup = "cleanup could not be confirmed after interrupted composition"
		}
		if action == "send" && !attempted && trace != nil && (trace.Pending == "outgoing_prepare_save" || preparedSaved) {
			cleanup = "prepared composition or stored draft may remain; cleanup could not be confirmed; inspect Mail before retrying"
		}
		// A completed send phase records an affirmative acknowledgement, even if
		// the bridge is interrupted while checking or closing the owned composition.
		if action == "send" && trace != nil && (trace.LastCompleted == "outgoing_send" || trace.LastCompleted == "outgoing_submitted_cleanup" || trace.Pending == "outgoing_submitted_cleanup") {
			cleanup = "submitted composition cleanup could not be confirmed"
			result := &OutgoingResult{Account: input.Account, Sender: input.Sender, Status: "submitted_cleanup_pending", Submitted: true}
			return result, &MutationError{Operation: action, Phase: "submitted_cleanup", Account: input.Account, Sender: input.Sender, Cause: err, Attempted: true, Submitted: true, Cleanup: cleanup}
		}
		return nil, &MutationError{Operation: action, Phase: "automation", Account: input.Account, Sender: input.Sender, Cause: err, Attempted: attempted, Uncertain: attempted, Cleanup: cleanup, Saved: preparedSaved}
	}
	acknowledged := action == "send" && trace != nil && (trace.LastCompleted == "outgoing_send" || trace.LastCompleted == "outgoing_submitted_cleanup" || trace.Pending == "outgoing_submitted_cleanup")
	responseFailure := func(result *OutgoingResult, cause error) (*OutgoingResult, error) {
		if result == nil {
			result = &OutgoingResult{}
		}
		if result.Account != input.Account {
			result.AccountID, result.Sender = "", input.Sender
		}
		result.Account = input.Account
		result.Submitted = true
		result.Status = "submitted_cleanup_pending"
		return result, &MutationError{Operation: action, Phase: "response", Account: input.Account, Sender: result.Sender, Cause: cause, Attempted: true, Submitted: true, Cleanup: "send acknowledged; final receipt and submitted composition cleanup could not be verified"}
	}
	var envelope outgoingEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		cause := fmt.Errorf("invalid outgoing response: %w", err)
		if acknowledged {
			return responseFailure(nil, cause)
		}
		return nil, &MutationError{Operation: action, Phase: "response", Account: input.Account, Sender: input.Sender, Cause: cause, Attempted: action != "preview", Uncertain: action != "preview"}
	}
	result := &envelope.Result
	if acknowledged {
		// The phase receipt proves acceptance, but cannot repair an incomplete
		// final response or establish that submitted-composition cleanup completed.
		validIdentity := result.Account == input.Account && result.AccountID != "" && result.Sender != "" && result.Submitted
		validStatus := (envelope.OK && result.Status == "submitted") || (!envelope.OK && result.Status == "submitted_cleanup_pending" && envelope.Phase == "submitted_cleanup" && envelope.Cleanup != "")
		if !validIdentity || !validStatus {
			return responseFailure(result, errors.New("Mail returned an incomplete outgoing receipt"))
		}
		envelope.Attempted, envelope.Uncertain = true, false
	}
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
	if action == "save" && len(input.Attachments) > 0 {
		if err := c.verifyDraftAttachments(result.Draft, input.Attachments); err != nil {
			return result, &MutationError{Operation: action, Phase: "attachment_verification", Account: input.Account, Sender: result.Sender, Cause: err, Attempted: true, Uncertain: true, Saved: true}
		}
	}
	return result, nil
}

// Keep phase receipts on cancellation or a severed one-shot bridge so a known
// send acknowledgement cannot be downgraded to an uncertain send.
func (c *Client) runOutgoingJXA(script string) (string, *PhaseTrace, error) {
	if c.session != nil {
		output, err := c.runJXA(script)
		var timeout *AutomationTimeoutError
		if errors.As(err, &timeout) {
			return output, timeout.Trace, err
		}
		return output, nil, err
	}
	ctx := c.Context()
	lockCtx, cancel := context.WithTimeout(ctx, automationLockTimeout)
	defer cancel()
	if err := acquireAutomationGate(lockCtx); err != nil {
		return "", nil, automationLockError("jxa", ctx, err)
	}
	defer releaseAutomationGate()
	release, err := acquireAutomationProcessLock(lockCtx)
	if err != nil {
		return "", nil, automationLockError("jxa", ctx, err)
	}
	defer release()
	execution, err := runAutomationUnlocked(ctx, "jxa", defaultAutomationTimeout, "-l", "JavaScript", "-e", script)
	return execution.Output, buildPhaseTrace(execution.Events, time.Now()), err
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
 const composeBody=(action === 'save' || action === 'send') && !input.body.endsWith('\n') ? input.body+'\n' : input.body;
 let result = {account: input.account, accountId: '', sender: '', status: '', submitted: false};
 function fail(message) { const error=new Error(message);error.safeDiagnostic=true;throw error; }
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
 // Read only the exact composition created by this invocation. Every field
 // is required; a failed recipient/attachment read cannot prove it unchanged.
 function ownedFingerprint(readNames) {
  function text(value) { if (typeof value !== 'string') fail('Owned composition field is not readable text'); return value; }
  function list(kind) {
   const values = outgoing[kind]();
   if (!Array.isArray(values)) fail('Owned composition recipient list is unavailable');
   return values.map(r => text(r.address()));
  }
  const id = String(outgoing.id());
  if (!/^[0-9]+$/.test(id)) fail('Owned outgoing message ID unavailable');
  // JXA cannot reliably address Mail's rich-text attachments. Use the same
  // public AppleScript content attachment surface used during insertion.
  // The outgoing object is the one created and retained by this invocation.
  const lines=['-- mail-app-cli-owned-attachment-snapshot','try','tell application "Mail"','tell outgoing message id '+id,'tell content',
   'set attachmentCount to count attachments','set attachmentNames to {}',
   'if attachmentCount is 0 then return {attachmentCount, attachmentNames, true}'];
  // Mail can expose a valid count while rejecting the public filename getter.
  // Preserve that precise baseline limitation; required count reads always fail
  // closed, and a later loss of previously readable names retains the object.
  if (readNames === false) lines.push('return {attachmentCount, attachmentNames, false}');
  else lines.push('try','repeat with attachmentItem in attachments',
   'set attachmentFile to file name of attachmentItem',
   'if attachmentFile is missing value then return {attachmentCount, {}, false}',
   'set end of attachmentNames to POSIX path of attachmentFile','end repeat',
   'on error errorMessage number errorNumber',
   'if errorNumber is -10000 or errorNumber is -1708 or errorNumber is -1728 then return {attachmentCount, {}, false}',
   'error errorMessage number errorNumber','end try','return {attachmentCount, attachmentNames, true}');
  lines.push('end tell','end tell','end tell','on error errorMessage number errorNumber','return {errorNumber}','end try');
  const error=Ref();
  const value=$.NSAppleScript.alloc.initWithSource(lines.join('\n')).executeAndReturnError(error);
  // Foundation can return a truthy JXA ObjC nil wrapper. Inspect only its
  // descriptor type; unwrapping nil/error-ref internals can crash osascript.
  // Required AppleScript runtime errors return a typed one-integer tuple.
  if (!value || !Number.isInteger(Number(value.descriptorType))) fail('Owned attachment read returned an invalid descriptor: root=invalid');
  function typeCode(code) { return code.split('').reduce((value,ch) => value*256+ch.charCodeAt(0),0); }
  function hasType(descriptor, code) { return descriptor && Number(descriptor.descriptorType) === typeCode(code); }
  function descriptorKind(descriptor) {
   if (!descriptor) return 'missing';
   const kind=Number(descriptor.descriptorType);
   if (!Number.isInteger(kind)) return 'invalid';
   const code=[24,16,8,0].map(shift => String.fromCharCode(Math.floor(kind/Math.pow(2,shift))%256)).join('');
   return JSON.stringify(code)+' ('+kind+')';
  }
  if (hasType(value,'list') && Number(value.numberOfItems) === 1) {
   const failure=value.descriptorAtIndex(1);
   if (!hasType(failure,'long')) fail('Owned attachment error tuple returned invalid type: error='+descriptorKind(failure));
   const number=Number(failure.int32Value);
   if (!Number.isInteger(number)) fail('Owned attachment error tuple returned an invalid error number');
   const readError=new Error('Owned attachment read failed: AppleScript error '+number);readError.errorNumber=number;readError.safeDiagnostic=true;throw readError;
  }
  if (!hasType(value,'list') || Number(value.numberOfItems) !== 3) fail('Owned attachment read returned an invalid descriptor: root='+descriptorKind(value));
  const countDescriptor=value.descriptorAtIndex(1);
  const names=value.descriptorAtIndex(2);
  const availableDescriptor=value.descriptorAtIndex(3);
  // AppleScript count returns a signed 32-bit integer. NSAppleEventDescriptor
  // accessors coerce other types, so validate each descriptor before reading.
  if (!hasType(countDescriptor,'long') || !hasType(names,'list') || !['bool','true','fals'].some(code => hasType(availableDescriptor,code))) {
   fail('Owned attachment read returned invalid field types: count='+descriptorKind(countDescriptor)+', names='+descriptorKind(names)+', available='+descriptorKind(availableDescriptor));
  }
  const count=Number(countDescriptor.int32Value);
  // Apple Events permits intrinsic true/fals descriptors as well as bool.
  // Intrinsic descriptors carry their value in the type, without a payload.
  let namesAvailable;
  if (hasType(availableDescriptor,'true')) namesAvailable=true;
  else if (hasType(availableDescriptor,'fals')) namesAvailable=false;
  else {
   const flag=availableDescriptor.booleanValue;
   if (typeof flag === 'boolean') namesAvailable=flag;
   else if (typeof flag === 'number' && (flag === 0 || flag === 1)) namesAvailable=flag === 1;
   else fail('Owned attachment read returned an invalid Boolean value: available='+descriptorKind(availableDescriptor));
  }
  if (!Number.isInteger(count) || count < 0 || (namesAvailable ? Number(names.numberOfItems) !== count : Number(names.numberOfItems) !== 0)) fail('Owned attachment count does not match filenames');
  const files=namesAvailable ? [] : null;
  for (let i=1;namesAvailable && i<=count;i++) {
   const nameDescriptor=names.descriptorAtIndex(i);
   if (!['TEXT','utxt','utf8'].some(code => hasType(nameDescriptor,code))) fail('Owned attachment filename has an invalid descriptor type');
   const name=ObjC.deepUnwrap(nameDescriptor.stringValue);
   if (typeof name !== 'string' || name.length === 0) fail('Owned attachment filename unavailable');
   files.push(name.split('/').pop().normalize('NFC'));
  }
  return {id:id,sender:text(outgoing.sender()),subject:text(outgoing.subject()),body:text(outgoing.content()),
   to:list('toRecipients'),cc:list('ccRecipients'),bcc:list('bccRecipients'),attachmentCount:count,files:files === null ? null : files.sort()};
 }
 function cleanupStage(name,action) {
  try {return action();} catch(e) {
   const number=e && typeof e.errorNumber === 'number' && Number.isInteger(e.errorNumber) ? e.errorNumber : null;
   const error=new Error(name+' failed'+(number === null ? '' : ' (AppleScript error '+number+')')+(e && e.safeDiagnostic ? ': '+e.message : ''));
   if(number !== null)error.errorNumber=number;
   error.safeDiagnostic=true;
   throw error;
  }
 }
 function ownedRootAbsent() {
  try {outgoing.subject();return false;} catch(e) {if(e.errorNumber === -1728)return true;throw e;}
 }
 function cleanupSubmitted(expected) {
  if(cleanupStage('owned_root_probe',ownedRootAbsent))return;
  let current;
  try {current=cleanupStage('owned_snapshot',()=>ownedFingerprint(expected.files !== null));}
  catch(e) {
   // A required nested property failure alone proves nothing about the root.
   if(e.errorNumber === -1728 && cleanupStage('owned_root_reprobe',ownedRootAbsent))return;
   throw e;
  }
  if(!equal(expected,current))fail('Submitted composition changed; retained');
  try {cleanupStage('owned_close',()=>mail.close(outgoing,{saving:'no'}));}
  catch(e) {
   if(e.errorNumber === -1728 && cleanupStage('owned_close_reprobe',ownedRootAbsent))return;
   throw e;
  }
 }
 function matchesBody(value) {
  const body=String(value).replace(/\r\n/g,'\n');
  const expected=composeBody.replace(/\r\n/g,'\n');
  // Mail's persisted rich-text view adds one leading paragraph separator.
  // Do not trim user whitespace or accept arbitrary body differences.
  return body===input.body.replace(/\r\n/g,'\n') || body===expected || body==='\n'+expected;
 }
 // Stored Drafts ownership is established before dispatch, never guessed from
 // a Sent message or a post-send subject search.
 function storedSnapshot(message) {
  function text(value) { if (typeof value !== 'string') fail('Prepared draft text property unavailable'); return value; }
  function list(kind) { const values=message[kind](); if(!Array.isArray(values)) fail('Prepared draft recipients unavailable'); return values.map(r=>text(r.address())); }
  const box=message.mailbox(), owner=box.account();
  const accountId=text(owner.id()), mailbox=text(box.name());
  if(accountId !== result.accountId) fail('Prepared draft account ownership changed');
  const attachments=message.mailAttachments();
  if(!Array.isArray(attachments)) fail('Prepared draft attachments unavailable');
  const files=attachments.map(a=>text(a.name()).normalize('NFC')).sort();
  const source=text(message.source());
  if(source.length === 0) fail('Prepared draft raw source unavailable');
  return {id:String(message.id()),accountId:accountId,mailbox:mailbox,subject:text(message.subject()),sender:text(message.sender()),body:text(message.content()),
   to:list('toRecipients'),cc:list('ccRecipients'),bcc:list('bccRecipients'),source:source,files:files};
 }
 function matchesPrepared(snapshot) {
  return snapshot.subject === input.subject && matchesBody(snapshot.body) && address(snapshot.sender).toLowerCase() === result.sender.toLowerCase() &&
   equal(snapshot.to,input.to) && equal(snapshot.cc,input.cc) && equal(snapshot.bcc,input.bcc) && equal(snapshot.files,expectedNames);
 }
 function preparedIds(values) {
  if(!Array.isArray(values))fail('Prepared Drafts IDs unavailable');
  const seen={};
  return values.map(value=> {
   let id;
   if(typeof value === 'number' && Number.isSafeInteger(value) && value > 0)id=String(value);
   else if(typeof value === 'string' && /^[1-9][0-9]*$/.test(value) && Number.isSafeInteger(Number(value)))id=value;
   else fail('Prepared Drafts ID list contains an invalid numeric identity');
   if(seen[id])fail('Prepared Drafts ID list contains duplicate identities');
   seen[id]=true;
   return id;
  });
 }
 function preparedById(box,id) {
  if(!/^[0-9]+$/.test(id) || !Number.isSafeInteger(Number(id)) || Number(id) <= 0) fail('Prepared draft ID is not a supported numeric identity');
  const ids=preparedIds(box.messages.id());
  const matching=ids.filter(value=>value===id);
  if(matching.length === 0) return null;
  if(matching.length !== 1) fail('Prepared draft ID is ambiguous');
  // The destructive reference must remain by-ID even if mailbox order changes.
  const message=box.messages.byId(Number(id));
  if(String(message.id()) !== id) fail('Prepared draft exact-ID readback mismatch');
  return message;
 }
 function prepareSendDraft(target,before) {
  phase('outgoing_prepare_save',function(){mail.save(outgoing);saved=true;});
  for(let i=0;i<80;i++) {
   const box=draftMailboxForAccount(mail,target.account);
   let candidates=[];
   try {
    const messages=box.messages();
    if(!Array.isArray(messages)) fail('Prepared Drafts messages unavailable');
    for(const message of messages) {
     const id=String(message.id());
     if(before[id]) continue;
     // Match all requested fields before taking the full immutable snapshot.
     if(matches(message)) { const exact=preparedById(box,id); if(exact === null) continue; const snapshot=storedSnapshot(exact); if(snapshot.id !== id) fail('Prepared draft identity changed during read'); if(matchesPrepared(snapshot)) candidates.push(snapshot); }
    }
   } catch(e) { if(e.errorNumber !== -1728) throw e; delay(0.1);continue; }
   if(candidates.length > 1) fail('Prepared draft identity is ambiguous');
   if(candidates.length === 1) return candidates[0];
   delay(0.1);
  }
  fail('Prepared draft could not be verified before dispatch');
 }
 function currentPreparedBox(expected) {
  const currentAccount=resolve();
  if(currentAccount.id !== result.accountId || currentAccount.id !== expected.accountId) fail('Prepared draft account identity changed; retained');
  return draftMailboxForAccount(mail,currentAccount.account);
 }
 function preparedAbsentAfterRace(expected) {
  const deadline=Date.now()+3000;
  for(let i=0;i<30;i++) {
   const box=cleanupStage('prepared_absence_owner',()=>currentPreparedBox(expected));
   const ids=cleanupStage('prepared_absence_ids',()=>preparedIds(box.messages.id()));
   const matching=ids.filter(id=>id===expected.id);
   if(matching.length > 1)fail('Prepared draft ID is ambiguous during absence confirmation');
   cleanupStage('prepared_absence_owner_confirmation',()=>currentPreparedBox(expected));
   if(matching.length === 0)return true;
   if(Date.now() >= deadline)break;
   delay(0.1);
  }
  return false;
 }
 function cleanupPreparedDraft(expected) {
  try {
   let box=cleanupStage('prepared_owner',()=>currentPreparedBox(expected));
   let message=cleanupStage('prepared_exact_lookup',()=>preparedById(box,expected.id));
   if(message === null) {cleanupStage('prepared_absent_owner_confirmation',()=>currentPreparedBox(expected));return;}
   if(!equal(expected,cleanupStage('prepared_snapshot',()=>storedSnapshot(message))))fail('Prepared draft changed after submission; retained');
   box=cleanupStage('prepared_delete_owner',()=>currentPreparedBox(expected));
   message=cleanupStage('prepared_delete_exact_lookup',()=>preparedById(box,expected.id));
   if(message === null) {cleanupStage('prepared_absent_owner_confirmation',()=>currentPreparedBox(expected));return;}
   if(!equal(expected,cleanupStage('prepared_delete_snapshot',()=>storedSnapshot(message))))fail('Prepared draft changed before removal; retained');
   cleanupStage('prepared_delete',()=>message.delete());
   for(let i=0;i<30;i++) {
    if(cleanupStage('prepared_removal_lookup',()=>preparedById(box,expected.id)) === null) {cleanupStage('prepared_removed_owner_confirmation',()=>currentPreparedBox(expected));return;}
    delay(0.1);
   }
   fail('Prepared draft removal could not be verified after submission');
  } catch(e) {
   // Only fresh ID enumeration in the guarded account-owned Drafts mailbox
   // establishes disappearance; a missing nested read or by-ID lookup cannot.
   if(e.errorNumber === -1728 && preparedAbsentAfterRace(expected))return;
   throw e;
  }
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
  if (action === 'save' || action === 'send') {
   step='draft_baseline';
   drafts = draftMailboxForAccount(mail,target.account);
   const baseline=preparedIds(drafts.messages.id());
   baseline.forEach(id => { before[id] = true; });
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
   step='send_fingerprint';
   const expected=ownedFingerprint();
   step='prepare_send';
   const prepared=prepareSendDraft(target,before);
   guardSender();
   step='prepared_compose_verification';
   if(!equal(expected,ownedFingerprint(expected.files !== null))) fail('Owned composition changed during draft preparation; retained');
   step='send';
   phase('outgoing_send',function() {
    attempted=true;
    if (mail.send(outgoing) !== true) fail('Mail did not accept send (expected true)');
    accepted=true; result.submitted=true; result.status='submitted';
   });
   step='submitted_cleanup';
   phase('outgoing_submitted_cleanup',function() { cleanupSubmitted(expected); cleanupPreparedDraft(prepared); });
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
  if (accepted) { result.status='submitted_cleanup_pending'; cleanup='submitted composition cleanup pending: '+String(e && e.message ? e.message : e); }
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
