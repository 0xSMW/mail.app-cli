package mail

import (
	"errors"
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

func nativeUnsupportedRead(err error) bool {
	var ae *appleevents.Error
	return errors.As(err, &ae) && (ae.Code == -1708 || ae.Code == -1700 || ae.Code == -10000)
}

// Contiguous source indexes use one bounded range per metadata column. Sparse
// singleton selections retain exact-ID reads. Both routes check the IDs again
// after metadata so an arrival or reorder cannot attach fields to another row.
func (n *nativeMail) selectedMessageMetadata(box appleevents.Value, account, mailbox string, selected []nativeSelectedMessage, projection nativeMessageProjection) ([]Message, error) {
	result := make([]Message, 0, len(selected))
	codes := projection.codes()
	for start := 0; start < len(selected); {
		end := start + 1
		for end < len(selected) && selected[end].index == selected[end-1].index+1 {
			end++
		}
		run := selected[start:end]
		handled := false
		if len(run) > 1 {
			bounded := appleevents.Range("mssg", run[0].index, run[len(run)-1].index, box)
			columns, err := n.getFields(bounded, codes...)
			if err != nil && !nativeUnsupportedRead(err) {
				return nil, err
			}
			if err == nil {
				for _, code := range codes {
					if columns[code].Kind != "list" || len(columns[code].Items) != len(run) {
						return nil, fmt.Errorf("Mail returned inconsistent %s bounded message column", code)
					}
				}
				finalIDs, err := n.get(appleevents.Property("ID  ", bounded))
				if err != nil {
					return nil, err
				}
				if finalIDs.Kind != "list" || len(finalIDs.Items) != len(run) {
					return nil, fmt.Errorf("message range changed during metadata read")
				}
				for i, selection := range run {
					row := make(map[string]appleevents.Value, len(codes))
					for _, code := range codes {
						row[code] = columns[code].Items[i]
					}
					if err := projection.validate(row); err != nil {
						return nil, err
					}
					if row["ID  "].Number != selection.id.Number || finalIDs.Items[i].Kind != "int" || finalIDs.Items[i].Number != selection.id.Number {
						return nil, fmt.Errorf("message range changed during metadata read")
					}
					result = append(result, *nativeMessageFields(row, account, mailbox))
				}
				handled = true
			}
		}
		if !handled {
			for _, selection := range run {
				message, err := n.exactMessageMetadata(box, account, mailbox, selection.id.Number, projection)
				if err != nil {
					return nil, err
				}
				result = append(result, *message)
			}
		}
		start = end
	}
	return result, nil
}

// Whole-mailbox columns avoid Mail's costly repeated range evaluation. Limit
// that amplification to moderate collections; larger collections retain exact
// scoped-ID reads for the selected page. No message property bags are read.
const nativeBulkMetadataMessageLimit = 16384

func (n *nativeMail) listMessageMetadata(box appleevents.Value, account, mailbox string, ids appleevents.Value, filterColumns map[string]appleevents.Value, selected []nativeSelectedMessage) ([]Message, error) {
	result := make([]Message, 0, len(selected))
	all := appleevents.All("mssg", box)
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
		} else {
			for _, selection := range selected {
				message, err := n.exactMessageMetadata(box, account, mailbox, selection.id.Number, nativeListMessageProjection)
				if err != nil {
					return nil, err
				}
				result = append(result, *message)
			}
		}
	} else {
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
