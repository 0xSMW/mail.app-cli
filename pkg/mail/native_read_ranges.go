package mail

import (
	"fmt"
	"strconv"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

type nativeSelectedMessage struct {
	id    appleevents.Value
	index int
}

func (n *nativeMail) exactMessageMetadata(box appleevents.Value, account, mailbox string, id int64, projection nativeMessageProjection) (*Message, error) {
	ref, err := n.message(box, strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	message, err := n.messageMetadataProjection(ref, account, mailbox, projection)
	if err != nil {
		return nil, err
	}
	postID, err := n.intProperty("ID  ", ref)
	if err != nil {
		return nil, err
	}
	if postID != id {
		return nil, fmt.Errorf("message ID changed during metadata read")
	}
	return message, nil
}

// Whole-mailbox columns avoid Mail's costly repeated range evaluation. Limit
// that amplification to moderate collections; larger collections retain exact
// scoped-ID reads for the selected page. No message property bags are read.
const nativeBulkMetadataMessageLimit = 16384

func (n *nativeMail) listMessageMetadata(box appleevents.Value, account, mailbox string, ids appleevents.Value, filterColumns map[string]appleevents.Value, selected []nativeSelectedMessage) ([]Message, error) {
	result := make([]Message, 0, len(selected))
	all := appleevents.All("mssg", box)
	handled := false
	if len(selected) > 1 && len(ids.Items) <= nativeBulkMetadataMessageLimit {
		columns := make(map[string]appleevents.Value, len(nativeListMessageCodes))
		columns["ID  "] = ids
		for code, column := range filterColumns {
			columns[code] = column
		}
		bulkSupported := true
		for _, code := range nativeListMessageCodes {
			if _, ok := columns[code]; ok {
				continue
			}
			column, err := n.get(appleevents.Property(code, all))
			if err != nil {
				if nativeUnsupportedRead(err) {
					bulkSupported = false
					break
				}
				return nil, err
			}
			if column.Kind != "list" || len(column.Items) != len(ids.Items) {
				return nil, fmt.Errorf("Mail returned inconsistent %s collection message column", code)
			}
			columns[code] = column
		}
		if bulkSupported {
			for _, selection := range selected {
				index := selection.index - 1
				if index < 0 || index >= len(ids.Items) || ids.Items[index].Number != selection.id.Number {
					return nil, fmt.Errorf("selected message index no longer matches its ID")
				}
				row := make(map[string]appleevents.Value, len(nativeListMessageCodes))
				for _, code := range nativeListMessageCodes {
					column := columns[code]
					if column.Kind != "list" || len(column.Items) != len(ids.Items) {
						return nil, fmt.Errorf("Mail returned inconsistent %s collection message column", code)
					}
					row[code] = column.Items[index]
				}
				if err := nativeRequireListMessageFields(row); err != nil {
					return nil, err
				}
				result = append(result, *nativeMessageFields(row, account, mailbox))
			}
			handled = true
		}
	}
	if !handled {
		for _, selection := range selected {
			message, err := n.exactMessageMetadata(box, account, mailbox, selection.id.Number, nativeListMessageProjection)
			if err != nil {
				return nil, err
			}
			result = append(result, *message)
		}
	}
	// The complete identity/order check brackets all filter and metadata columns,
	// including empty pages; no partial row becomes a successful result.
	finalIDs, err := n.get(appleevents.Property("ID  ", all))
	if err != nil {
		return nil, err
	}
	if finalIDs.Kind != "list" || len(finalIDs.Items) != len(ids.Items) {
		return nil, fmt.Errorf("message collection changed during metadata read")
	}
	for i, id := range ids.Items {
		if id.Kind != "int" || finalIDs.Items[i].Kind != "int" || id.Number != finalIDs.Items[i].Number {
			return nil, fmt.Errorf("message collection changed during metadata read")
		}
	}
	return result, nil
}
