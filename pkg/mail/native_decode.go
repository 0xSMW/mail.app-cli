package mail

import (
	"errors"
	"fmt"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func nativeList(v appleevents.Value, label string) ([]appleevents.Value, error) {
	if v.Kind != "list" {
		return nil, fmt.Errorf("%s unavailable", label)
	}
	return v.Items, nil
}

func nativeString(v appleevents.Value, label string) (string, error) {
	if v.Kind != "string" {
		return "", fmt.Errorf("%s unavailable", label)
	}
	return v.Text, nil
}

func nativeReference(v appleevents.Value) bool {
	return v.Kind == "object" || v.Kind == "opaque"
}

func (n *nativeMail) recipientAddresses(ref appleevents.Value, class string, detailed bool) ([]string, error) {
	v, err := n.get(appleevents.Property("radd", appleevents.All(class, ref)))
	if err != nil {
		return nil, err
	}
	label := "recipients"
	if detailed {
		label = "recipient addresses"
	}
	return nativeStrings(v, label, "recipient address", detailed)
}

func nativeStrings(v appleevents.Value, listLabel, itemLabel string, detailed bool) ([]string, error) {
	items, err := nativeList(v, listLabel)
	if err != nil {
		if detailed {
			return nil, fmt.Errorf("Mail returned %s for %s", v.Kind, listLabel)
		}
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, err := nativeString(item, itemLabel)
		if err != nil {
			if detailed {
				return nil, fmt.Errorf("Mail returned %s for %s; expected text", item.Kind, itemLabel)
			}
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func nativeAppleError(err error, codes ...int) bool {
	var ae *appleevents.Error
	if !errors.As(err, &ae) {
		return false
	}
	for _, code := range codes {
		if ae.Code == code {
			return true
		}
	}
	return false
}

func nativeMissing(err error) bool { return nativeAppleError(err, -1728) }

func nativeUnsupportedRead(err error) bool {
	return nativeAppleError(err, -1708, -1700, -10000)
}

func nativeOptionalProperty(err error) bool {
	return nativeAppleError(err, -1728, -1708)
}

func nativeContentUnavailable(err error) bool {
	return nativeAppleError(err, -1700, -1728)
}
