package mail

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

type nativeAccountProjectionPolicy bool

const (
	nativeDiscoveryAccounts nativeAccountProjectionPolicy = false
	nativeSenderAccounts    nativeAccountProjectionPolicy = true
)

// Each projection owns a fresh observation. Codes retain caller order, and
// the final ID read brackets precisely those columns without extra getters.
type nativeAccountProjection struct {
	policy   nativeAccountProjectionPolicy
	codes    []string
	columns  map[string][]appleevents.Value
	finalIDs []appleevents.Value
}

func (n *nativeMail) projectAccounts(policy nativeAccountProjectionPolicy, codes ...string) (*nativeAccountProjection, error) {
	all := appleevents.All("mact", appleevents.Null())
	p := &nativeAccountProjection{policy: policy, codes: codes, columns: make(map[string][]appleevents.Value, len(codes))}
	for _, code := range codes {
		value, err := n.get(appleevents.Property(code, all))
		if err != nil {
			return nil, err
		}
		if value.Kind != "list" {
			if policy == nativeSenderAccounts {
				return nil, fmt.Errorf("account %s column unavailable", code)
			}
			return nil, fmt.Errorf("Mail returned %s for account %s column; expected list", value.Kind, code)
		}
		p.columns[code] = value.Items
	}
	for _, code := range codes {
		if len(p.columns[code]) != len(p.columns["ID  "]) {
			if policy == nativeSenderAccounts {
				return nil, fmt.Errorf("account %s column length differs", code)
			}
			return nil, fmt.Errorf("Mail returned inconsistent account %s column", code)
		}
	}
	final, err := n.get(appleevents.Property("ID  ", all))
	if err != nil {
		return nil, err
	}
	if final.Kind != "list" || len(final.Items) != len(p.columns["ID  "]) {
		return nil, p.changed()
	}
	p.finalIDs = final.Items
	return p, nil
}

func (p *nativeAccountProjection) changed() error {
	if p.policy == nativeSenderAccounts {
		return fmt.Errorf("account collection changed during sender resolution")
	}
	return fmt.Errorf("account collection changed during metadata read")
}

func (p *nativeAccountProjection) row(i int, seen map[string]bool) (map[string]appleevents.Value, error) {
	id, final := p.columns["ID  "][i], p.finalIDs[i]
	if p.policy == nativeSenderAccounts && (id.Kind != "string" || id.Text == "") {
		return nil, fmt.Errorf("account ID ownership unavailable")
	}
	if id.Kind != "string" || final.Kind != "string" || id.Text != final.Text {
		return nil, p.changed()
	}
	if p.policy == nativeSenderAccounts {
		if err := p.claimID(id.Text, seen); err != nil {
			return nil, err
		}
	}
	row := make(map[string]appleevents.Value, len(p.codes))
	for _, code := range p.codes {
		row[code] = p.columns[code][i]
	}
	return row, nil
}

// Discovery claims after field decoding; sender ownership claims before it.
// Keep that ordering so malformed fields retain their existing error priority.
func (p *nativeAccountProjection) claimID(id string, seen map[string]bool) error {
	if seen[id] {
		if p.policy == nativeSenderAccounts {
			return fmt.Errorf("account ID must resolve uniquely")
		}
		return fmt.Errorf("Mail returned duplicate account ID %q", id)
	}
	seen[id] = true
	return nil
}
