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
 return {id:()=>id,subject:()=>data.subject,content:()=>data.body,sender:()=>data.sender || (config.accounts || [{addresses:['sender@example.com']}])[0].addresses[0],
 toRecipients:()=>recipients(data.to || []),ccRecipients:()=>recipients(data.cc || []),bccRecipients:()=>recipients(data.bcc || []),
 mailAttachments:()=> (data.attachments||[]).map(path=>({name:()=>path.split('/').pop()}))};
}
const drafts={name:()=> 'Drafts',messages:()=> {
 if (!closed) return config.preexisting ? [stored('old')] : [];
 polls++;
 if (config.neverSave || polls < (config.saveDelay || 1)) return config.preexisting ? [stored('old')] : [];
 const values = [stored('stored-900',config.savedChange)];
 if (config.ambiguousSave) values.push(stored('stored-901'));
 if (config.preexisting) values.push(stored('old'));
 return values;
}};
const accounts=(config.accounts || [{name:'Account',id:'id-1',enabled:true,addresses:['sender@example.com']}]).map(data=>({
 name:()=>data.name,id:()=>data.id,enabled:()=>data.enabled,
 emailAddresses:()=>{ if(config.accountGetterThrows) throw new Error('address lookup failed'); return data.addresses; },
 mailboxes:()=>config.missingDrafts ? [] : [drafts]
}));
const mail={
 accounts:()=>{accountReads++;if(config.disableBeforeMutation && accountReads>=3) accounts[0].enabled=()=>false;return accounts;},
 outgoingMessages:{push:message=>{ composed++; outgoing=message; }},
 OutgoingMessage:properties=> {
  const message={subject:()=>properties.subject,content:()=>properties.content,id:()=> 'outgoing-7'};
  let sender=properties.sender;
  Object.defineProperty(message,'sender',{get:()=>()=>{ if(config.getterThrows) throw new Error('sender read failed'); reads++;if(config.fallbackBeforeMutation && reads>=2) return 'other@example.com'; return config.readback || sender; },set:value=>{ if(config.setterThrows) throw new Error('sender write failed'); if(!config.ignoreSetter) sender=value; }});
  ['to','cc','bcc'].forEach(kind=>{message[kind+'Recipients']=collection([])});
  message.content.attachments=()=>config.attachNeverReady ? [] : attached;
  message.content.attachments.push=value=>{ if(config.attachmentThrows) throw new Error('attachment insertion failed'); attached.push(value); };
  return message;
 },
 ToRecipient:p=>({address:()=>p.address}), CcRecipient:p=>({address:()=>p.address}), BccRecipient:p=>({address:()=>p.address}),
 Attachment:p=>({fileName:()=>p.fileName}),
 send:message=>{if(message!==outgoing) throw new Error('wrong outgoing');sent++;if(config.sendThrows)throw new Error('transport failed');return config.sendFalse ? false : true;},
 close:(message,options)=>{if(message!==outgoing)throw new Error('invalid close');if(options.saving==='no'){deleted++;if(config.cleanupThrows)throw new Error('cleanup failed');return;}if(options.saving!=='yes')throw new Error('invalid close');closed++; if(config.closeThrows)throw new Error('save transport failed');},
 delete:message=>{if(message!==outgoing)throw new Error('deleting unrelated object');deleted++;if(config.cleanupThrows)throw new Error('cleanup failed');}
};
const sandbox={Application:name=>{if(name!=='Mail')throw new Error('wrong app');return mail;},Path:path=>path,delay:seconds=>{events.push(['delay',seconds]);if(config.realDelay)Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,seconds*1000);},ObjC:{import:()=>{}},reply:event=>events.push(event)};
let response=vm.runInNewContext(request.script || request.helper+'\ncomposeOutgoing('+JSON.stringify(input)+','+JSON.stringify(request.action)+')',sandbox);
if(typeof response==='string')response=JSON.parse(response);
process.stdout.write(JSON.stringify({response,composed,sent,closed,deleted,polls,events,transportSender: config.transportSender || response.result.sender}));
