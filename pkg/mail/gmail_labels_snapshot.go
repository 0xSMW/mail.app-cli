package mail

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Keep the index's exact sender spelling for its guarded SQL tuple. Mail's
// independently read sender may use different address casing or display text;
// matching its parsed mailbox address guards identity without making those
// presentation differences part of the logical identity.
func (c *Client) gmailLabelMessageWithRead(r GmailLabelRequest, read func(string, string, string) (*Message, error)) (*Message, error) {
	location, err := c.LocateMessage(r.MessageID)
	if err != nil {
		return nil, err
	}
	if location.Account != r.Account {
		return nil, gmailLabelError("usage", "Select the Mail account that owns this message", fmt.Errorf("Mail message belongs to a different account"))
	}
	live, err := read(r.Account, r.Mailbox, r.MessageID)
	if err != nil {
		return nil, err
	}
	if live == nil {
		return nil, notFound("Mail message", r.MessageID)
	}
	indexed := location.Envelope
	if !gmailLabelMetadataMatches(StableIdentityFromMessage(indexed), gmailLabelIdentity(*live)) {
		return nil, gmailLabelError("unavailable", "Refresh the Mail message selector and repeat the read-only lookup", fmt.Errorf("live Mail metadata differs from the indexed message identity"))
	}
	indexed.RFCMessageID = gmailRFCMessageID(live.RFCMessageID)
	return &indexed, nil
}

// gmailLabelSnapshot reads all backing and label memberships of the exact
// stable tuple in one SQL snapshot. The RFC header is then independently read
// from Mail using the newly resolved local ID. No index writes are performed.
func (c *Client) gmailLabelSnapshot(account string, id StableIdentity) (GmailLabelSnapshot, error) {
	return c.gmailLabelSnapshotWithRead(account, id, c.GetMessageDetailsForVerificationJSON)
}

func (c *Client) gmailLabelSnapshotWithRead(account string, id StableIdentity, read func(string, string, string) (*Message, error)) (GmailLabelSnapshot, error) {
	out := GmailLabelSnapshot{LocalIDs: []string{}, Memberships: []string{}}
	a, err := c.accountByName(account)
	if err != nil {
		return out, err
	}
	conditions, err := indexIdentityConditions([]StableIdentity{id})
	if err != nil {
		return out, err
	}
	prefix := "imap://" + a.ID + "/"
	var rows []struct {
		ID          int64
		Read        int
		URL         string
		Memberships string
	}
	query := fmt.Sprintf(`select m.ROWID as ID, m.read as Read, mb.url as URL,
 (select json_group_array(url) from (
 select mb.url as url union select lb.url from labels l join mailboxes lb on lb.ROWID=l.mailbox_id where l.message_id=m.ROWID
 )) as Memberships
 from messages m join mailboxes mb on mb.ROWID=m.mailbox
 join subjects s on s.ROWID=m.subject join addresses a on a.ROWID=m.sender
 where substr(mb.url,1,%d)=%s and m.deleted=0 and (%s) limit 2;`, len(prefix), sqlQuote(prefix), strings.Join(conditions, " or "))
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return out, err
	}
	if len(rows) != 1 {
		if len(rows) == 0 {
			return out, notFound("Mail message for stable identity", id.RFCMessageID)
		}
		return out, gmailLabelError("usage", "Choose a Mail message with a unique indexed stable identity", fmt.Errorf("Mail stable identity must resolve to exactly one local message; found %d", len(rows)))
	}
	row := rows[0]
	if row.ID <= 0 {
		return out, fmt.Errorf("Mail snapshot returned invalid local ID")
	}
	var memberships []string
	if err := json.Unmarshal([]byte(row.Memberships), &memberships); err != nil {
		return out, fmt.Errorf("Mail snapshot memberships are invalid")
	}
	for _, raw := range memberships {
		if !strings.HasPrefix(raw, prefix) {
			return out, fmt.Errorf("Mail membership crosses account boundary")
		}
		name, err := url.PathUnescape(strings.TrimPrefix(raw, prefix))
		if err != nil {
			return out, err
		}
		name = strings.TrimPrefix(name, "[Gmail]/")
		out.Memberships = append(out.Memberships, name)
	}
	out.LocalIDs = []string{strconv.FormatInt(row.ID, 10)}
	out.Read = row.Read != 0
	out.Memberships = labelSet(out.Memberships)
	message, err := read(account, gmailSnapshotMailbox(row.URL), out.LocalIDs[0])
	if err != nil {
		return out, err
	}
	if message == nil {
		return out, fmt.Errorf("Mail message no longer resolves after membership snapshot")
	}
	actual := gmailLabelIdentity(*message)
	if actual.RFCMessageID != id.RFCMessageID || !gmailLabelMetadataMatches(actual, id) {
		return out, gmailLabelError("unavailable", "Refresh the Mail message selector and retry the read-only lookup", fmt.Errorf("Mail message identity changed after membership snapshot"))
	}
	return out, nil
}

func gmailSnapshotMailbox(raw string) string {
	// This primary branch decodes inline instead of calling
	// mailboxLeafFromURL (it keeps the whole nested path, not just the leaf),
	// so it needs its own NFC normalization: the Envelope Index stores this
	// path in NFD, but Mail.app's live Apple Events lookups expect NFC (see
	// mailboxLeafFromURL).
	if u, err := url.Parse(raw); err == nil {
		if name, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/")); err == nil {
			return norm.NFC.String(name)
		}
	}
	return mailboxLeafFromURL(raw)
}
