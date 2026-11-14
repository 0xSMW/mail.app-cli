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
	ViewerID int `json:"viewerId"`
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
	if c.nativeEnabled() {
		var err error
		err = c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
			useNative, err = n.gmailArchiveAccount(account)
			return err
		})
		if err != nil {
			return false, err
		}
	} else {
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
	}
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
	if c.nativeEnabled() {
		if err := c.Done(); err != nil {
			return err
		}
		return c.archiveAX().Trusted()
	}
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
	if c.nativeEnabled() {
		return c.runNativeArchiveDirect(account, messageID, apply)
	}
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
ObjC.bindFunction('AXUIElementIsAttributeSettable', ['int', ['id', 'id', 'bool*']]);
ObjC.bindFunction('AXUIElementPerformAction', ['int', ['id', 'id']]);
ObjC.bindFunction('AXUIElementCopyActionNames', ['int', ['id', 'id*']]);
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
function matchingAX(element, id, depth, matches) {
    if (!element || depth > 8) return matches;
    const identifier = optionalAX(element, 'AXIdentifier');
    // A matched control owns its subtree; inspecting message rows is unnecessary.
    if (identifier === id) {matches.push(element);return matches;}
    if (identifier === 'MailboxesOutlineView' || identifier === 'Mail.sidebar' || identifier === 'Mail.messageList') return matches;
    for (const child of optionalAX(element, 'AXChildren') || []) matchingAX(child, id, depth + 1, matches);
    return matches;
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
const existingViewerIDs = mail.messageViewers().map(v => Number(v.id()));
if (existingViewerIDs.some(id => !Number.isSafeInteger(id) || id <= 0) || new Set(existingViewerIDs).size !== existingViewerIDs.length) throw Error('Could not snapshot existing Mail viewers');
const viewerID = Number(appleScript('tell application "Mail"\nset ownedViewer to make new message viewer with properties {preview pane is visible:false}\nreturn id of ownedViewer\nend tell').int32Value);
if (!Number.isSafeInteger(viewerID) || viewerID <= 0 || existingViewerIDs.includes(viewerID)) throw Error('Mail did not return a new owned viewer');
// Persist ownership before any further Mail read can fail, so cleanup remains possible.
emitPhase({name:'archive-viewer-owned',state:'done',note:JSON.stringify({viewerId:viewerID})});
const createdViewers = mail.messageViewers().filter(v => Number(v.id()) === viewerID);
if (createdViewers.length !== 1) throw Error('Could not prove ownership of the new Mail viewer');
const viewer = createdViewers[0];
appleScript('tell application "Mail"\nset sourceBox to mailbox "INBOX" of account ' + asText(request.account) + '\nset selected mailboxes of message viewer id ' + viewerID + ' to {sourceBox}\nset selected messages of message viewer id ' + viewerID + ' to {first message of sourceBox whose id is ' + request.id + '}\nset visible of window of message viewer id ' + viewerID + ' to true\nend tell');
const root = mailAX();
const windowID = 'Mail.messageViewer.window.' + viewerID;
let win = null, table = null;
const readyDeadline = Date.now() + 3000;
do {
    const windows = ax(root,'AXWindows').filter(w => optionalAX(w,'AXIdentifier') === windowID);
    if (windows.length > 1) throw Error('Owned Mail window is ambiguous');
    win = windows[0];
    if (win) {
        const tables = matchingAX(win,'Mail.messageList',0,[]);
        if (tables.length > 1) throw Error('Owned Mail message list is ambiguous');
        table = tables[0];
    }
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
// Refresh the complete exposed row collection until the session budget expires.
// Selected rows are hints only, never a restriction to the visible first page.
const lookupDeadline = Date.now() + 15000;
const expandedRows = [];
let expansions = 0, pages = 0, quietPolls = 0, scrollStarted = false, scrollFinished = false;
let scrollArea = null, scrollBar = null, pendingScroll = null;
function falseState(value) { return value === false || value === 0; }
function trueState(value) { return value === true || value === 1; }
function conversation(row) {
    if (findAX(row,'Mail.messageList.cell.view.disclosureButton',0) || (optionalAX(row,'AXDisclosedRows') || []).length > 0) return true;
    const disclosing = optionalAX(row,'AXDisclosing');
    if (disclosing === null) return false;
    if (!falseState(disclosing)) return true;
    const parent = optionalAX(row,'AXDisclosedByRow');
    if (!parent || !trueState(optionalAX(parent,'AXDisclosing'))) return true;
    if (ax(table,'AXRows').filter(current => $.CFEqual(current,parent)).length!==1) throw Error('Owned Mail disclosure parent changed');
    const level=optionalAX(row,'AXDisclosureLevel'), parentLevel=optionalAX(parent,'AXDisclosureLevel');
    return (optionalAX(parent,'AXDisclosedRows') || []).filter(child => $.CFEqual(child,row)).length!==1 || typeof level!=='number' || typeof parentLevel!=='number' || level<=parentLevel;
}
function ownedTable() {
    const windows = ax(root,'AXWindows').filter(w => optionalAX(w,'AXIdentifier') === windowID);
    const tables = matchingAX(win,'Mail.messageList',0,[]);
    if (windows.length !== 1 || !$.CFEqual(windows[0],win) || tables.length !== 1 || !$.CFEqual(tables[0],table)) throw Error('Owned Mail message list changed');
}
function childOnce(parent,child) { return ax(parent,'AXChildren').filter(item => $.CFEqual(item,child)).length === 1; }
function ownedScroll() {
    ownedTable();
    let child = table, area = null;
    for (let depth = 0; depth < 8; depth++) {
        const parent = optionalAX(child,'AXParent');
        if (!parent) return null;
        if (!childOnce(parent,child)) throw Error('Owned Mail scroll ancestry changed');
        if (!area && optionalAX(parent,'AXRole') === 'AXScrollArea') area = parent;
        if ($.CFEqual(parent,win)) {
            if (!area) return null;
            if (scrollArea && !$.CFEqual(scrollArea,area)) throw Error('Owned Mail scroll area changed');
            const bar = optionalAX(area,'AXVerticalScrollBar');
            if (!bar) return null;
            if (ax(bar,'AXRole') !== 'AXScrollBar' || !$.CFEqual(ax(bar,'AXParent'),area) || !childOnce(area,bar)) throw Error('Owned Mail scroll bar provenance is invalid');
            if (scrollBar && !$.CFEqual(scrollBar,bar)) throw Error('Owned Mail scroll bar changed');
            scrollArea = area;
            scrollBar = bar;
            return bar;
        }
        child = parent;
    }
    throw Error('Owned Mail scroll ancestry exceeded its bound');
}
function settable(element,key) { const value = Ref();return $.AXUIElementIsAttributeSettable(element,$(key),value) === 0 && !!value[0]; }
function supports(element,action) { const value = Ref();return $.AXUIElementCopyActionNames(element,value) === 0 && ObjC.deepUnwrap(value[0]).includes(action); }
function expansionControl(row) {
    const controls = matchingAX(row,'Mail.messageList.cell.view.disclosureButton',0,[]);
    if (controls.length !== 1) return null;
    const control = controls[0], state = optionalAX(row,'AXDisclosing'), value = optionalAX(control,'AXValue');
    if (!(falseState(state) || (state === null && falseState(value))) || trueState(value)) return null;
    if (!trueState(optionalAX(control,'AXEnabled')) || !supports(control,'AXPress')) return null;
    let child = control;
    for (let depth = 0; depth < 8; depth++) {
        const parent = optionalAX(child,'AXParent');
        if (!parent || !childOnce(parent,child)) return null;
        if ($.CFEqual(parent,row)) return control;
        child = parent;
    }
    return null;
}
function canExpand(row) { return (falseState(optionalAX(row,'AXDisclosing')) && settable(row,'AXDisclosing')) || !!expansionControl(row); }
function navigationDeadline() { if (Date.now() >= lookupDeadline) throw Error('Mail traversal timed out before action dispatch'); }
function scrollNumber(bar,key) { const value=ax(bar,key);if (typeof value !== 'number' || !Number.isFinite(value)) throw Error('Owned Mail scroll position is invalid');return value; }
function pageControls(node,depth,matches) {
    if (depth > 3) return;
    for (const child of optionalAX(node,'AXChildren') || []) {
        if (!$.CFEqual(ax(child,'AXParent'),node)) throw Error('Owned Mail page control ancestry changed');
        if (optionalAX(child,'AXSubrole') === 'AXIncrementPage') matches.push(child);
        pageControls(child,depth+1,matches);
    }
}
function sameRows(left,right) { return left.length === right.length && left.every((row,index) => $.CFEqual(row,right[index])); }
function advance(rows) {
    if (++quietPolls < 6 || scrollFinished) return;
    const bar=ownedScroll();
    if (!bar) {scrollFinished=true;return;}
    const value=scrollNumber(bar,'AXValue'), minimum=scrollNumber(bar,'AXMinValue'), maximum=scrollNumber(bar,'AXMaxValue');
    if (minimum >= maximum || value < minimum || value > maximum) throw Error('Owned Mail scroll range is invalid');
    if (pages >= 64) throw Error('Owned Mail scroll page limit reached');
    if (!scrollStarted && value > minimum) {
        if (!settable(bar,'AXValue')) {scrollFinished=true;return;}
        selectionMatches();
        ownedScroll();
        navigationDeadline();
        if ($.AXUIElementSetAttributeValue(bar,$('AXValue'),$(minimum)) !== 0) throw Error('Could not reset owned Mail scroll position');
    } else {
        scrollStarted=true;
        if (value >= maximum) {scrollFinished=true;return;}
        const controls=[];pageControls(bar,0,controls);
        if (controls.length > 1) throw Error('Owned Mail page control is ambiguous');
        let control=controls[0], action='AXPress';
        if (!control && supports(bar,'AXIncrement')) {control=bar;action='AXIncrement';}
        if (!control || ax(control,'AXEnabled') !== true || !supports(control,action)) {scrollFinished=true;return;}
        selectionMatches();
        ownedScroll();
        navigationDeadline();
        if ($.AXUIElementPerformAction(control,$(action)) !== 0) throw Error('Could not advance owned Mail message list');
        ownedScroll();
        if (!$.CFEqual(control,bar)) {
            const current = [];
            pageControls(bar,0,current);
            if (current.length !== 1 || !$.CFEqual(current[0],control)) throw Error('Owned Mail page control changed');
        }
    }
    scrollStarted = true;
    pages++;
    quietPolls = 0;
    pendingScroll = {rows,value,polls:0,progress:false};
}
const triedRows = [];
let selectedRow = null;
while (!selectedRow && Date.now() < lookupDeadline) {
    ownedTable();
    const rows = ax(table,'AXRows');
    if (rows.length > 10000 || triedRows.length > 10000) throw Error('Owned Mail row traversal limit reached');
    if (pendingScroll) {
        const bar = ownedScroll();
        if (!bar) throw Error('Owned Mail scroll bar disappeared');
        const value=scrollNumber(bar,'AXValue');
        if (value !== pendingScroll.value || !sameRows(rows,pendingScroll.rows)) {
            pendingScroll.progress = true;
            pendingScroll.value = value;
            pendingScroll.rows = rows;
            pendingScroll.polls = 0;
            delay(0.05);
            continue;
        }
        if (++pendingScroll.polls < 6) {delay(0.05);continue;}
        if (pendingScroll.progress) {
            // AX objects may have been recycled for a different viewport.
            triedRows.length = 0;
            expandedRows.length = 0;
        } else {
            scrollFinished = true;
        }
        pendingScroll = null;
    }
    let expanded=false;
    const hints = ax(table,'AXSelectedRows');
    const candidates = [], seen = new Set();
    function add(index) { if (index >= 0 && index < rows.length && !seen.has(index)) {seen.add(index);candidates.push(index);} }
    for (const hint of hints) add(rows.findIndex(row => $.CFEqual(row,hint)));
    for (let index = 0; index < rows.length; index++) add(index);
    for (const index of candidates) {
        if (Date.now() >= lookupDeadline) break;
        const row = rows[index];
        if (conversation(row)) {
            if (canExpand(row) && !expandedRows.some(previous => $.CFEqual(previous,row))) {
                if (expansions >= 128) throw Error('Owned Mail conversation expansion limit reached');
                selectionMatches();
                ownedTable();
                if (ax(table,'AXRows').filter(current => $.CFEqual(current,row)).length !== 1) throw Error('Owned Mail row is unavailable or ambiguous');
                if (falseState(optionalAX(row,'AXDisclosing')) && settable(row,'AXDisclosing')) {
                    navigationDeadline();
                    if ($.AXUIElementSetAttributeValue(row,$('AXDisclosing'),$(true)) !== 0) throw Error('Could not expand owned Mail conversation');
                } else {
                    const control = expansionControl(row);
                    if (!control) throw Error('Owned Mail disclosure control changed');
                    navigationDeadline();
                    if ($.AXUIElementPerformAction(control,$('AXPress')) !== 0) throw Error('Could not expand owned Mail conversation');
                }
                expandedRows.push(row);
                expansions++;
                quietPolls = 0;
                expanded = true;
                break;
            }
            continue;
        }
        if (triedRows.some(previous => $.CFEqual(previous,row))) continue;
        const label = findAX(row,'Mail.messageList.cell.view.subjectLabel',0);
        if (!label || ax(label,'AXValue') !== expectedSubject) continue;
        selectionMatches();
        ownedTable();
        if (conversation(row)) throw Error('Mail candidate became a conversation');
        if (ax(table,'AXRows').filter(current => $.CFEqual(current,row)).length !== 1) continue;
        navigationDeadline();
        if ($.AXUIElementSetAttributeValue(table,$('AXSelectedRows'),$([row])) !== 0) throw Error('Could not select exact Mail row');
        triedRows.push(row);
        const settleDeadline = Math.min(lookupDeadline,Date.now() + 500);
        let previousMatch = false;
        do {
            const current = ax(table,'AXSelectedRows');
            if (current.length !== 1 || !$.CFEqual(current[0],row)) throw Error('Mail message selection changed');
            const matches = selectionMatches();
            if (matches && previousMatch) {selectedRow = row;break;}
            previousMatch = matches;
            if (Date.now() >= settleDeadline) break;
            delay(0.025);
        } while (Date.now() < settleDeadline);
        if (selectedRow) break;
    }
    if (!selectedRow && !expanded) advance(rows);
    if (!selectedRow && scrollFinished) break;
    if (!selectedRow && Date.now() < lookupDeadline) delay(0.05);
}
if (!selectedRow) throw Error('Could not safely select the exact individual message in Mail within the lookup budget; archive it in Mail directly');
function guard() {
    const windows = ax(root,'AXWindows').filter(w => optionalAX(w,'AXIdentifier') === windowID);
    if (windows.length !== 1 || !$.CFEqual(windows[0],win)) throw Error('Owned Mail window changed');
    const tables = matchingAX(win,'Mail.messageList',0,[]);
    if (tables.length !== 1 || !$.CFEqual(tables[0],table)) throw Error('Owned Mail message list changed');
    const currentRows = ax(table,'AXRows');
    if (currentRows.filter(row => $.CFEqual(row,selectedRow)).length !== 1) throw Error('Owned Mail row changed');
    const selected = ax(table,'AXSelectedRows');
    if (selected.length !== 1 || !$.CFEqual(selected[0],selectedRow) || !selectionMatches()) throw Error('Mail message selection changed');
    if (conversation(selectedRow)) throw Error('Conversation archive is not allowed for an individual message');
}
guard();
let archiveTitle = null;
for (const top of ax(ax(root,'AXMenuBar'),'AXChildren')) {
    if (optionalAX(top,'AXIdentifier') !== 'Mail.menuBar.messageMenu') continue;
    for (const menu of ax(top,'AXChildren')) for (const item of ax(menu,'AXChildren')) {
        if (optionalAX(item,'AXIdentifier') === 'Mail.menuBar.messageMenu.archive') {
            const title = ax(item,'AXTitle');
            if (archiveTitle !== null) throw Error('Mail Archive title is ambiguous');
            archiveTitle = title;
        }
    }
}
if (!archiveTitle) throw Error('Mail native Archive is unavailable');
function archiveButton() {
    const toolbars = ax(win,'AXChildren').filter(child => optionalAX(child,'AXRole') === 'AXToolbar');
    if (toolbars.length !== 1) throw Error('Owned Mail toolbar is unavailable');
    const buttons = [];
    function collectButtons(element,depth) {
        if (!element || depth > 5) return;
        if (optionalAX(element,'AXRole') === 'AXButton' && optionalAX(element,'AXDescription') === archiveTitle) buttons.push(element);
        for (const child of optionalAX(element,'AXChildren') || []) collectButtons(child,depth + 1);
    }
    collectButtons(toolbars[0],0);
    if (buttons.length !== 1 || ax(buttons[0],'AXEnabled') !== true) throw Error('Owned Mail Archive button is unavailable');
    const actions = Ref();
    const actionStatus = $.AXUIElementCopyActionNames(buttons[0],actions);
    const actionNames = actionStatus === 0 ? ObjC.deepUnwrap(actions[0]) : null;
    if (!Array.isArray(actionNames) || !actionNames.includes('AXPress')) throw Error('Owned Mail Archive button cannot be pressed');
    return buttons[0];
}
const button = archiveButton();
guard();
if (!$.CFEqual(archiveButton(),button)) throw Error('Owned Mail Archive button changed');
if (request.apply) phase('native-archive-press',function () {
    const status = $.AXUIElementPerformAction(button,$('AXPress'));
    if (status !== 0) throw Error('Mail Archive action returned ' + status + '; do not retry');
});
JSON.stringify({prepared:true,applied:request.apply});
`

const nativeArchiveCleanupScript = `
const mail = Application('Mail');
const viewers = mail.messageViewers().filter(v => Number(v.id()) === owner.viewerId);
if (viewers.length > 1) throw Error('Owned Mail viewer is ambiguous');
if (viewers.length === 1) mail.close(viewers[0].window(),{saving:'no'});
if (mail.messageViewers().some(v => Number(v.id()) === owner.viewerId)) throw Error('Owned Mail viewer remains open');
'closed';
`
