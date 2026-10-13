// Executes generated production JXA against an account-owned special Drafts
// hierarchy. All files and mutable state belong to the Go test's TempDir.
const fs = require('fs');
const vm = require('vm');
const config = JSON.parse(process.env.DRAFT_REGRESSION_CONFIG);
const statePath = process.env.DRAFT_REGRESSION_STATE;
const state = JSON.parse(fs.readFileSync(statePath, 'utf8'));
const script = process.argv[process.argv.length - 1];

function collection(values, name) {
    const getter = () => values;
    return new Proxy(getter, {get(target, key) {
        if (key === 'id') return () => values.map(v => v.id());
        if (key === 'name') return () => values.map(v => v.name());
        if (key === 'mailboxes') return {name: () => values.map(v => v.mailboxes().map(b => b.name()))};
        if (key === 'at') return index => values[index];
        if (key === 'byId') return id => {
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
        id: () => Number(id), mailbox: () => box,
        subject: () => data.subject, sender: () => data.sender,
        content: () => data.content, source: () => config.snapshot.source,
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
                    fs.writeFileSync(options.in, Buffer.from(attachment.base64, 'base64'));
                }
            }));
        },
        delete: () => {
            state.deleted.push(kind);
            if (kind !== 'special') throw Error('deleted unrelated message');
            state.originalRemoved = true;
            box.messages = collection([], 'messages');
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
let outgoing = null;
const inserted = [];
const mail = {
    accounts,
    draftsMailbox: () => ({mailboxes: collection(specialBoxes, 'mailboxes')}),
    outgoingMessages: {push: value => {outgoing = value; state.composed++;}},
    OutgoingMessage: properties => {
        let sender = properties.sender;
        const message = {id: () => 7, subject: () => properties.subject, content: () => properties.content};
        Object.defineProperty(message, 'sender', {get: () => () => sender, set: value => {sender = value;}});
        for (const kind of ['to', 'cc', 'bcc']) message[kind + 'Recipients'] = collection([], 'recipients');
        return message;
    },
    ToRecipient: data => ({address: () => data.address}),
    CcRecipient: data => ({address: () => data.address}),
    BccRecipient: data => ({address: () => data.address}),
    send: message => {
        if (message !== outgoing) throw Error('wrong outgoing object');
        state.sent++;
        state.sentAttachments = inserted;
        state.sentBody = message.content();
        return true;
    },
    close: () => {state.closed++;}
};
const foundation = {NSAppleScript: {alloc: {initWithSource: source => ({executeAndReturnError: () => {
    if (!source.includes('tell outgoing message id 7')) throw Error('wrong compose attachment target');
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
