// Executes generated production JXA against an account-owned special Drafts
// hierarchy. All files and mutable state belong to the Go test's TempDir.
const fs = require('fs');
const vm = require('vm');
const config = JSON.parse(process.env.DRAFT_REGRESSION_CONFIG);
const statePath = process.env.DRAFT_REGRESSION_STATE;
const state = JSON.parse(fs.readFileSync(statePath, 'utf8'));
const script = process.argv[process.argv.length - 1];
const replacementRun=script.includes(', "save"));');
if(script.includes('const draftSnapshotRead = true;'))state.snapshotReads=(state.snapshotReads || 0)+1;

state.closed=state.closed || 0;
state.preparedDeleted=state.preparedDeleted || [];
state.preparedEnumerated=state.preparedEnumerated || [];
state.preparedExported=state.preparedExported || [];
state.preparedById=state.preparedById || [];
state.ownedCloseAfterSend=state.ownedCloseAfterSend || 0;


function collection(values, name) {
    const getter = () => values;
    return new Proxy(getter, {get(target, key) {
        if (key === 'id') return () => {
            if(name==='messages' && state.originalDeleteAttempted) {
             if(config.specialState==='cleanup-malformed-ids')return [null];
             if(config.specialState==='cleanup-duplicate-ids')return [42,42];
            }
            return values.map(v=>v.id());
        };
        if (key === 'name') return () => values.map(v => v.name());
        if (key === 'mailboxes') return {name: () => values.map(v => v.mailboxes().map(b => b.name()))};
        if (key === 'at') return index => values[index];
        if (key === 'byId') return id => {
            if(String(id)==='700')state.preparedById.push(String(id));
            const value = values.find(v => String(v.id()) === String(id));
            if (!value) throw Error('object not found');
            return value;
        };
        if (key === 'byName') return wanted => {
            const value = values.find(v => v.name() === wanted);
            if (!value) throw Error('object not found');
            return value;
        };
        if (key === 'push') return value => values.push(value);
        if (/^[0-9]+$/.test(String(key))) return values[Number(key)];
        return target[key];
    }});
}
function recipients(values) { return values.map(address => ({address: () => address})); }
function storedMessage(id, kind, box) {
    const data = config.snapshot.message;
    return {
        id: () => Number(id), mailbox: () => box, deletedStatus: () => {
          if(kind==='special') {
           if(config.specialState==='inactive-original')return true;
           if(config.specialState==='invalid-deleted-status')return 1;
           if(config.specialState==='unreadable-deleted-status' || (state.originalDeleteAttempted && config.specialState.startsWith('cleanup-getter-'))) {
            if(config.specialState==='cleanup-getter-absence'){box.messages=collection([],'messages');state.originalRemoved=true;}
            const error=new Error('deleted state unavailable');error.errorNumber=-1728;throw error;
           }
           if(state.originalDeleteAttempted && config.specialState==='cleanup-tombstone')return true;
          }
          return false;
        },
        subject: () => data.subject, sender: () => data.sender,
        content: () => data.content, source: () => config.snapshot.source+(kind==='special' && ((state.sent>0 && config.specialState==='changed-original-after-send') || (state.prepared && config.specialState==='changed-original-during-replacement') || (state.snapshotReads>1 && config.specialState==='changed-before-replacement')) ? '\r\nX-Edited: true\r\n' : ''),
        toRecipients: () => recipients(data.toRecipients),
        ccRecipients: () => recipients(data.ccRecipients),
        bccRecipients: () => recipients(data.bccRecipients),
        mailAttachments: () => {
            state.enumerated.push(kind);
            return (config.attachments || []).map(attachment => ({
                name: () => attachment.name,
                fileSize: () => Buffer.from(attachment.base64, 'base64').length,
                save: options => {
                    state.exported.push(kind);
                    if (kind !== 'special') throw Error('exported unrelated message');
                    fs.writeFileSync(options.in, config.specialState==='mismatched-export' ? Buffer.from('wrong bytes') : Buffer.from(attachment.base64, 'base64'));
                }
            }));
        },
        delete: () => {
            state.deleted.push(kind);
            if (kind !== 'special') throw Error('deleted unrelated message');
            state.originalDeleteAttempted=true;
            if(config.specialState==='cleanup-tombstone'){state.originalRemoved=true;return;}
            if(config.specialState.startsWith('cleanup-getter-') || config.specialState==='cleanup-malformed-ids' || config.specialState==='cleanup-duplicate-ids')return;
            state.originalRemoved = true;
            if(config.specialState==='cleanup-account-change')work.id=()=> 'changed-account';
            box.messages = collection(box.messages().filter(message=>String(message.id())!==String(id)), 'messages');
        }
    };
}

const work = {name: () => 'Work', id: () => 'work-id', enabled: () => true, emailAddresses: () => ['alias@example.test']};
const other = {name: () => 'Other', id: () => 'other-id', enabled: () => true, emailAddresses: () => ['other@example.test']};
function mailbox(owner, kind, id) {
    const box = {name: () => 'Bozze', account: () => owner, mailboxes: collection([], 'mailboxes')};
    box.messages = collection(kind === 'special' && state.originalRemoved ? [] : [storedMessage(id, kind, box)], 'messages');
    return box;
}
const ordinary = mailbox(work, 'ordinary', 99);
const special = mailbox(work, 'special', 42);
const foreign = mailbox(other, 'foreign', 88);
const parent = {name: () => 'Nested', account: () => work, mailboxes: collection([special], 'mailboxes')};
work.mailboxes = collection([ordinary, parent], 'mailboxes');
other.mailboxes = collection([foreign], 'mailboxes');
const specialBoxes = config.specialState === 'missing' ? [foreign] :
    config.specialState === 'ambiguous' ? [foreign, special, special] : [foreign, special];
const accounts = collection([work, other], 'accounts');
let outgoing = null, composedProperties=null;
const inserted = [];
function preparedMessage(data) {
 return {id:()=>700,mailbox:()=>special,deletedStatus:()=>false,subject:()=>data.subject,sender:()=>data.sender,content:()=>data.body,
  source:()=>data.source,toRecipients:()=>recipients(data.to),ccRecipients:()=>recipients(data.cc),bccRecipients:()=>recipients(data.bcc),
  mailAttachments:()=>{state.preparedEnumerated.push('700');return data.attachments.map(file=>({name:()=>file.name,fileSize:()=>Buffer.from(file.base64,'base64').length,save:options=>{state.preparedExported.push('700');fs.writeFileSync(options.in,Buffer.from(file.base64,'base64'));}}));},
  delete:()=>{
   state.preparedDeleted.push('700');state.preparedRemoved=true;
   special.messages=collection(special.messages().filter(message=>String(message.id())!=='700'),'messages');
  }};
}
if(state.prepared && !state.preparedRemoved)special.messages.push(preparedMessage(state.prepared));
const mail = {
    accounts,
    draftsMailbox: () => ({mailboxes: collection(specialBoxes, 'mailboxes')}),
    outgoingMessages: {push: value => {outgoing = value; state.composed++;}},
    OutgoingMessage: properties => {
        let sender = properties.sender;composedProperties=properties;
        const message = {id: () => 7, subject: () => properties.subject, content: () => properties.content+(replacementRun ? '\uFFFC'.repeat(inserted.length) : '')};
        Object.defineProperty(message, 'sender', {get: () => () => sender, set: value => {sender = value;}});
        for (const kind of ['to', 'cc', 'bcc']) message[kind + 'Recipients'] = collection([], 'recipients');
        return message;
    },
    ToRecipient: data => ({address: () => data.address}),
    CcRecipient: data => ({address: () => data.address}),
    BccRecipient: data => ({address: () => data.address}),
    save: message => {
        if(message!==outgoing)throw Error('saved unrelated outgoing');
        if(!special.messages().some(value=>String(value.id())==='700')) {
         state.savedBody=composedProperties.content;state.savedAttachments=inserted.slice();
         const rendered='\n'+composedProperties.content.slice(0,-1).split('\n').map(line=>line===''?'\u2028':line).join('\n')+'\n';
         const data={subject:message.subject(),sender:message.sender(),body:replacementRun ? rendered : message.content(),
          to:message.toRecipients().map(r=>r.address()),cc:message.ccRecipients().map(r=>r.address()),bcc:message.bccRecipients().map(r=>r.address()),
          source:'X-Prepared-Draft: 700\r\n'+config.snapshot.source,attachments:inserted.slice()};
         state.prepared=data;state.preparedRemoved=false;
         special.messages.push(preparedMessage(data));
        }
    },
    send: message => {
        if (message !== outgoing) throw Error('wrong outgoing object');
        if(!state.prepared || state.preparedRemoved)throw Error('send was not prepared');
        if(state.closed>0)throw Error('owned composition closed before send');
        state.sent++;
        state.sentAttachments = inserted;
        state.sentBody = message.content();
        return true;
    },
    close: (message,options) => {
      if(message!==outgoing || (options.saving!=='no' && !(replacementRun && options.saving==='yes')))throw Error('wrong owned composition cleanup');
      state.closed++;
      if(state.sent>0)state.ownedCloseAfterSend++;
    }
};
const foundation = {NSAppleScript: {alloc: {initWithSource: source => ({executeAndReturnError: () => {
    if (!source.includes('tell outgoing message id 7')) throw Error('wrong compose attachment target');
    if(source.includes('-- mail-app-cli-owned-attachment-snapshot')) {
      const code=value=>[...value].reduce((number,letter)=>number*256+letter.charCodeAt(0),0);
      const names=inserted.map(file=>file.name);
      return {descriptorType:code('list'),numberOfItems:3,descriptorAtIndex:index=>index===1 ?
       {descriptorType:code('long'),int32Value:names.length} : index===3 ?
       {descriptorType:code('true')} :
       {descriptorType:code('list'),numberOfItems:names.length,descriptorAtIndex:item=>({descriptorType:code('utxt'),stringValue:names[item-1]})}};
    }
    for (const line of source.split('\n')) {
        const match = /^make new attachment with properties \{file name:\(POSIX file (".*")\)\} at after last paragraph$/.exec(line);
        if (match) {
            const path = JSON.parse(match[1]);
            inserted.push({name: path.split('/').pop(), base64: fs.readFileSync(path).toString('base64')});
        }
    }
    return {int32Value: inserted.length};
}})}}};
const application = name => {
    if (name !== 'Mail') throw Error('unexpected application');
    return mail;
};
application.currentApplication = () => ({});
const sandbox = {Application: application, Path: path => path, delay: () => {},
    ObjC: {import: () => {}, deepUnwrap: value => value}, $: foundation, Ref: () => [], reply: () => {}};
try {
    const output = vm.runInNewContext(script, sandbox);
    process.stdout.write(String(output));
} catch (error) {
    process.stderr.write(String(error));
    process.exitCode = 1;
} finally {
    fs.writeFileSync(statePath, JSON.stringify(state));
}
