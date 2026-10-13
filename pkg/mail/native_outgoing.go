package mail

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"golang.org/x/text/unicode/norm"
)

// All object references are returned by Mail or scoped to a known container.
func outgoingList(v ae.Value, label string) ([]ae.Value, error) {
	if v.Kind != "list" {
		return nil, fmt.Errorf("%s unavailable", label)
	}
	return v.Items, nil
}
func outgoingString(v ae.Value, label string) (string, error) {
	if v.Kind != "string" {
		return "", fmt.Errorf("%s unavailable", label)
	}
	return v.Text, nil
}
func outgoingID(v ae.Value) string {
	if v.Kind == "int" {
		return strconv.FormatInt(v.Number, 10)
	}
	return v.Text
}
func (n *nativeMail) outgoingAccounts() ([]ae.Value, error) {
	v, e := n.get(ae.All("mact", ae.Null()))
	if e != nil {
		return nil, e
	}
	return outgoingList(v, "accounts")
}

// Sender guards read only their four required public columns. IDs are read
// before and after the projection so an account reorder cannot associate an
// address with a different account. Each guard performs its own fresh read.
func (n *nativeMail) outgoingSender(input OutgoingInput) (ae.Value, string, string, error) {
	allRef := ae.All("mact", ae.Null())
	codes := []string{"ID  ", "pnam", "isac", "emad"}
	columns := make(map[string][]ae.Value, len(codes))
	for _, code := range codes {
		value, e := n.get(ae.Property(code, allRef))
		if e != nil {
			return ae.Value{}, "", "", e
		}
		items, e := outgoingList(value, "account "+code+" column")
		if e != nil {
			return ae.Value{}, "", "", e
		}
		columns[code] = items
	}
	ids := columns["ID  "]
	for _, code := range codes {
		if len(columns[code]) != len(ids) {
			return ae.Value{}, "", "", fmt.Errorf("account %s column length differs", code)
		}
	}
	final, e := n.get(ae.Property("ID  ", allRef))
	if e != nil {
		return ae.Value{}, "", "", e
	}
	finalIDs, e := outgoingList(final, "final account IDs")
	if e != nil || len(finalIDs) != len(ids) {
		return ae.Value{}, "", "", fmt.Errorf("account collection changed during sender resolution")
	}
	all := make([]map[string]ae.Value, len(ids))
	seenIDs := make(map[string]bool, len(ids))
	var fields map[string]ae.Value
	matches := 0
	for i, value := range ids {
		id, e := outgoingString(value, "account ID")
		if e != nil || id == "" {
			return ae.Value{}, "", "", fmt.Errorf("account ID ownership unavailable")
		}
		current, e := outgoingString(finalIDs[i], "final account ID")
		if e != nil || current != id {
			return ae.Value{}, "", "", fmt.Errorf("account collection changed during sender resolution")
		}
		if seenIDs[id] {
			return ae.Value{}, "", "", fmt.Errorf("account ID must resolve uniquely")
		}
		seenIDs[id] = true
		row := map[string]ae.Value{}
		for _, code := range codes {
			row[code] = columns[code][i]
		}
		name, e := outgoingString(row["pnam"], "account name")
		if e != nil {
			return ae.Value{}, "", "", e
		}
		if row["isac"].Kind != "bool" {
			return ae.Value{}, "", "", fmt.Errorf("account enabled state unavailable")
		}
		addresses, e := outgoingList(row["emad"], "account sender addresses")
		if e != nil {
			return ae.Value{}, "", "", e
		}
		for _, address := range addresses {
			if _, e = outgoingString(address, "account sender address"); e != nil {
				return ae.Value{}, "", "", e
			}
		}
		all[i] = row
		if name == input.Account {
			fields = row
			matches++
		}
	}
	if matches != 1 {
		return ae.Value{}, "", "", fmt.Errorf("account name must resolve uniquely")
	}
	id := fields["ID  "].Text
	if !fields["isac"].Boolean {
		return ae.Value{}, "", "", fmt.Errorf("selected account is disabled or unavailable")
	}
	addresses := fields["emad"].Items
	if len(addresses) == 0 {
		return ae.Value{}, "", "", fmt.Errorf("selected account has no configured sender addresses")
	}
	// This specifier follows the stable ID, never a position from the columns.
	// Mail resolves it directly when an account-owned Drafts reference is used.
	selected := ae.ByID("mact", ae.String(id), ae.Null())
	requested := input.Sender
	if requested == "" {
		requested = addresses[0].Text
	}
	requested, e = draftSenderAddress(requested)
	if e != nil {
		return ae.Value{}, "", "", e
	}
	chosen := ""
	count := 0
	for _, v := range addresses {
		s, e := outgoingString(v, "sender address")
		if e != nil {
			return ae.Value{}, "", "", e
		}
		s, e = draftSenderAddress(s)
		if e != nil {
			return ae.Value{}, "", "", e
		}
		if strings.EqualFold(s, requested) {
			chosen = s
			count++
		}
	}
	if count != 1 {
		return ae.Value{}, "", "", fmt.Errorf("sender must identify exactly one configured account address")
	}
	owners := 0
	for _, f := range all {
		list, e := outgoingList(f["emad"], "account sender ownership")
		if e != nil {
			return ae.Value{}, "", "", e
		}
		owned := false
		for _, v := range list {
			s, e := outgoingString(v, "account sender address")
			if e != nil {
				return ae.Value{}, "", "", e
			}
			s, e = draftSenderAddress(s)
			if e != nil {
				return ae.Value{}, "", "", e
			}
			owned = owned || strings.EqualFold(s, chosen)
		}
		if owned {
			owners++
		}
	}
	if owners != 1 {
		return ae.Value{}, "", "", fmt.Errorf("sender belongs to multiple accounts")
	}
	return selected, id, chosen, nil
}
func (n *nativeMail) outgoingRecipients(object ae.Value, class string) ([]string, error) {
	v, e := n.get(ae.Property("radd", ae.All(class, object)))
	if e != nil {
		return nil, e
	}
	list, e := outgoingList(v, "recipients")
	if e != nil {
		return nil, e
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, e := outgoingString(item, "recipient address")
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, nil
}
func outgoingEqual(a, b []string) bool {
	return len(a) == len(b) && reflect.DeepEqual(append([]string{}, a...), append([]string{}, b...))
}
func outgoingBodyMatches(actual, body string) bool {
	actual = strings.ReplaceAll(actual, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	return actual == body || actual == "\n"+body
}

// Report only structural differences; message text and native descriptor data
// never belong in a failed composition diagnostic.
func outgoingBodyDifference(actual, expected string) error {
	actual = strings.ReplaceAll(actual, "\r\n", "\n")
	expected = strings.ReplaceAll(expected, "\r\n", "\n")
	insertion := "none"
	expectedRunes, actualRunes := []rune(expected), []rune(actual)
	if len(actualRunes) == len(expectedRunes)+1 {
		i := 0
		for i < len(expectedRunes) && expectedRunes[i] == actualRunes[i] {
			i++
		}
		if string(actualRunes[i+1:]) == string(expectedRunes[i:]) {
			insertion = fmt.Sprintf("U+%04X at rune index %d", actualRunes[i], i)
		}
	}
	return fmt.Errorf("outgoing body differs from requested body (expected bytes=%d runes=%d; readback bytes=%d runes=%d; extra terminal LF=%t; extra leading-and-terminal LF=%t; extra terminal CR=%t; extra leading CR=%t; NFC-equivalent=%t; single inserted scalar=%s; readback object markers=%d)", len(expected), len(expectedRunes), len(actual), len(actualRunes), actual == expected+"\n", actual == "\n"+expected+"\n", actual == expected+"\r", actual == "\r"+expected, norm.NFC.String(actual) == norm.NFC.String(expected), insertion, strings.Count(actual, "\ufffc"))
}

func (n *nativeMail) outgoingVerify(object ae.Value, input OutgoingInput, id, sender, body string, checkBody bool) error {
	_, currentID, currentSender, e := n.outgoingSender(input)
	if e != nil {
		return e
	}
	if currentID != id || currentSender != sender {
		return fmt.Errorf("account identity changed during composition")
	}
	codes := []string{"sndr", "subj"}
	if checkBody {
		codes = append(codes, "ctnt")
	}
	fields, e := n.getFields(object, codes...)
	if e != nil {
		return e
	}
	value, e := outgoingString(fields["sndr"], "sender readback")
	if e != nil {
		return e
	}
	value, e = draftSenderAddress(value)
	if e != nil {
		return e
	}
	if !strings.EqualFold(value, sender) {
		return fmt.Errorf("Mail sender readback differs from selected sender")
	}
	value, e = outgoingString(fields["subj"], "subject readback")
	if e != nil {
		return e
	}
	if value != input.Subject {
		return fmt.Errorf("outgoing subject differs from requested subject")
	}
	if checkBody {
		value, e = outgoingString(fields["ctnt"], "body readback")
		if e != nil {
			return e
		}
		if !outgoingBodyMatches(value, body) {
			return outgoingBodyDifference(value, body)
		}
	}
	for i, class := range []string{"trcp", "crcp", "brcp"} {
		values, e := n.outgoingRecipients(object, class)
		if e != nil {
			return e
		}
		if !outgoingEqual(values, [][]string{input.To, input.Cc, input.Bcc}[i]) {
			return fmt.Errorf("outgoing recipients differ from requested recipients")
		}
	}
	return nil
}
func (n *nativeMail) outgoingPause() error {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-n.ctx.Done():
		return n.ctx.Err()
	case <-timer.C:
		return nil
	}
}

// A rich-text attachment can appear before Mail has published its file name.
// These typed missing values are readiness signals, never successful names.
var errOutgoingAttachmentPending = errors.New("attachment file name not ready")

var errOutgoingAttachmentGetterUnsupported = errors.New("attachment file name getter unsupported")

type outgoingAttachmentPendingError struct{ kind, code string }

func (e *outgoingAttachmentPendingError) Error() string {
	return fmt.Sprintf("attachment file name not ready (descriptor kind=%q code=%q)", e.kind, e.code)
}
func (e *outgoingAttachmentPendingError) Unwrap() error { return errOutgoingAttachmentPending }
func (e *outgoingAttachmentPendingError) Is(target error) bool {
	return target == errOutgoingAttachmentPending || (target == errOutgoingAttachmentGetterUnsupported && e.code == "msng" && (e.kind == "type" || e.kind == "enum" || e.kind == "opaque"))
}

func outgoingAttachmentPending(v ae.Value) bool {
	return v.Kind == "null" || ((v.Kind == "type" || v.Kind == "enum" || v.Kind == "opaque") && v.Code == "msng") || ((v.Kind == "file" || v.Kind == "string") && v.Text == "")
}

func outgoingAttachmentName(v ae.Value) (string, error) {
	if outgoingAttachmentPending(v) {
		return "", &outgoingAttachmentPendingError{kind: v.Kind, code: v.Code}
	}
	if v.Kind != "file" && v.Kind != "string" {
		return "", fmt.Errorf("attachment file name unavailable (descriptor kind=%q code=%q)", v.Kind, v.Code)
	}
	path := v.Text
	if strings.HasPrefix(path, "file:") {
		u, e := url.Parse(path)
		if e != nil || !u.IsAbs() || u.Scheme != "file" || u.Path == "" {
			return "", fmt.Errorf("attachment file name malformed (descriptor kind=%q code=%q)", v.Kind, v.Code)
		}
		path = u.Path
	}
	return norm.NFC.String(filepath.Base(path)), nil
}
func (n *nativeMail) outgoingAttachmentNames(object ae.Value, stored bool) ([]string, error) {
	class, code, container := "atts", "atfn", ae.Property("ctnt", object)
	if stored {
		class, code, container = "attc", "pnam", object
	}
	v, e := n.get(ae.Property(code, ae.All(class, container)))
	if e != nil {
		return nil, e
	}
	if !stored && outgoingAttachmentPending(v) {
		return nil, &outgoingAttachmentPendingError{kind: v.Kind, code: v.Code}
	}
	list, e := outgoingList(v, "attachment names")
	if e != nil {
		return nil, e
	}
	names := make([]string, 0, len(list))
	for _, v := range list {
		var name string
		if stored {
			name, e = outgoingString(v, "attachment name")
			name = norm.NFC.String(name)
		} else {
			name, e = outgoingAttachmentName(v)
		}
		if e != nil {
			return nil, e
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
func outgoingExpectedNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, norm.NFC.String(filepath.Base(path)))
	}
	sort.Strings(out)
	return out
}
func outgoingAttachmentRef(v ae.Value) bool {
	return (v.Kind == "object" && v.Fields["want"].Code == "atts") || (v.Kind == "opaque" && v.Code == "obj " && v.Data != "")
}

func (n *nativeMail) outgoingAttachmentCount(object ae.Value) (int64, error) {
	v, e := n.command("core", "cnte", map[string]ae.Value{"----": ae.All("atts", ae.Property("ctnt", object))})
	if e != nil {
		return 0, e
	}
	if v.Kind != "int" || v.Number < 0 {
		return 0, fmt.Errorf("attachment count unavailable (descriptor kind=%q code=%q)", v.Kind, v.Code)
	}
	return v.Number, nil
}

func (n *nativeMail) outgoingAttach(object ae.Value, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	// Only this outgoing descriptor is owned. Mail may return missing value
	// from an accepted rich-text make; the public count proves readiness.
	baseline, e := n.outgoingAttachmentCount(object)
	if e != nil {
		return e
	}
	if baseline != 0 {
		return fmt.Errorf("outgoing contains unrequested inherited attachments")
	}
	inserted := 0
	for _, path := range paths {
		ref, e := n.command("core", "crel", map[string]ae.Value{"kocl": ae.TypeCode("atts"), "insh": ae.Insertion(ae.Object("cpar", "indx", ae.Ordinal("last"), ae.Property("ctnt", object)), "afte"), "prdt": ae.Record(map[string]ae.Value{"atfn": ae.File(path)})})
		if e != nil {
			return e
		}
		missingReceipt := ref.Kind == "null" || ((ref.Kind == "type" || ref.Kind == "enum" || ref.Kind == "opaque") && ref.Code == "msng")
		if !outgoingAttachmentRef(ref) && !missingReceipt {
			return fmt.Errorf("attachment insertion returned an unsupported receipt (descriptor kind=%q code=%q)", ref.Kind, ref.Code)
		}
		inserted++
	}
	expected := outgoingExpectedNames(paths)
	var readinessErr error
	for i := 0; i < 50; i++ {
		names, e := n.outgoingAttachmentNames(object, false)
		readinessErr = e
		unavailable := nativeMissing(e) || errors.Is(e, errOutgoingAttachmentGetterUnsupported)
		if e != nil && !unavailable && !errors.Is(e, errOutgoingAttachmentPending) {
			return e
		}
		count, ce := n.outgoingAttachmentCount(object)
		if ce != nil {
			return ce
		}
		if count > int64(inserted) {
			return fmt.Errorf("outgoing contains unrequested attachments: expected %d, found %d", inserted, count)
		}
		// Current Mail can expose the rich-text element while its atfn getter
		// remains unsupported. Its public exact count from an empty baseline,
		// together with acknowledged FileURL insertions, supplies the loading guard.
		if count == int64(inserted) && (unavailable || (e == nil && outgoingEqual(names, expected))) {
			return nil
		}
		if e = n.outgoingPause(); e != nil {
			return e
		}
	}
	if readinessErr != nil {
		return fmt.Errorf("attachments did not finish loading with expected file names and count: %w", readinessErr)
	}
	return fmt.Errorf("attachments did not finish loading with expected file names and count")
}
func (c *Client) composeNativeOutgoing(input OutgoingInput, action string) (*OutgoingResult, error) {
	return c.composeNativeOutgoingOwned(input, action, nil)
}

// A factory returns a newly created outgoing descriptor, transferring ownership
// before field replacement, verification, or cleanup can fail.
func (c *Client) composeNativeOutgoingOwned(input OutgoingInput, action string, create func(*nativeMail, OutgoingInput, string, string) (ae.Value, error)) (*OutgoingResult, error) {
	result := &OutgoingResult{Account: input.Account}
	var owned ae.Value
	attempted, saved := false, false
	step := "account_resolution"
	cleanup := ""
	err := c.withNativeAutomation(0, func(n *nativeMail) (err error) {
		defer func() {
			if err != nil && (owned.Kind == "object" || owned.Kind == "opaque") && !attempted {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(n.ctx), 5*time.Second)
				defer cancel()
				clean := *n
				clean.ctx = ctx
				_, ce := clean.command("core", "clos", map[string]ae.Value{"----": owned, "savo": ae.Enum("no  ")})
				if ce != nil {
					cleanup = ce.Error()
				}
			}
		}()
		var account ae.Value
		err = n.phase("outgoing_resolve", func() error {
			var e error
			account, result.AccountID, result.Sender, e = n.outgoingSender(input)
			return e
		})
		if err != nil {
			return err
		}
		if action == "preview" {
			result.Status = "preview"
			return nil
		}
		// Mail's rich-text view adds a space to a final unterminated
		// paragraph. Supply its terminator explicitly before composition,
		// then retain exact readback checks without trimming user whitespace.
		body := input.Body
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		var box ae.Value
		before := map[string]bool{}
		if action == "save" {
			step = "draft_resolution"
			box, err = n.outgoingDraftMailbox(account)
			if err != nil {
				return err
			}
			v, e := n.get(ae.Property("ID  ", ae.All("mssg", box)))
			if e != nil {
				return e
			}
			ids, e := outgoingList(v, "draft IDs")
			if e != nil {
				return e
			}
			for _, id := range ids {
				if id.Kind != "int" || id.Number <= 0 {
					return fmt.Errorf("draft baseline ID unavailable")
				}
				key := outgoingID(id)
				if before[key] {
					return fmt.Errorf("draft baseline ID is ambiguous")
				}
				before[key] = true
			}
		}
		step = "compose"
		err = n.phase("outgoing_compose", func() error {
			var e error
			if create == nil {
				owned, e = n.command("core", "crel", map[string]ae.Value{"kocl": ae.TypeCode("bcke"), "prdt": ae.Record(map[string]ae.Value{"subj": ae.String(input.Subject), "ctnt": ae.String(body), "sndr": ae.String(result.Sender), "pvis": ae.Bool(false)})})
			} else {
				owned, e = create(n, input, result.Sender, body)
			}
			if e != nil {
				var transport *ae.Error
				if errors.As(e, &transport) && transport.Attempted && (create == nil || n.phaseName == "threaded_create") && owned.Kind != "object" && owned.Kind != "opaque" {
					cleanup = "outgoing creation may have completed; owned object descriptor unavailable"
				}
				return e
			}
			if owned.Kind != "object" && owned.Kind != "opaque" {
				cleanup = "outgoing creation receipt did not identify an owned object"
				return fmt.Errorf("Mail did not return an owned outgoing object")
			}
			if create != nil {
				for code, value := range map[string]ae.Value{"subj": ae.String(input.Subject), "ctnt": ae.String(body), "pvis": ae.Bool(false)} {
					if e = n.set(ae.Property(code, owned), value); e != nil {
						return e
					}
				}
				for _, class := range []string{"trcp", "crcp", "brcp"} {
					v, e := n.get(ae.All(class, owned))
					if e != nil {
						return e
					}
					refs, e := outgoingList(v, "inherited recipients")
					if e != nil {
						return e
					}
					for _, ref := range refs {
						if _, e = n.command("core", "delo", map[string]ae.Value{"----": ref}); e != nil {
							return e
						}
					}
				}
			}
			if e = n.set(ae.Property("sndr", owned), ae.String(result.Sender)); e != nil {
				return e
			}
			for i, class := range []string{"trcp", "crcp", "brcp"} {
				for _, address := range [][]string{input.To, input.Cc, input.Bcc}[i] {
					_, e = n.command("core", "crel", map[string]ae.Value{"kocl": ae.TypeCode(class), "insh": owned, "prdt": ae.Record(map[string]ae.Value{"radd": ae.String(address)})})
					if e != nil {
						return e
					}
				}
			}
			step = "recipient_verification"
			if e = n.outgoingVerify(owned, input, result.AccountID, result.Sender, body, true); e != nil {
				return e
			}
			step = "attachments"
			if e = n.outgoingAttach(owned, input.Attachments); e != nil {
				return e
			}
			if create != nil && len(input.Attachments) == 0 {
				count, e := n.outgoingAttachmentCount(owned)
				if e != nil {
					return e
				}
				if count != 0 {
					return fmt.Errorf("outgoing contains unrequested inherited attachments")
				}
			}
			step = "sender_verification"
			return n.outgoingVerify(owned, input, result.AccountID, result.Sender, body, len(input.Attachments) == 0)
		})
		if err != nil {
			return err
		}
		step = action
		if action == "send" {
			return n.phase("outgoing_send", func() error {
				if e := n.ctx.Err(); e != nil {
					return e
				}
				attempted = true
				v, e := n.command("emsg", "send", map[string]ae.Value{"----": owned})
				if e != nil {
					return e
				}
				if v.Kind != "bool" || !v.Boolean {
					return fmt.Errorf("Mail did not accept send (expected true)")
				}
				result.Submitted = true
				result.Status = "submitted"
				return nil
			})
		}
		err = n.phase("outgoing_save", func() error {
			if e := n.ctx.Err(); e != nil {
				return e
			}
			attempted = true
			if _, e := n.command("core", "save", map[string]ae.Value{"----": owned}); e != nil {
				return e
			}
			saved = true
			_, e := n.command("core", "clos", map[string]ae.Value{"----": owned, "savo": ae.Enum("yes ")})
			return e
		})
		if err != nil {
			return err
		}
		step = "save_verification"
		for i := 0; i < 80; i++ {
			// Read IDs together so polling never reads every preexisting draft.
			// Only new account-scoped IDs receive the strict fingerprint read.
			v, e := n.get(ae.Property("ID  ", ae.All("mssg", box)))
			if e != nil {
				return e
			}
			ids, e := outgoingList(v, "saved draft IDs")
			if e != nil {
				return e
			}
			var candidates []*Message
			seen := map[string]bool{}
			for _, id := range ids {
				if id.Kind != "int" || id.Number <= 0 {
					return fmt.Errorf("saved draft ID unavailable")
				}
				key := outgoingID(id)
				if seen[key] {
					return fmt.Errorf("saved draft ID is ambiguous")
				}
				seen[key] = true
				if before[key] {
					continue
				}
				ref := ae.ByID("mssg", id, box)
				m, e := n.outgoingSnapshotMessage(ref, input.Account, box)
				if e != nil {
					if nativeMissing(e) {
						continue
					}
					return e
				}
				if m.ID != key {
					return fmt.Errorf("saved draft ID readback mismatch")
				}
				names, e := n.outgoingAttachmentNames(ref, true)
				if e != nil {
					return e
				}
				sender, e := draftSenderAddress(m.Sender)
				if e != nil {
					continue
				}
				if m.Subject == input.Subject && outgoingBodyMatches(m.Content, body) && strings.EqualFold(sender, result.Sender) && outgoingEqual(m.ToRecipients, input.To) && outgoingEqual(m.CcRecipients, input.Cc) && outgoingEqual(m.BccRecipients, input.Bcc) && outgoingEqual(names, outgoingExpectedNames(input.Attachments)) {
					candidates = append(candidates, m)
				}
			}
			if len(candidates) > 1 {
				return fmt.Errorf("saved draft identity is ambiguous")
			}
			if len(candidates) == 1 {
				result.Draft = candidates[0]
				result.Status = "saved"
				return nil
			}
			if e = n.outgoingPause(); e != nil {
				return e
			}
		}
		return fmt.Errorf("saved draft could not be verified in target account Drafts")
	})
	if err != nil {
		return result, &MutationError{Operation: action, Phase: step, Account: input.Account, Sender: result.Sender, Cause: err, Attempted: attempted, Uncertain: attempted && !result.Submitted, Submitted: result.Submitted, Saved: saved, Cleanup: cleanup}
	}
	if action == "save" && len(input.Attachments) > 0 {
		if err := c.verifyDraftAttachments(result.Draft, input.Attachments); err != nil {
			return result, &MutationError{Operation: action, Phase: "attachment_verification", Account: input.Account, Sender: result.Sender, Cause: err, Attempted: true, Saved: true, Uncertain: true}
		}
	}
	return result, nil
}
