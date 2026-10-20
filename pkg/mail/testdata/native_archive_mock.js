// Execute the production JXA scripts against controlled Mail/AX objects.
const fs = require('fs');
const vm = require('vm');
const scenario = process.argv[2];
const [script, cleanupScript] = fs.readFileSync(0, 'utf8').split('\n// archive-mock-cleanup\n');
let selectionChecks = 0, tableChildReads = 0, sidebarChildReads = 0, postCreateReadFailed = false;
let presses = 0, activationCalls = 0, focusReads = 0, selectedMessage;
const initialFront = scenario === 'background-other-mail-window' ? 'com.apple.mail' : 'com.example.editor';
let front = initialFront;
let created = scenario.startsWith('cleanup-');
const events = [], closed = [];
const element = attributes => ({attributes});
const subject = element({AXIdentifier:'Mail.messageList.cell.view.subjectLabel', AXValue:'Same subject'});
const disclosure = element({AXIdentifier:'Mail.messageList.cell.view.disclosureButton'});
const row = element({AXChildren:scenario === 'conversation' ? [subject,disclosure] : [subject]});
const table = element({AXIdentifier:'Mail.messageList', AXRows:[row], AXSelectedRows:[row], AXChildren:[row]});
const makeButton = () => element({AXRole:'AXButton', AXDescription:'Archive', AXEnabled:true});
const button = makeButton();
if (scenario === 'disabled-button') button.attributes.AXEnabled = false;
const toolbar = element({AXRole:'AXToolbar', AXChildren:scenario === 'ambiguous-button' ? [button,makeButton()] : [button]});
const sidebar = element({AXIdentifier:'MailboxesOutlineView', AXChildren:[row]});
const win = element({AXIdentifier:'Mail.messageViewer.window.42', AXChildren:scenario === 'ambiguous-table' ? [table,element({...table.attributes}),toolbar,sidebar] : [table,toolbar,sidebar]});
const unusedWin = element({AXIdentifier:'Mail.messageViewer.window.99', AXChildren:[]});
const menuItem = element({AXIdentifier:'Mail.menuBar.messageMenu.archive', AXEnabled:scenario !== 'disabled-menu', AXTitle:'Archive'});
const menu = element({AXChildren:[menuItem]});
const menuTop = element({AXIdentifier:'Mail.menuBar.messageMenu', AXChildren:[menu]});
const menuBar = element({AXChildren:[menuTop]});
const root = element({AXWindows:scenario === 'ambiguous-window' ? [win,element({...win.attributes}),unusedWin] : [win,unusedWin], AXFocusedWindow:unusedWin, AXMenuBar:menuBar});
const message = id => ({id:()=>id,subject:()=> 'Same subject',sender:()=> 'sender',dateSent:()=>new Date(1000),messageSize:()=>100});
const target = message(7);
selectedMessage = target;
const account = {id:()=> 'account-id',serverName:()=>scenario === 'non-gmail' ? 'imap.example.com' : scenario === 'gmail-lookalike' ? 'imap.gmail.com.example.com' : scenario === 'googlemail' ? 'IMAP.GOOGLEMAIL.COM.' : 'imap.gmail.com',mailboxes:{byName:()=>source}};
const source = {name:()=> 'INBOX',account:()=>account,messages:{byId:()=>target}};
const viewer = {id:()=>{if (created && scenario === 'created-viewer-id-read-error' && !postCreateReadFailed) {postCreateReadFailed=true;throw Error('Post-creation viewer ID read failed');}return 42;},window:()=>win,previewPaneIsVisible:()=>scenario === 'preview', selectedMailboxes:()=>scenario === 'source' ? [] : [source],selectedMessages:()=>{selectionChecks++;return [scenario === 'late-selection' && selectionChecks >= 3 ? message(8) : selectedMessage];}};
const unusedViewer = {id:()=>99,window:()=>unusedWin};
let viewerClosed = false;
function viewers() {
 if (created && scenario === 'created-viewer-read-error' && !postCreateReadFailed) {postCreateReadFailed=true;throw Error('Post-creation viewer read failed');}
 if (scenario === 'existing-viewer-id') return [viewer,unusedViewer];
 if (!created || viewerClosed || scenario === 'created-viewer-missing' || scenario === 'cleanup-missing') return [unusedViewer];
 return scenario === 'created-viewer-ambiguous' || scenario === 'cleanup-ambiguous' ? [viewer,viewer,unusedViewer] : [viewer,unusedViewer];
}
const mail = {accounts:{byName:()=>account},messageViewers:viewers,close:window=>{
 if (window !== win) throw Error('Closed an unowned viewer');
 closed.push(42);viewerClosed = scenario !== 'cleanup-remains';
}};
const dollar = value => value;
Object.assign(dollar,{
 AXIsProcessTrusted:()=>true,
 AXUIElementCreateApplication:()=>root,
 AXUIElementCopyAttributeValue:(node,key,ref)=>{
  if (key === 'AXChildren' && node.attributes.AXIdentifier === 'Mail.messageList') tableChildReads++;
  if (key === 'AXChildren' && node === sidebar) sidebarChildReads++;
  if (key === 'AXFocusedWindow') {focusReads++;throw Error('Focus must not be read');}
  if (!(key in node.attributes)) return 1;
  if (node === root && key === 'AXMenuBar') {
   if (scenario === 'changed-window') root.attributes.AXWindows = [element({...win.attributes}),unusedWin];
   if (scenario === 'changed-table') win.attributes.AXChildren = [element({...table.attributes}),toolbar];
   if (scenario === 'late-ambiguous-window') root.attributes.AXWindows.push(element({...win.attributes}));
   if (scenario === 'late-ambiguous-table') win.attributes.AXChildren.push(element({...table.attributes}));
   if (scenario === 'missing-current-row') table.attributes.AXRows = [];
   if (scenario === 'duplicate-current-row') table.attributes.AXRows = [row,row];
   if (scenario === 'changed-selected-row') table.attributes.AXSelectedRows = [element({...row.attributes})];
   if (scenario === 'late-conversation') row.attributes.AXChildren.push(disclosure);
  }
  ref[0] = node.attributes[key];return 0;
 },
 AXUIElementSetAttributeValue:(node,key,value)=>{
  if (node !== table || key !== 'AXSelectedRows') throw Error('Unexpected AX mutation');
  node.attributes[key]=value;
  selectedMessage=scenario === 'wrong-id' ? message(8) : target;
  return 0;
 },
 AXUIElementCopyActionNames:(node,ref)=>{
  ref[0] = scenario === 'unsupported-button' ? [] : ['AXPress'];
  if (scenario === 'changed-button') toolbar.attributes.AXChildren = [makeButton()];
  if (scenario === 'late-disabled-button') button.attributes.AXEnabled = false;
  if (scenario === 'late-ambiguous-button') toolbar.attributes.AXChildren.push(makeButton());
  return scenario === 'action-query-error' ? 1 : 0;
 },
 AXUIElementPerformAction:(node,action)=>{
  if (node !== button || action !== 'AXPress') throw Error('Pressed outside the owned toolbar');
  if (!events.some(event=>event.name === 'native-archive-press' && event.state === 'start')) throw Error('Press attempt was not recorded');
  presses++;return scenario === 'press-error' ? 1 : 0;
 },
 CFEqual:(a,b)=>a===b,
 NSRunningApplication:{runningApplicationsWithBundleIdentifier:()=>({count:'1',objectAtIndex:()=>({processIdentifier:123,activateWithOptions:()=>{activationCalls++;}})})},
 NSWorkspace:{sharedWorkspace:{get frontmostApplication(){focusReads++;throw Error('Foreground must not be read');}}},
 NSAppleScript:{alloc:{initWithSource:source=>({executeAndReturnError:()=>{
  if (/\bactivate\b|set frontmost|AXRaise|AXFocused/.test(source)) {front='com.apple.mail';activationCalls++;throw Error('Focus request is forbidden');}
  if (source.includes('make new message viewer')) {created=true;return {int32Value:42};}
  return {};
 }})}}
});
const context = {
 request:{account:'Test',id:7,apply:scenario !== 'prepare'},owner:{viewerId:42},
 ObjC:{import:()=>{},bindFunction:()=>{},unwrap:x=>x,deepUnwrap:x=>x},
 $:dollar,Ref:()=>[],Application:()=>mail,delay:()=>{},
 emitPhase:event=>events.push(event),
 phase:(name,fn)=>{events.push({name,state:'start'});try {const result=fn();events.push({name,state:'done'});return result;} catch(err){events.push({name,state:'failed'});throw err;}}
};
let output, error, cleanupOutput, cleanupError;
try {output=vm.runInNewContext(script,context,{timeout:1000});} catch(err){error=err.message;}
if (cleanupScript && events.some(event=>event.name === 'archive-viewer-owned' && event.state === 'done')) {
 try {cleanupOutput=vm.runInNewContext(cleanupScript,{...context},{timeout:1000});} catch(err){cleanupError=err.message;}
}
process.stdout.write(JSON.stringify({presses,output,error,cleanupOutput,cleanupError,events,closed,activationCalls,focusReads,tableChildReads,sidebarChildReads,unusedViewerPresent:viewers().includes(unusedViewer),focusPreserved:front===initialFront}));
