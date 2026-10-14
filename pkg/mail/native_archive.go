package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NativeArchiveError stops a batch when the native action may have run or its
// owned window could not be cleaned up. Such an operation must not be replayed.
type NativeArchiveError struct{ Err error }

func (e *NativeArchiveError) Error() string { return e.Err.Error() }
func (e *NativeArchiveError) Unwrap() error { return e.Err }

type archiveViewerOwnership struct {
	ViewerID    int    `json:"viewerId"`
	PreviousApp string `json:"previousApp"`
}

func needsNativeArchive(source, destination string) bool {
	return strings.EqualFold(source, "INBOX") && destination == "All Mail"
}

// A mailbox called All Mail is not sufficient evidence of a Gmail account.
// Other providers keep their explicit destination-based move behavior.
func (c *Client) archiveUsesNative(account, source, destination string) (bool, error) {
	if !needsNativeArchive(source, destination) {
		return false, nil
	}
	c.shared.accountsMu.Lock()
	useNative, cached := c.shared.nativeArchiveAccounts[account]
	c.shared.accountsMu.Unlock()
	if cached {
		return useNative, nil
	}
	quoted, _ := json.Marshal(account)
	output, err := c.runJXA("// mail-app-cli-native-archive-account\nconst account = Application('Mail').accounts.byName(" + string(quoted) + ");\n" + nativeArchiveGmailHelper + "JSON.stringify(isGmailArchiveAccount(account));")
	if err != nil {
		return false, err
	}
	var eligible *bool
	if err := json.Unmarshal([]byte(output), &eligible); err != nil || eligible == nil {
		return false, fmt.Errorf("native archive account check returned an invalid response")
	}
	useNative = *eligible
	c.shared.accountsMu.Lock()
	if c.shared.nativeArchiveAccounts == nil {
		c.shared.nativeArchiveAccounts = make(map[string]bool)
	}
	c.shared.nativeArchiveAccounts[account] = useNative
	c.shared.accountsMu.Unlock()
	return useNative, nil
}

const nativeArchiveGmailHelper = `
function isGmailArchiveAccount(account) {
    return /^imap\.(gmail|googlemail)\.com\.?$/i.test(String(account.serverName()).trim());
}
`

// Check permission before a batch performs its separately requested mark-read.
// The action repeats this check and validates the actual Mail controls.
func (c *Client) preflightNativeArchive() error {
	output, err := c.runJXA(`ObjC.import('ApplicationServices');if (!$.AXIsProcessTrusted()) throw Error('Native Gmail archive requires Accessibility access for the app running mail-app-cli');'ready';`)
	if err != nil {
		return err
	}
	if output != "ready" {
		return fmt.Errorf("native Mail archive permission check returned an invalid response")
	}
	return nil
}

// archiveInboxThroughMail uses Mail's native Archive action. Gmail's INBOX is
// a label: Mail's generic scripting move to All Mail can leave it on the server.
// No provider credentials or provider API are used here.
func (c *Client) archiveInboxThroughMail(account, messageID string) error {
	return c.runNativeArchive(account, messageID, true)
}

func (c *Client) runNativeArchive(account, messageID string, apply bool) (err error) {
	id, parseErr := strconv.ParseUint(messageID, 10, 53)
	if parseErr != nil || id == 0 {
		return fmt.Errorf("invalid native archive message ID: %q", messageID)
	}
	ctx := c.Context()
	lockCtx, cancel := context.WithTimeout(ctx, automationLockTimeout)
	defer cancel()
	if err := acquireAutomationGate(lockCtx); err != nil {
		return automationLockError("native archive", ctx, err)
	}
	defer releaseAutomationGate()
	release, err := acquireAutomationProcessLock(lockCtx)
	if err != nil {
		return automationLockError("native archive", ctx, err)
	}
	defer release()

	request, _ := json.Marshal(map[string]any{"account": account, "id": id, "apply": apply})
	script := "const request = " + string(request) + ";\n" + jxaPhaseHelper() + nativeArchiveScript
	execution, runErr := runAutomationUnlocked(ctx, "native archive", defaultAutomationTimeout, "-l", "JavaScript", "-e", script)
	var owner archiveViewerOwnership
	pressed := false
	for _, event := range execution.Events {
		if event.Name == "archive-viewer-owned" && event.State == "done" {
			if decodeErr := json.Unmarshal([]byte(event.Note), &owner); decodeErr != nil || owner.ViewerID <= 0 {
				return &NativeArchiveError{fmt.Errorf("native archive ownership record unreadable; inspect Mail before retrying")}
			}
		}
		if event.Name == "native-archive-press" && event.State == "start" {
			pressed = true
		}
	}
	if owner.ViewerID > 0 {
		ownership, _ := json.Marshal(owner)
		cleanupScript := "const owner = " + string(ownership) + ";\n" + nativeArchiveAXHelper + nativeArchiveCleanupScript
		cleanup, cleanupErr := runAutomationUnlocked(context.WithoutCancel(ctx), "native archive cleanup", 5*time.Second, "-l", "JavaScript", "-e", cleanupScript)
		if cleanupErr != nil || cleanup.Output != "closed" {
			return &NativeArchiveError{errors.Join(runErr, fmt.Errorf("native archive window %d cleanup failed; inspect Mail before retrying: %v", owner.ViewerID, cleanupErr))}
		}
	}
	if runErr != nil {
		if pressed {
			return &NativeArchiveError{fmt.Errorf("native Archive was issued; outcome is uncertain, reconcile without replaying: %w", runErr)}
		}
		return runErr
	}
	var result struct {
		Prepared *bool `json:"prepared"`
		Applied  *bool `json:"applied"`
	}
	if json.Unmarshal([]byte(execution.Output), &result) != nil || result.Prepared == nil || !*result.Prepared || result.Applied == nil || *result.Applied != apply || owner.ViewerID == 0 {
		err := fmt.Errorf("native archive returned an incomplete result")
		if pressed {
			return &NativeArchiveError{err}
		}
		return err
	}
	return nil
}

const nativeArchiveAXHelper = `
ObjC.import('AppKit');
ObjC.import('ApplicationServices');
ObjC.bindFunction('AXUIElementCreateApplication', ['id', ['int']]);
ObjC.bindFunction('AXUIElementCopyAttributeValue', ['int', ['id', 'id', 'id*']]);
ObjC.bindFunction('AXUIElementSetAttributeValue', ['int', ['id', 'id', 'id']]);
ObjC.bindFunction('AXUIElementPerformAction', ['int', ['id', 'id']]);
ObjC.bindFunction('CFEqual', ['bool', ['id', 'id']]);
function ax(element, key) {
    const value = Ref();
    const status = $.AXUIElementCopyAttributeValue(element, $(key), value);
    if (status !== 0) throw Error('Mail accessibility ' + key + ' failed: ' + status);
    return ObjC.unwrap(value[0]);
}
function optionalAX(element, key) { try { return ax(element, key); } catch (e) { return null; } }
function mailAX() {
    const apps = $.NSRunningApplication.runningApplicationsWithBundleIdentifier('com.apple.mail');
    if (Number(apps.count) !== 1) throw Error('Mail must be running');
    return $.AXUIElementCreateApplication(apps.objectAtIndex(0).processIdentifier);
}
function frontApp() { return ObjC.unwrap($.NSWorkspace.sharedWorkspace.frontmostApplication.bundleIdentifier); }
function appleScript(source) {
    const error = Ref();
    const result = $.NSAppleScript.alloc.initWithSource($(source)).executeAndReturnError(error);
    if (!result) throw Error('Mail scripting failed: ' + JSON.stringify(ObjC.deepUnwrap(error[0])));
    return result;
}
function asText(text) { return '"' + String(text).replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\r/g, '\\r').replace(/\n/g, '\\n') + '"'; }
function findAX(element, id, depth) {
    if (depth > 8) return null;
    const identifier = optionalAX(element, 'AXIdentifier');
    if (identifier === id) return element;
    if (identifier === 'MailboxesOutlineView') return null;
    for (const child of optionalAX(element, 'AXChildren') || []) {
        const found = findAX(child, id, depth + 1);
        if (found) return found;
    }
    return null;
}
`

const nativeArchiveScript = nativeArchiveAXHelper + nativeArchiveGmailHelper + `
if (!$.AXIsProcessTrusted()) throw Error('Native Gmail archive requires Accessibility access for the app running mail-app-cli');
const mail = Application('Mail');
const account = mail.accounts.byName(request.account);
if (!isGmailArchiveAccount(account)) throw Error('Account is no longer configured for Gmail archive');
const source = account.mailboxes.byName('INBOX');
const target = source.messages.byId(request.id);
function identity(message) {
    return JSON.stringify([message.id(), message.subject(), message.sender(), +message.dateSent(), message.messageSize()]);
}
const expectedIdentity = identity(target);
const expectedSubject = target.subject();
const accountID = account.id();
const previousApp = frontApp();
const viewerID = Number(appleScript('tell application "Mail"\nset ownedViewer to make new message viewer with properties {preview pane is visible:false}\nreturn id of ownedViewer\nend tell').int32Value);
if (!Number.isSafeInteger(viewerID) || viewerID <= 0) throw Error('Mail did not return an owned viewer');
emitPhase({name:'archive-viewer-owned',state:'done',note:JSON.stringify({viewerId:viewerID,previousApp:previousApp})});
const viewer = mail.messageViewers.byId(viewerID);
appleScript('tell application "Mail"\nset sourceBox to mailbox "INBOX" of account ' + asText(request.account) + '\nset selected mailboxes of message viewer id ' + viewerID + ' to {sourceBox}\nset selected messages of message viewer id ' + viewerID + ' to {first message of sourceBox whose id is ' + request.id + '}\nset visible of window of message viewer id ' + viewerID + ' to true\nactivate\nend tell');
const root = mailAX();
const windowID = 'Mail.messageViewer.window.' + viewerID;
let win = null, table = null;
const readyDeadline = Date.now() + 3000;
do {
    win = ax(root,'AXWindows').find(w => optionalAX(w,'AXIdentifier') === windowID);
    if (win) table = findAX(win,'Mail.messageList',0);
    if (table) break;
    delay(0.05);
} while (Date.now() < readyDeadline);
if (!table) throw Error('Owned Mail message list did not become ready');
function selectionMatches() {
    if (viewer.previewPaneIsVisible()) throw Error('Mail preview became visible');
    const boxes = viewer.selectedMailboxes() || [];
    if (boxes.length !== 1 || String(boxes[0].name()).toLowerCase() !== 'inbox' || boxes[0].account().id() !== accountID) throw Error('Mail source selection changed');
    const selected = viewer.selectedMessages() || [];
    return selected.length === 1 && identity(selected[0]) === expectedIdentity;
}
const rows = ax(table,'AXRows');
const selectedRows = ax(table,'AXSelectedRows');
const hints = [];
for (const selected of selectedRows) {
    const index = rows.findIndex(row => $.CFEqual(row,selected));
    if (index >= 0) hints.push(index);
}
const candidates = [], seen = new Set();
function add(index) { if (index >= 0 && index < rows.length && !seen.has(index)) {seen.add(index);candidates.push(index);} }
for (let distance = 0; distance < 40; distance++) for (const hint of hints) {add(hint + distance);add(hint - distance);}
for (let index = 0; index < Math.min(rows.length,80); index++) add(index);
const lookupDeadline = Date.now() + 5000;
let selectedRow = null;
for (const index of candidates) {
    if (Date.now() >= lookupDeadline) break;
    const row = rows[index];
    const label = findAX(row,'Mail.messageList.cell.view.subjectLabel',0);
    if (!label || ax(label,'AXValue') !== expectedSubject) continue;
    if (findAX(row,'Mail.messageList.cell.view.disclosureButton',0)) continue;
    if (viewer.previewPaneIsVisible()) throw Error('Mail preview became visible');
    if ($.AXUIElementSetAttributeValue(table,$('AXSelectedRows'),$([row])) !== 0) throw Error('Could not select exact Mail row');
    if (selectionMatches()) {selectedRow = row;break;}
}
if (!selectedRow) throw Error('Could not safely select the exact individual message in Mail; archive it in Mail directly');
function guard() {
    if (frontApp() !== 'com.apple.mail' || optionalAX(ax(root,'AXFocusedWindow'),'AXIdentifier') !== windowID) throw Error('Mail focus changed');
    const selected = ax(table,'AXSelectedRows');
    if (selected.length !== 1 || !$.CFEqual(selected[0],selectedRow) || !selectionMatches()) throw Error('Mail message selection changed');
    if (findAX(selectedRow,'Mail.messageList.cell.view.disclosureButton',0)) throw Error('Conversation archive is not allowed for an individual message');
}
guard();
let archiveTitle = null;
for (const top of ax(ax(root,'AXMenuBar'),'AXChildren')) {
    if (optionalAX(top,'AXIdentifier') !== 'Mail.menuBar.messageMenu') continue;
    for (const menu of ax(top,'AXChildren')) for (const item of ax(menu,'AXChildren')) {
        if (optionalAX(item,'AXIdentifier') === 'Mail.menuBar.messageMenu.archive' && ax(item,'AXEnabled')) archiveTitle = ax(item,'AXTitle');
    }
}
if (!archiveTitle) throw Error('Mail native Archive is unavailable');
const toolbar = ax(win,'AXChildren').find(child => optionalAX(child,'AXRole') === 'AXToolbar');
const archiveButtons = [];
function collectButtons(element,depth) {
    if (!element || depth > 5) return;
    if (optionalAX(element,'AXRole') === 'AXButton' && optionalAX(element,'AXDescription') === archiveTitle) archiveButtons.push(element);
    for (const child of optionalAX(element,'AXChildren') || []) collectButtons(child,depth + 1);
}
collectButtons(toolbar,0);
if (archiveButtons.length !== 1 || !ax(archiveButtons[0],'AXEnabled')) throw Error('Owned Mail Archive button is unavailable');
guard();
if (request.apply) phase('native-archive-press',function () {
    const status = $.AXUIElementPerformAction(archiveButtons[0],$('AXPress'));
    if (status !== 0) throw Error('Mail Archive action returned ' + status + '; do not retry');
});
JSON.stringify({prepared:true,applied:request.apply});
`

const nativeArchiveCleanupScript = `
const mail = Application('Mail');
const viewers = mail.messageViewers();
const viewer = viewers.find(v => v.id() === owner.viewerId);
if (viewer) {
    const root = mailAX();
    const focused = optionalAX(root,'AXFocusedWindow');
    const ownedFocus = frontApp() === 'com.apple.mail' && focused && optionalAX(focused,'AXIdentifier') === 'Mail.messageViewer.window.' + owner.viewerId;
    mail.close(viewer.window(),{saving:'no'});
    if (ownedFocus && owner.previousApp && owner.previousApp !== 'com.apple.mail' && frontApp() === 'com.apple.mail') {
        const previous = $.NSRunningApplication.runningApplicationsWithBundleIdentifier(owner.previousApp);
        if (Number(previous.count) === 1) previous.objectAtIndex(0).activateWithOptions(2);
    }
}
if (mail.messageViewers().some(v => v.id() === owner.viewerId)) throw Error('Owned Mail viewer remains open');
'closed';
`
