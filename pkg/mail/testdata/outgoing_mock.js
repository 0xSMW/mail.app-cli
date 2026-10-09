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
  const message={subject:()=>properties.subject,content:()=>properties.content,id:()=> 7};
  let sender=properties.sender;
  Object.defineProperty(message,'sender',{get:()=>()=>{ if(config.getterThrows) throw new Error('sender read failed'); reads++;if(config.fallbackBeforeMutation && reads>=2) return 'other@example.com'; return config.readback || sender; },set:value=>{ if(config.setterThrows) throw new Error('sender write failed'); if(!config.ignoreSetter) sender=value; }});
  ['to','cc','bcc'].forEach(kind=>{message[kind+'Recipients']=collection([]); if(config.dropRecipients)message[kind+'Recipients'].push=()=>{}});
  message.content.attachments=()=>config.attachNeverReady ? [] : attached;
  message.content.attachments.push=value=>{ if(config.attachmentThrows) throw new Error('attachment insertion failed'); attached.push(value); };
  const content=message.content;
  Object.defineProperty(message,'content',{get:()=>content,set:value=>{properties.content=value;}});
  if(config.nullContentAttachments) message.content.attachments=()=>null;
  return message;
 },
 ToRecipient:p=>({address:()=>p.address}), CcRecipient:p=>({address:()=>p.address}), BccRecipient:p=>({address:()=>p.address}),
 Attachment:p=>({fileName:()=>p.fileName}),
 send:message=>{if(message!==outgoing) throw new Error('wrong outgoing');sent++;if(config.sendThrows)throw new Error('transport failed');return config.sendFalse ? false : true;},
 save:message=>{if(message!==outgoing)throw new Error('invalid save');},
 close:(message,options)=>{if(message!==outgoing)throw new Error('invalid close');if(options.saving==='no'){deleted++;if(config.cleanupThrows)throw new Error('cleanup failed');return;}if(options.saving!=='yes')throw new Error('invalid close');closed++; if(config.closeThrows)throw new Error('save transport failed');},
 delete:message=>{if(message!==outgoing)throw new Error('deleting unrelated object');deleted++;if(config.cleanupThrows)throw new Error('cleanup failed');}
};
const foundation={NSAppleScript:{alloc:{initWithSource:code=>({executeAndReturnError:error=>{
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
process.stdout.write(JSON.stringify({response,composed,sent,closed,deleted,polls,events,composedBody:outgoing?outgoing.content():null,transportSender: config.transportSender || (response && response.result ? response.result.sender : '')}));
