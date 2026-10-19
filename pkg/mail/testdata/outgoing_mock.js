// Executes the production helper; only Mail's Apple Event API is mocked.
const fs = require('fs');
const vm = require('vm');
const request = JSON.parse(fs.readFileSync(0, 'utf8'));
const config = request.config || {};
const input = request.input;
const events = [];
let outgoing = null, polls = 0, attached = [], composed = 0, sent = 0, closed = 0, deleted = 0, reads = 0, accountReads = 0;
function collection(values) { const f=()=>values; f.push=x=>values.push(x); return f; }
function recipients(values) { return values.map(address=>({address:()=>address})); }
function stored(id, change) {
 const data=Object.assign({},input,change||{});
 return {id:()=>id,mailbox:()=>({name:()=>config.physicalDraftName || config.draftName || 'Drafts'}),source:()=>config.source || '',subject:()=>data.subject,content:()=>data.body,sender:()=>data.sender || (config.accounts || [{addresses:['sender@example.com']}])[0].addresses[0],
 toRecipients:()=>recipients(data.to || []),ccRecipients:()=>recipients(data.cc || []),bccRecipients:()=>recipients(data.bcc || []),
 mailAttachments:()=> (data.attachments||[]).map(path=>({name:()=>path.split('/').pop()}))};
}
const drafts={name:()=> config.draftName || 'Drafts',account:()=>accounts[0],messages:()=> {
 if(config.snapshotRead) return [stored(String(config.draftId || '42'))];
 if (!closed) return config.preexisting ? [stored('old')] : [];
 polls++;
 if(config.transientLookup && polls===1){const e=new Error('object not ready');e.errorNumber=-1728;throw e;}
 if(config.transportLookup){const e=new Error('transport timeout');e.errorNumber=-1712;throw e;}
 if (config.neverSave || polls < (config.saveDelay || 1)) return config.preexisting ? [stored('old')] : [];
 const values = [stored('stored-900',config.savedChange)];
 if (config.ambiguousSave) values.push(stored('stored-901'));
 if (config.preexisting) values.push(stored('old'));
 return values;
}};
drafts.messages=new Proxy(drafts.messages,{get:(target,key)=>{
 if(key==='at')return index=>target()[index];
 if(key==='id')return ()=>target().map(m=>m.id());
 if(/^[0-9]+$/.test(String(key)))return target()[Number(key)];
 return target[key];
}});
const accounts=(config.accounts || [{name:'Account',id:'id-1',enabled:true,addresses:['sender@example.com']}]).map(data=>({
 name:()=>data.name,id:()=>data.id,enabled:()=>data.enabled,
 emailAddresses:()=>{ if(config.accountGetterThrows) throw new Error('address lookup failed'); return data.addresses; },
 mailboxes:()=>config.missingDrafts ? [] : (config.nestedDrafts ? [{name:()=> '[Gmail]',mailboxes:()=>[drafts]}] : [drafts])
}));
const mail={
 draftsMailbox:()=>({mailboxes:()=>config.missingDrafts ? [] : (config.ambiguousDrafts ? [drafts,drafts] : [drafts])}),
 accounts:()=>{accountReads++;if(config.disableBeforeMutation && accountReads>=3) accounts[0].enabled=()=>false;return accounts;},
 outgoingMessages:{push:message=>{ composed++; outgoing=message; }},
 OutgoingMessage:properties=> {
  function property(name, value) {
   if ((config.fingerprintFailures || []).includes(name) && (!config.failAfterSend || sent > 0)) {const e=new Error(name+' unavailable');e.errorNumber=-1728;throw e;}
   return value;
  }
  const message={subject:()=>property('subject',properties.subject),content:()=>property('body',properties.content),id:()=> 7};
  let sender=properties.sender;
  Object.defineProperty(message,'sender',{get:()=>()=>{ if(config.getterThrows) throw new Error('sender read failed'); property('sender',sender); reads++;if(config.fallbackBeforeMutation && reads>=2) return 'other@example.com'; return config.readback || sender; },set:value=>{ if(config.setterThrows) throw new Error('sender write failed'); if(!config.ignoreSetter) sender=value; }});
  ['to','cc','bcc'].forEach(kind=>{const values=[];const list=()=>property(kind,values);list.push=x=>values.push(x);message[kind+'Recipients']=list;if(config.dropRecipients)list.push=()=>{}});
  message.content.attachments=()=>config.attachNeverReady ? [] : attached;
  message.content.attachments.push=value=>{ if(config.attachmentThrows) throw new Error('attachment insertion failed'); attached.push(value); };
  const content=message.content;
  Object.defineProperty(message,'content',{get:()=>content,set:value=>{properties.content=value;}});
  if(config.nullContentAttachments) message.content.attachments=()=>null;
  return message;
 },
 ToRecipient:p=>({address:()=>p.address}), CcRecipient:p=>({address:()=>p.address}), BccRecipient:p=>({address:()=>p.address}),
 Attachment:p=>({fileName:()=>p.fileName}),
 send:message=>{if(message!==outgoing) throw new Error('wrong outgoing');sent++;if(config.sendThrows)throw new Error('transport failed');
  if(config.afterSend){
   const change=config.afterSend;
   if(change.subject!==undefined)message.subject=()=>change.subject;
   if(change.body!==undefined)message.content=change.body;
   if(change.sender!==undefined)message.sender=change.sender;
   ['to','cc','bcc'].forEach(kind=>{if(change[kind]!==undefined)message[kind+'Recipients']=()=>recipients(change[kind]);});
   if(change.attachments!==undefined)attached=change.attachments.map(name=>({fileName:()=>name}));
  }
  return config.sendFalse ? false : true;},
 save:message=>{if(message!==outgoing)throw new Error('invalid save');},
 close:(message,options)=>{if(message!==outgoing)throw new Error('invalid close');if(options.saving==='no'){deleted++;if(config.cleanupThrows)throw new Error('cleanup failed');return;}if(options.saving!=='yes')throw new Error('invalid close');closed++; if(config.closeThrows)throw new Error('save transport failed');},
 delete:message=>{if(message!==outgoing)throw new Error('deleting unrelated object');deleted++;if(config.cleanupThrows)throw new Error('cleanup failed');}
};
const foundation={NSAppleScript:{alloc:{initWithSource:code=>({executeAndReturnError:error=>{
 if(code.includes('-- mail-app-cli-owned-attachment-snapshot')) {
  if(!code.includes('tell outgoing message id 7') || !code.includes('tell content')) throw new Error('invalid owned attachment snapshot');
  if((config.fingerprintFailures || []).includes('attachments') && (!config.failAfterSend || sent>0)){error[0]={message:'attachment read failed'};return null;}
  if(config.snapshotNil && (!config.failAfterSend || sent>0)) return {__objcNil:true};
  const typeCode=code=>[...code].reduce((value,ch)=>value*256+ch.charCodeAt(0),0);
  if(config.snapshotCountMissing && (!config.failAfterSend || sent>0)){
   if(!code.includes('on error errorMessage number errorNumber\nreturn {errorNumber}')) throw new Error('required count errors need outer typed error tuple');
   return {descriptorType:typeCode('list'),numberOfItems:1,descriptorAtIndex:()=>({descriptorType:typeCode('long'),int32Value:-1728})};
  }
  const malformed=(!config.malformedAfterSend || sent>0) ? (config.malformedSnapshot || {}) : {};
  const names=attached.map(a=>({descriptorType:typeCode(malformed.name || 'utxt'),stringValue:a.fileName()}));
  if((config.fingerprintFailures || []).includes('attachmentName') && (!config.failAfterSend || sent>0))names.forEach(n=>n.stringValue=null);
  const filenameMissingValue=config.filenameMissingValue || (config.filenameMissingValueAfterSend && sent>0);
  if(filenameMissingValue && code.includes('repeat with attachmentItem') && !code.includes('if attachmentFile is missing value then return {attachmentCount, {}, false}')){
   return {descriptorType:typeCode('list'),numberOfItems:1,descriptorAtIndex:()=>({descriptorType:typeCode('long'),int32Value:-1700})};
  }
  if(config.filenameMalformed && code.includes('repeat with attachmentItem')){
   return {descriptorType:typeCode('list'),numberOfItems:1,descriptorAtIndex:()=>({descriptorType:typeCode('long'),int32Value:-1700})};
  }
  const filenameMissing = filenameMissingValue || (config.filenameMissing || (config.filenameMissingAfterSend && sent>0));
  if(filenameMissing && code.includes('repeat with attachmentItem') && !code.includes('or errorNumber is -1728 then')){
   error[0]={NSAppleScriptErrorNumber:-1728,NSAppleScriptErrorBriefMessage:'Filename getter unavailable'};
   return {__objcNil:true};
  }
  const namesAvailable = names.length===0 || !(filenameMissing || config.filenameUnsupported || (config.filenameUnsupportedAfterSend && sent>0) || (code.includes('return {attachmentCount, attachmentNames, false}') && !code.includes('repeat with attachmentItem')));
  return {descriptorType:typeCode(malformed.root || 'list'),numberOfItems:3,descriptorAtIndex:i=>i===1 ? {descriptorType:typeCode(malformed.count || 'long'),int32Value:names.length} : i===3 ? {descriptorType:typeCode(malformed.flag || (config.intrinsicBoolean ? (namesAvailable ? 'true' : 'fals') : 'bool')),booleanValue:namesAvailable} : {descriptorType:typeCode(malformed.names || 'list'),numberOfItems:namesAvailable?names.length:0,descriptorAtIndex:j=>names[j-1]}};
 }
 if(!code.includes('tell outgoing message id 7') || !code.includes('tell content'))throw new Error('invalid native attachment script');
 if(config.attachmentThrows){error[0]={message:'attachment insertion failed'};return null;}
 for(const line of code.split('\n')) {
  const match=/^make new attachment with properties \{file name:\(POSIX file (".*")\)\} at after last paragraph$/.exec(line);
  if(match)attached.push({fileName:()=>JSON.parse(match[1])});
 }
 return {int32Value:config.attachNeverReady?0:attached.length};
}})}}};
const sandbox={Application:name=>{if(name!=='Mail')throw new Error('wrong app');return mail;},Path:path=>path,delay:seconds=>{events.push(['delay',seconds]);if(config.realDelay)Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,seconds*1000);},ObjC:{import:()=>{},deepUnwrap:v=>v},$:foundation,Ref:()=>[],reply:event=>events.push(event)};
let response=vm.runInNewContext(request.script || request.helper+'\ncomposeOutgoing('+JSON.stringify(input)+','+JSON.stringify(request.action)+')',sandbox);
if(typeof response==='string')response=JSON.parse(response);
process.stdout.write(JSON.stringify({response,composed,sent,closed,deleted,polls,events,composedBody:(()=>{try{return outgoing?outgoing.content():null;}catch(e){return null;}})(),transportSender: config.transportSender || (response && response.result ? response.result.sender : '')}));
