// Execute the production JXA script against controlled Mail/AX objects.
const fs = require('fs');
const vm = require('vm');
const scenario = process.argv[2];
const script = fs.readFileSync(0, 'utf8');
let selectionChecks = 0;
let presses = 0, front = 'com.example.editor', selectedMessage;
const events = [];
const element = attributes => ({attributes});
const subject = element({AXIdentifier:'Mail.messageList.cell.view.subjectLabel', AXValue:'Same subject'});
const disclosure = element({AXIdentifier:'Mail.messageList.cell.view.disclosureButton'});
const row = element({AXChildren:scenario === 'conversation' ? [subject,disclosure] : [subject]});
const table = element({AXIdentifier:'Mail.messageList', AXRows:[row], AXSelectedRows:[row], AXChildren:[row]});
const button = element({AXRole:'AXButton', AXDescription:'Archive', AXEnabled:true});
const toolbar = element({AXRole:'AXToolbar', AXChildren:[button]});
const win = element({AXIdentifier:'Mail.messageViewer.window.42', AXChildren:[table,toolbar]});
const menuItem = element({AXIdentifier:'Mail.menuBar.messageMenu.archive', AXEnabled:true, AXTitle:'Archive'});
const menu = element({AXChildren:[menuItem]});
const menuTop = element({AXIdentifier:'Mail.menuBar.messageMenu', AXChildren:[menu]});
const menuBar = element({AXChildren:[menuTop]});
const root = element({AXWindows:[win], AXFocusedWindow:scenario === 'focus-window' ? element({AXIdentifier:'Mail.messageViewer.window.99'}) : win, AXMenuBar:menuBar});
const message = id => ({id:()=>id,subject:()=> 'Same subject',sender:()=> 'sender',dateSent:()=>new Date(1000),messageSize:()=>100});
const target = message(7);
selectedMessage = target;
const account = {id:()=> 'account-id',serverName:()=>scenario === 'non-gmail' ? 'imap.example.com' : scenario === 'gmail-lookalike' ? 'imap.gmail.com.example.com' : scenario === 'googlemail' ? 'IMAP.GOOGLEMAIL.COM.' : 'imap.gmail.com',mailboxes:{byName:()=>source}};
const source = {name:()=> 'INBOX',account:()=>account,messages:{byId:()=>target}};
const viewer = {previewPaneIsVisible:()=>scenario === 'preview', selectedMailboxes:()=>scenario === 'source' ? [] : [source],selectedMessages:()=>{selectionChecks++;return [scenario === 'late-selection' && selectionChecks >= 3 ? message(8) : selectedMessage];}};
const mail = {accounts:{byName:()=>account},messageViewers:{byId:()=>viewer}};
const dollar = value => value;
Object.assign(dollar,{
 AXIsProcessTrusted:()=>true,
 AXUIElementCreateApplication:()=>root,
 AXUIElementCopyAttributeValue:(node,key,ref)=>{
  if (!(key in node.attributes)) return 1;
  if (node === root && key === 'AXMenuBar' && scenario === 'late-foreground') front = 'com.example.other';
  ref[0] = node.attributes[key];return 0;
 },
 AXUIElementSetAttributeValue:(node,key,value)=>{
  node.attributes[key]=value;
  selectedMessage=scenario === 'wrong-id' ? message(8) : target;
  return 0;
 },
 AXUIElementPerformAction:()=>{presses++;return scenario === 'press-error' ? 1 : 0;},
 CFEqual:(a,b)=>a===b,
 NSRunningApplication:{runningApplicationsWithBundleIdentifier:()=>({count:'1',objectAtIndex:()=>({processIdentifier:123})})},
 NSWorkspace:{sharedWorkspace:{frontmostApplication:{get bundleIdentifier(){return front;}}}},
 NSAppleScript:{alloc:{initWithSource:source=>({executeAndReturnError:()=>{
  if (source.includes('make new message viewer')) return {int32Value:42};
  front=scenario === 'foreground' ? 'com.example.other' : 'com.apple.mail';
  return {};
 }})}}
});
const context = {
 request:{account:'Test',id:7,apply:scenario !== 'prepare'},
 ObjC:{import:()=>{},bindFunction:()=>{},unwrap:x=>x,deepUnwrap:x=>x},
 $:dollar,Ref:()=>[],Application:()=>mail,delay:()=>{},
 emitPhase:event=>events.push(event),
 phase:(name,fn)=>{events.push({name,state:'start'});try {const result=fn();events.push({name,state:'done'});return result;} catch(err){events.push({name,state:'failed'});throw err;}}
};
let output, error;
try {output=vm.runInNewContext(script,context,{timeout:1000});} catch(err){error=err.message;}
process.stdout.write(JSON.stringify({presses,output,error,events}));
