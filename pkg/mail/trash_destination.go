package mail

import (
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"golang.org/x/text/unicode/norm"
)

// ResolveTrashMailbox reads Mail's special Trash mailbox, rather than guessing
// a provider's spelling. Only a unique account-owned physical mailbox is safe.
func (c *Client) ResolveTrashMailbox(accountName string) (string, error) {
	if c.nativeEnabled() {
		var destination string
		err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
			account, err := n.account(accountName)
			if err != nil {
				return err
			}
			id, err := n.stringProperty("ID  ", account)
			if err != nil {
				return err
			}
			if id == "" {
				return fmt.Errorf("Trash account ID is unavailable")
			}
			root, err := n.get(appleevents.Property("trmb", appleevents.Null()))
			if err != nil {
				return err
			}
			if !nativeReference(root) {
				return fmt.Errorf("Trash mailbox is unavailable")
			}
			boxes, err := n.collection("mbxp", root)
			if err != nil {
				return err
			}
			if len(boxes) > 4096 {
				return fmt.Errorf("Trash mailbox collection exceeds 4096 entries")
			}
			matches := make(map[string]string)
			for _, box := range boxes {
				owner, err := n.get(appleevents.Property("mact", box))
				if err != nil {
					return err
				}
				if owner.Kind == "null" || ((owner.Kind == "enum" || owner.Kind == "type") && owner.Code == "msng") {
					continue
				}
				if !nativeReference(owner) {
					return fmt.Errorf("Trash mailbox account is unavailable")
				}
				ownerID, err := n.stringProperty("ID  ", owner)
				if err != nil {
					return err
				}
				if ownerID != id {
					continue
				}
				name, err := n.stringProperty("pnam", box)
				if err != nil {
					return err
				}
				if strings.TrimSpace(name) == "" {
					return fmt.Errorf("Trash mailbox name is unavailable")
				}
				identity, path, err := n.canonicalMailbox(nativeMailboxRef{ref: box, name: name}, id)
				if err != nil {
					return err
				}
				if strings.TrimSpace(path) == "" {
					return fmt.Errorf("Trash mailbox path is unavailable")
				}
				matches[identity] = path
			}
			if len(matches) != 1 {
				return fmt.Errorf("target account Trash mailbox cannot be identified uniquely")
			}
			for _, path := range matches {
				destination = path
			}
			return nil
		})
		return destination, err
	}
	var result struct {
		Path string `json:"path"`
	}
	script := jxaTrashMailboxHelper() + fmt.Sprintf(`
const mail = Application('Mail');
const account = uniqueTrashAccount(mail, '%s');
JSON.stringify({path: trashPathForAccount(mail, account)});
`, escapeJSString(accountName))
	if err := c.runJXAJSON(script, defaultAutomationTimeout, "resolve Trash mailbox", &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Path) == "" {
		return "", fmt.Errorf("Trash mailbox path is unavailable")
	}
	return result.Path, nil
}

// prepareDeleteItem pins the recoverable destination before any write. A
// nonempty destination must come from this run's ResolveTrashMailbox result.
func (c *Client) prepareDeleteItem(item *BatchItem, destination string) error {
	if destination == "" {
		var err error
		destination, err = c.ResolveTrashMailbox(item.Account)
		if err != nil {
			return fmt.Errorf("resolve recoverable Trash: %w", err)
		}
	}
	if item.TargetMailbox != "" && norm.NFC.String(item.TargetMailbox) != norm.NFC.String(destination) {
		return fmt.Errorf("delete destination does not match the account's actual Trash mailbox")
	}
	if strings.TrimSpace(item.SourceMailbox) == "" {
		return fmt.Errorf("delete source mailbox is unavailable")
	}
	var source string
	if c.nativeEnabled() {
		err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
			account, err := n.account(item.Account)
			if err != nil {
				return err
			}
			box, err := n.mailboxExact(account, item.SourceMailbox)
			if err != nil {
				return err
			}
			id, err := n.stringProperty("ID  ", account)
			if err != nil {
				return err
			}
			name, err := n.stringProperty("pnam", box)
			if err != nil {
				return err
			}
			_, source, err = n.canonicalMailbox(nativeMailboxRef{ref: box, name: name}, id)
			return err
		})
		if err != nil {
			return fmt.Errorf("resolve delete source: %w", err)
		}
	} else {
		var result struct {
			Path string `json:"path"`
		}
		script := jxaTrashMailboxHelper() + fmt.Sprintf(`
const mail = Application('Mail');
const account = uniqueTrashAccount(mail, '%s');
JSON.stringify({path: deleteSourcePath(account, '%s')});
`, escapeJSString(item.Account), escapeJSString(item.SourceMailbox))
		if err := c.runJXAJSON(script, defaultAutomationTimeout, "resolve delete source", &result); err != nil {
			return err
		}
		source = result.Path
	}
	if strings.TrimSpace(source) == "" {
		return fmt.Errorf("delete source mailbox is unavailable")
	}
	if norm.NFC.String(source) == norm.NFC.String(destination) {
		return fmt.Errorf("message is already in Trash; refusing a recoverable delete from Trash")
	}
	// Save full physical paths so subsequent reads and moves cannot select a
	// different folder with the same leaf name.
	item.SourceMailbox, item.TargetMailbox = source, destination
	return nil
}

// moveDeleteToTrash uses the pinned physical destination literally. Generic
// move aliases (for example Archive) must never reinterpret a special Trash.
func (c *Client) moveDeleteToTrash(item *BatchItem) error {
	if c.nativeEnabled() {
		return c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
			account, err := n.account(item.Account)
			if err != nil {
				return err
			}
			source, err := n.mailboxExact(account, item.SourceMailbox)
			if err != nil {
				return err
			}
			destination, err := n.mailboxExact(account, item.TargetMailbox)
			if err != nil {
				return err
			}
			message, err := n.message(source, item.ID)
			if err != nil {
				return err
			}
			_, err = n.command("core", "move", map[string]appleevents.Value{"----": message, "insh": destination})
			return err
		})
	}
	script := jxaTrashMailboxHelper() + fmt.Sprintf(`
const mail = Application('Mail');
try {
 const account = uniqueTrashAccount(mail, '%s');
 const source = deleteSourceMailbox(account, '%s', true);
 const destination = trashMailboxForAccount(mail, account);
 const destinationPath = trashCanonicalPath(destination, trashAccountID(account));
 if (destinationPath.normalize('NFC') !== '%s'.normalize('NFC')) throw new Error('Trash destination changed before move');
 if (trashCanonicalPath(source, trashAccountID(account)).normalize('NFC') === destinationPath.normalize('NFC')) throw new Error('Message is already in Trash');
 const messages = source.messages.whose({id: Number('%s')})();
 if (messages.length !== 1) throw new Error('Message not found uniquely in delete source');
 messages[0].mailbox = destination;
 'Success';
} catch(e) { 'Error: ' + e; }
`, escapeJSString(item.Account), escapeJSString(item.SourceMailbox), escapeJSString(item.TargetMailbox), escapeJSString(item.ID))
	return c.runJXAStatus(script)
}

func jxaTrashMailboxHelper() string {
	return `
function trashAccountID(account) {
 if (!account) throw new Error('Trash account is unavailable');
 const id = account.id();
 if (typeof id !== 'string' || !id.trim()) throw new Error('Trash account ID is unavailable');
 return id;
}
function uniqueTrashAccount(mail, name) {
 const accounts = mail.accounts.whose({name: name})();
 if (accounts.length !== 1) throw new Error('Target account cannot be identified uniquely');
 trashAccountID(accounts[0]);
 return accounts[0];
}
function trashCanonicalIdentity(box, accountID) {
 const owner = box.account();
 if (!owner || trashAccountID(owner) !== accountID) throw new Error('Mailbox resolved outside requested account');
 const parts = [];
 let current = box;
 for (let depth = 0; depth <= 32; depth++) {
  const name = current.name();
  if (typeof name !== 'string' || !name.trim()) throw new Error('Mailbox name is unavailable');
  parts.unshift(name);
  let parent;
  try { parent = current.container(); } catch (e) {
   if (e.errorNumber === -1728) return JSON.stringify(parts);
   throw e;
  }
  if (parent === null || parent === undefined) return JSON.stringify(parts);
  if (depth === 32) throw new Error('Mailbox hierarchy exceeds 32 levels');
  current = parent;
 }
 throw new Error('Mailbox path is unavailable');
}
function trashCanonicalPath(box, accountID) {
 return JSON.parse(trashCanonicalIdentity(box, accountID)).join('/');
}
function trashMailboxForAccount(mail, account) {
 const id = trashAccountID(account);
 const matches = new Map();
 const boxes = mail.trashMailbox().mailboxes();
 if (boxes.length > 4096) throw new Error('Trash mailbox collection exceeds 4096 entries');
 for (const box of boxes) {
  const owner = box.account();
  if (owner && trashAccountID(owner) === id) matches.set(trashCanonicalIdentity(box, id), box);
 }
 if (matches.size !== 1) throw new Error('Target account Trash mailbox cannot be identified uniquely');
 return Array.from(matches.values())[0];
}
function trashPathForAccount(mail, account) {
 return trashCanonicalPath(trashMailboxForAccount(mail, account), trashAccountID(account));
}
function deleteSourceMailbox(account, requested, literal) {
 const id = trashAccountID(account);
 const matches = new Map();
 let visited = 0;
 function walk(container, depth) {
  if (depth > 32) throw new Error('Mailbox hierarchy exceeds 32 levels');
  for (const box of container.mailboxes()) {
   if (++visited > 4096) throw new Error('Mailbox hierarchy exceeds 4096 entries');
   const name = box.name();
   const candidate = name.normalize('NFC') === requested.normalize('NFC') ||
    (requested.toLowerCase() === 'inbox' && name.toLowerCase() === 'inbox') ||
    (requested.indexOf('/') >= 0 && requested.normalize('NFC').endsWith('/' + name.normalize('NFC'))) ||
    (!literal && ['archive', 'all mail', '[gmail]/all mail', 'gmail/all mail'].includes(requested.toLowerCase()) && ['All Mail', 'Archive'].includes(name));
   if (candidate) {
    const path = trashCanonicalPath(box, id);
    if (requested.indexOf('/') < 0 || name.normalize('NFC') === requested.normalize('NFC') || path.normalize('NFC') === requested.normalize('NFC')) matches.set(trashCanonicalIdentity(box, id), box);
   }
   walk(box, depth + 1);
  }
 }
 walk(account, 0);
 if (matches.size !== 1) throw new Error('Delete source mailbox cannot be identified uniquely');
 return Array.from(matches.values())[0];
}
function deleteSourcePath(account, requested) {
 return trashCanonicalPath(deleteSourceMailbox(account, requested, true), trashAccountID(account));
}
`
}

// getLiteralMessageDetailsForVerification reads bounded metadata in the exact
// physical mailbox used by a delete snapshot. Alias-aware readers could prove
// a same-envelope copy in a different mailbox after Trash was pinned.
func (c *Client) getLiteralMessageDetailsForVerification(accountName, mailbox, id string) (*Message, error) {
	if c.nativeEnabled() {
		var result *Message
		err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
			account, err := n.account(accountName)
			if err != nil {
				return err
			}
			box, err := n.mailboxExact(account, mailbox)
			if err != nil {
				return err
			}
			message, err := n.message(box, id)
			if err != nil {
				return err
			}
			result, err = n.messageMetadata(message, accountName, mailbox)
			return err
		})
		return result, err
	}
	var result *Message
	script := jxaTrashMailboxHelper() + jxaRFCMessageIDHelper() + fmt.Sprintf(`
const mail = Application('Mail');
const account = uniqueTrashAccount(mail, '%s');
const box = deleteSourceMailbox(account, '%s', true);
const candidates = box.messages.whose({id: Number('%s')})();
if (candidates.length !== 1) throw new Error('RFC candidate not found uniquely in literal mailbox');
const msg = candidates[0];
JSON.stringify({id: String(msg.id()), rfcMessageId: rfcMessageId(msg), sender: msg.sender(), subject: msg.subject(), dateSent: msg.dateSent().toISOString(), messageSize: msg.messageSize(), deleted: msg.deletedStatus()});
`, escapeJSString(accountName), escapeJSString(mailbox), escapeJSString(id))
	if err := c.runJXAJSON(script, defaultAutomationTimeout, "literal RFC identity metadata", &result); err != nil {
		return nil, err
	}
	return result, nil
}
