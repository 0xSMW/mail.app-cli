// Execute the production JXA script with a synthetic Mail Apple Event surface.
const vm = require('vm');
const config = JSON.parse(process.env.SEARCH_COVERAGE_CONFIG);
const script = process.argv[process.argv.indexOf('-e') + 1];
const rows = Array.from({length: config.count || 1}, (_, i) => {
 const data = (config.messages || [])[i] || {};
 function read(key, fallback) { if ((data.fail || []).includes(key)) throw new Error(key + ' unavailable'); return data[key] === undefined ? fallback : data[key]; }
 return {
  id: () => read('id', String(i)), subject: () => read('subject', 'ordinary'),
  sender: () => read('sender', 'person@example.test'), content: () => read('content', ''),
  dateReceived: () => { const d = read('dateReceived', '2026-10-19T00:00:00Z'); return d === null ? null : new Date(d); },
  dateSent: () => new Date(read('dateSent', '2026-10-19T00:00:00Z')),
  readStatus: () => read('read', false), flaggedStatus: () => read('flagged', false), messageSize: () => read('messageSize', 10)
 };
});
const mailbox = {name: () => config.mailbox || 'INBOX', messages: () => rows, mailboxes: () => []};
const mailboxes = () => [mailbox];
mailboxes.byName = () => mailbox;
const account = {name: () => 'Work', inbox: () => config.nullInbox ? null : mailbox, mailboxes};
try {
 const result = vm.runInNewContext(script, {Application: () => ({accounts: {byName: () => account}}), Date});
 console.log(result);
} catch(e) { console.error(e.stack); process.exit(1); }
