package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"golang.org/x/text/unicode/norm"
)

// collection resolves only the requested container. An unexpected descriptor
// is a bridge failure, never an empty collection or a reason to run a script.
func (n *nativeMail) collection(class string, container appleevents.Value) ([]appleevents.Value, error) {
	v, err := n.get(appleevents.All(class, container))
	if err != nil {
		return nil, err
	}
	if v.Kind != "list" {
		return nil, fmt.Errorf("Mail returned %s for %s collection", v.Kind, class)
	}
	for _, ref := range v.Items {
		if !nativeReference(ref) {
			return nil, fmt.Errorf("Mail returned %s for %s object reference", ref.Kind, class)
		}
	}
	return v.Items, nil
}

func (n *nativeMail) account(name string) (appleevents.Value, error) {
	selector := appleevents.ByTest("mact", appleevents.Comparison("=   ", appleevents.Property("pnam", appleevents.Examined()), appleevents.String(name)), appleevents.Null())
	value, err := n.get(selector)
	if err != nil {
		return appleevents.Value{}, err
	}
	if value.Kind != "list" {
		return appleevents.Value{}, fmt.Errorf("Mail returned %s for account name query; expected list", value.Kind)
	}
	if len(value.Items) == 0 {
		return appleevents.Value{}, notFound("account", name)
	}
	if len(value.Items) != 1 {
		return appleevents.Value{}, fmt.Errorf("account name is ambiguous: %s", name)
	}
	ref := value.Items[0]
	if !nativeReference(ref) {
		return appleevents.Value{}, fmt.Errorf("Mail returned %s for account reference", ref.Kind)
	}
	return ref, nil
}

type nativeMailboxRef struct {
	ref        appleevents.Value
	name, path string
}

func (n *nativeMail) mailboxRefs(account appleevents.Value) ([]nativeMailboxRef, error) {
	var out []nativeMailboxRef
	var walk func(appleevents.Value, string, int) error
	walk = func(container appleevents.Value, prefix string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("mailbox hierarchy exceeds 32 levels")
		}
		level := appleevents.All("mbxp", container)
		names, err := n.get(appleevents.Property("pnam", level))
		if err != nil {
			return err
		}
		if names.Kind != "list" {
			return fmt.Errorf("Mail returned %s for mailbox names; expected list", names.Kind)
		}
		if len(names.Items) == 0 {
			return nil
		}
		children, err := n.get(appleevents.Property("pnam", appleevents.All("mbxp", level)))
		if err != nil {
			return err
		}
		if children.Kind != "list" || len(children.Items) != len(names.Items) {
			return fmt.Errorf("Mail returned inconsistent mailbox child matrix")
		}
		for i, name := range names.Items {
			if name.Kind != "string" {
				return fmt.Errorf("Mail returned %s for mailbox name; expected text", name.Kind)
			}
			childNames := children.Items[i]
			if childNames.Kind != "list" {
				return fmt.Errorf("Mail returned %s for child mailbox names; expected list", childNames.Kind)
			}
			for _, childName := range childNames.Items {
				if childName.Kind != "string" {
					return fmt.Errorf("Mail returned %s for child mailbox name; expected text", childName.Kind)
				}
			}
			if len(out) >= 4096 {
				return fmt.Errorf("mailbox hierarchy exceeds 4096 entries")
			}
			path := name.Text
			if prefix != "" {
				path = prefix + "/" + path
			}
			ref := appleevents.Index("mbxp", i+1, container)
			out = append(out, nativeMailboxRef{ref: ref, name: name.Text, path: path})
			if len(childNames.Items) > 0 {
				if err := walk(ref, path, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(account, "", 0)
	return out, err
}

func (n *nativeMail) mailbox(account appleevents.Value, name string) (appleevents.Value, error) {
	if isArchiveAlias(name) {
		for _, candidate := range []string{"All Mail", "Archive"} {
			ref, err := n.mailboxExact(account, candidate)
			if err == nil {
				return ref, nil
			}
			if !errors.Is(err, ErrNotFound) {
				return appleevents.Value{}, err
			}
		}
		return appleevents.Value{}, notFound("mailbox", name)
	}
	return n.mailboxExact(account, name)
}

// mailboxExact keeps a persisted mutation destination literal. Alias lookup
// is confined to mailbox(), before an operation chooses its destination.
func (n *nativeMail) mailboxExact(account appleevents.Value, name string) (appleevents.Value, error) {
	refs, err := n.mailboxRefs(account)
	if err != nil {
		return appleevents.Value{}, err
	}
	var candidates []nativeMailboxRef
	comparisonName := norm.NFC.String(name)
	for _, box := range refs {
		boxName := norm.NFC.String(box.name)
		if boxName == comparisonName || (strings.Contains(name, "/") && (norm.NFC.String(box.path) == comparisonName || strings.HasSuffix(comparisonName, "/"+boxName))) || (strings.EqualFold(name, "INBOX") && strings.EqualFold(box.name, "INBOX")) {
			candidates = append(candidates, box)
		}
	}
	if len(candidates) == 0 {
		return appleevents.Value{}, notFound("mailbox", name)
	}
	expectedID, err := n.stringProperty("ID  ", account)
	if err != nil {
		return appleevents.Value{}, err
	}
	if expectedID == "" {
		return appleevents.Value{}, fmt.Errorf("Mail returned an empty account ID")
	}
	seen := make(map[string]bool)
	type match struct {
		candidate      nativeMailboxRef
		identity, path string
	}
	var matches []match
	for _, candidate := range candidates {
		identity, path, err := n.canonicalMailbox(candidate, expectedID)
		if err != nil {
			return appleevents.Value{}, err
		}
		if strings.Contains(name, "/") && norm.NFC.String(candidate.name) != comparisonName && norm.NFC.String(path) != comparisonName {
			continue
		}
		if !seen[identity] {
			seen[identity] = true
			matches = append(matches, match{candidate: candidate, identity: identity, path: path})
		}
	}
	if len(matches) == 0 {
		return appleevents.Value{}, notFound("mailbox", name)
	}
	if len(matches) != 1 {
		return appleevents.Value{}, fmt.Errorf("mailbox name is ambiguous in account: %s", name)
	}
	selected := matches[0]
	resolved, err := n.get(selected.candidate.ref)
	if err != nil {
		return appleevents.Value{}, err
	}
	if resolved.Kind != "object" && resolved.Kind != "opaque" {
		return appleevents.Value{}, fmt.Errorf("Mail returned %s for resolved mailbox; expected object reference", resolved.Kind)
	}
	identity, path, err := n.canonicalMailbox(nativeMailboxRef{ref: resolved, name: selected.candidate.name}, expectedID)
	if err != nil {
		return appleevents.Value{}, err
	}
	if identity != selected.identity || path != selected.path {
		return appleevents.Value{}, fmt.Errorf("mailbox identity changed during resolution")
	}
	return resolved, nil
}

// canonicalMailbox distinguishes repeated references to one physical mailbox
// from distinct mailboxes sharing a leaf name. Only matching candidates incur
// owner and ancestor reads; hundreds of unrelated label leaves stay bulk reads.
func (n *nativeMail) canonicalMailbox(candidate nativeMailboxRef, expectedAccountID string) (string, string, error) {
	actualName, err := n.stringProperty("pnam", candidate.ref)
	if err != nil {
		return "", "", err
	}
	if actualName != candidate.name {
		return "", "", fmt.Errorf("mailbox collection changed during lookup")
	}
	owner, err := n.get(appleevents.Property("mact", candidate.ref))
	if err != nil {
		return "", "", err
	}
	if !nativeReference(owner) {
		return "", "", fmt.Errorf("Mail returned %s for mailbox account; expected reference", owner.Kind)
	}
	actualID, err := n.stringProperty("ID  ", owner)
	if err != nil {
		return "", "", err
	}
	if actualID != expectedAccountID {
		return "", "", fmt.Errorf("mailbox resolved outside requested account")
	}
	parts := []string{actualName}
	current := candidate.ref
	for depth := 0; depth <= 32; depth++ {
		parent, err := n.get(appleevents.Property("mbxc", current))
		if nativeMissing(err) {
			break
		}
		if err != nil {
			return "", "", err
		}
		if parent.Kind == "null" || ((parent.Kind == "enum" || parent.Kind == "type") && parent.Code == "msng") {
			break
		}
		if parent.Kind != "object" && parent.Kind != "opaque" {
			return "", "", fmt.Errorf("Mail returned %s for mailbox container; expected reference or missing value", parent.Kind)
		}
		if depth == 32 {
			return "", "", fmt.Errorf("mailbox container hierarchy exceeds 32 levels")
		}
		name, err := n.stringProperty("pnam", parent)
		if err != nil {
			return "", "", err
		}
		parts = append([]string{name}, parts...)
		current = parent
	}
	identityParts := append([]string{expectedAccountID}, parts...)
	identity, err := json.Marshal(identityParts)
	if err != nil {
		return "", "", err
	}
	return string(identity), strings.Join(parts, "/"), nil
}

func (n *nativeMail) message(mailbox appleevents.Value, id string) (appleevents.Value, error) {
	number, err := strconv.ParseInt(id, 10, 64)
	if err != nil || number <= 0 {
		return appleevents.Value{}, fmt.Errorf("invalid message ID: %q", id)
	}
	ref := appleevents.ByID("mssg", appleevents.Int(number), mailbox)
	actual, err := n.intProperty("ID  ", ref)
	if nativeMissing(err) {
		return appleevents.Value{}, notFound("message", id)
	}
	if err != nil {
		return appleevents.Value{}, err
	}
	if actual != number {
		return appleevents.Value{}, fmt.Errorf("message ID resolution mismatch: requested %s, got %d", id, actual)
	}
	return ref, nil
}

func (n *nativeMail) scopedMessage(account, mailbox, id string) (appleevents.Value, error) {
	acc, err := n.account(account)
	if err != nil {
		return appleevents.Value{}, err
	}
	box, err := n.mailbox(acc, mailbox)
	if err != nil {
		return appleevents.Value{}, err
	}
	return n.message(box, id)
}
