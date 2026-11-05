package mail

import (
	"fmt"
	netmail "net/mail"
	"strconv"
	"strings"
)

const recipientIndexIncomplete = "recipient index may omit addresses; complete headers unavailable"

func recipientIndexNeedsEvidence(state string) bool {
	return state == "" || state == "not-requested" || state == "unavailable" || state == "partial"
}

// enrichRecipientIndexEvidence adds positive observations only. Indexed To/Cc
// rows cannot certify that an address, field, or later repetition is absent.
// All queries finish before any caller-owned evidence is changed.
func (c *Client) enrichRecipientIndexEvidence(account, mailbox string, messages []Message) error {
	type scope struct{ account, mailbox string }
	type group struct {
		scope scope
		ids   []string
		rows  map[int64][]int
	}
	groups := []*group{}
	byScope := make(map[scope]*group)
	for i, message := range messages {
		needsEvidence := false
		for _, field := range recipientFields(&message)[:2] {
			if recipientIndexNeedsEvidence(field.status.State) {
				needsEvidence = true
			}
		}
		if !needsEvidence {
			continue
		}
		a, m := recipientScope(message, account, mailbox)
		id, err := strconv.ParseInt(message.ID, 10, 64)
		if err != nil || id <= 0 || strings.TrimSpace(a) == "" || strings.TrimSpace(m) == "" {
			continue
		}
		key := scope{a, m}
		g := byScope[key]
		if g == nil {
			g = &group{scope: key, rows: make(map[int64][]int)}
			byScope[key] = g
			groups = append(groups, g)
		}
		if _, seen := g.rows[id]; !seen {
			g.ids = append(g.ids, strconv.FormatInt(id, 10))
		}
		g.rows[id] = append(g.rows[id], i)
	}
	type indexedRecipient struct {
		MessageID int64
		Type      *int64
		Position  *int64
		Address   *string
	}
	parser := &netmail.AddressParser{WordDecoder: headerWordDecoder()}
	observed := make(map[int][2][]string)
	for _, g := range groups {
		_, membership, err := c.resolveLocalMessageScope(g.scope.account, g.scope.mailbox)
		if err != nil {
			return err
		}
		if membership == "" {
			continue
		}
		// typeof guards keep malformed optional index rows from becoming typed
		// JSON decode failures or coercing an unverified value into evidence.
		query := "select r.message as MessageID, " +
			"case when typeof(r.type)='integer' then r.type end as Type, " +
			"case when typeof(r.position)='integer' then r.position end as Position, " +
			"case when typeof(a.address)='text' then a.address end as Address " +
			"from recipients r join messages m on m.ROWID=r.message " +
			"join mailboxes mb on mb.ROWID=m.mailbox left join addresses a on a.ROWID=r.address " +
			"where m.deleted=0 and m.ROWID in (" + strings.Join(g.ids, ",") + ") and " + membership +
			" order by r.message,r.type,r.position;"
		var rows []indexedRecipient
		if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.Type == nil || (*row.Type != 0 && *row.Type != 1) || row.Position == nil || *row.Position < 0 || row.Address == nil {
				continue
			}
			address, err := parser.Parse(*row.Address)
			if err != nil || !usableRecipientAddress(address.Address) {
				continue
			}
			for _, i := range g.rows[row.MessageID] {
				fields := observed[i]
				fields[*row.Type] = append(fields[*row.Type], address.Address)
				observed[i] = fields
			}
		}
	}
	for i, fields := range observed {
		for j, values := range fields {
			field := recipientFields(&messages[i])[j]
			if len(values) == 0 || !recipientIndexNeedsEvidence(field.status.State) {
				continue
			}
			detail := field.status.Error
			if detail == "" {
				detail = recipientIndexIncomplete
			} else if !strings.Contains(detail, recipientIndexIncomplete) {
				detail += "; " + recipientIndexIncomplete
			}
			appendRecipientFieldEvidence(field, values, "index")
			recipientFieldFailure(field, "index", fmt.Errorf("%s", detail))
		}
	}
	return nil
}
