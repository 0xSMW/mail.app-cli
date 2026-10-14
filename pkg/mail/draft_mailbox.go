package mail

// Mail's special mailbox contains account-owned mailbox references. Using
// these objects avoids guessing translated names or flattened IMAP paths.
func jxaDraftMailboxHelper() string {
	return `
function draftMailboxForAccount(mail, account, optional) {
 const id=String(account.id());
 const boxes=mail.draftsMailbox().mailboxes().filter(box=> {
  const owner=box.account();
  return owner !== null && owner !== undefined && String(owner.id()) === id;
 });
 if(optional && boxes.length === 0) return null;
 if(boxes.length !== 1) throw new Error('Target account Drafts mailbox cannot be identified uniquely');
 return boxes[0];
}
`
}
