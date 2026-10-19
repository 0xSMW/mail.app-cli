package mail

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"golang.org/x/text/unicode/norm"
)

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
	return nativeList(v, "accounts")
}

// Sender guards read only their four required public columns. IDs are read
// before and after the projection so an account reorder cannot associate an
// address with a different account. Each guard performs its own fresh read.
func (n *nativeMail) outgoingSender(input OutgoingInput) (ae.Value, string, string, error) {
	projection, e := n.projectAccounts(nativeSenderAccounts, "ID  ", "pnam", "isac", "emad")
	if e != nil {
		return ae.Value{}, "", "", e
	}
	return selectOutgoingSender(projection, input)
}

// Selection and ownership validation operate only on this fresh observation.
func selectOutgoingSender(projection *nativeAccountProjection, input OutgoingInput) (ae.Value, string, string, error) {
	all := make([]map[string]ae.Value, len(projection.finalIDs))
	seen := make(map[string]bool, len(projection.finalIDs))
	var fields map[string]ae.Value
	matches := 0
	for i := range projection.finalIDs {
		row, e := projection.row(i, seen)
		if e != nil {
			return ae.Value{}, "", "", e
		}
		name, e := nativeString(row["pnam"], "account name")
		if e != nil {
			return ae.Value{}, "", "", e
		}
		if row["isac"].Kind != "bool" {
			return ae.Value{}, "", "", fmt.Errorf("account enabled state unavailable")
		}
		addresses, e := nativeList(row["emad"], "account sender addresses")
		if e != nil {
			return ae.Value{}, "", "", e
		}
		for _, address := range addresses {
			if _, e = nativeString(address, "account sender address"); e != nil {
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
	requested, e := draftSenderAddress(requested)
	if e != nil {
		return ae.Value{}, "", "", e
	}
	chosen := ""
	count := 0
	for _, v := range addresses {
		s, e := nativeString(v, "sender address")
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
		list, e := nativeList(f["emad"], "account sender ownership")
		if e != nil {
			return ae.Value{}, "", "", e
		}
		owned := false
		for _, v := range list {
			s, e := nativeString(v, "account sender address")
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
	return n.recipientAddresses(object, class, false)
}
func outgoingEqual(a, b []string) bool {
	return slices.Equal(a, b)
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
	value, e := nativeString(fields["sndr"], "sender readback")
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
	value, e = nativeString(fields["subj"], "subject readback")
	if e != nil {
		return e
	}
	if value != input.Subject {
		return fmt.Errorf("outgoing subject differs from requested subject")
	}
	if checkBody {
		value, e = nativeString(fields["ctnt"], "body readback")
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
	list, e := nativeList(v, "attachment names")
	if e != nil {
		return nil, e
	}
	names := make([]string, 0, len(list))
	for _, v := range list {
		var name string
		if stored {
			name, e = nativeString(v, "attachment name")
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
	// from an accepted rich-text make; count proves insertion, not payload readiness.
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
		// together with acknowledged FileURL insertions, supplies insertion evidence.
		// Send additionally verifies persisted MIME attachment payloads.
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

// A factory transfers the newly created descriptor before replacement can fail.
func (c *Client) composeNativeOutgoingOwned(input OutgoingInput, action string, create outgoingFactory) (*OutgoingResult, error) {
	r := &nativeOutgoingRun{input: input, action: action, create: create, result: &OutgoingResult{Account: input.Account}, step: "account_resolution", before: map[string]bool{}}
	err := c.withNativeAutomation(0, func(n *nativeMail) (err error) {
		r.n = n
		defer func() { r.cleanupOwned(err) }()
		var account ae.Value
		err = n.phase("outgoing_resolve", func() error {
			var e error
			account, r.result.AccountID, r.result.Sender, e = n.outgoingSender(input)
			return e
		})
		if err != nil {
			return err
		}
		if action == "preview" {
			r.result.Status = "preview"
			return nil
		}
		// Explicitly terminate Mail's final rich-text paragraph without trimming.
		r.body = input.Body
		if !strings.HasSuffix(r.body, "\n") {
			r.body += "\n"
		}
		if action == "send" {
			r.step = "attachment_input_hash"
			err = n.phase("outgoing_attachment_input_hash", func() error {
				var hashErr error
				r.attachmentProofs, hashErr = hashOutgoingAttachmentInputs(n.ctx, input.Attachments)
				return hashErr
			})
			if err != nil {
				return err
			}
		}
		if action == "save" || action == "send" {
			r.step = "draft_resolution"
			if err = r.draftBaseline(account); err != nil {
				return err
			}
		}
		r.step = "compose"
		if err = n.phase("outgoing_compose", r.compose); err != nil {
			return err
		}
		if action == "send" {
			if err = r.prepareSendDraft(); err != nil {
				return err
			}
		}
		r.step = action
		if err = r.dispatch(); err != nil || action == "send" {
			return err
		}
		r.step = "save_verification"
		return r.verifySavedDraft()
	})
	if err != nil {
		return r.result, &MutationError{Operation: action, Phase: r.step, Account: input.Account, Sender: r.result.Sender, Cause: err, Attempted: r.attempted, Uncertain: r.attempted && !r.result.Submitted, Submitted: r.result.Submitted, Saved: r.saved, Cleanup: r.cleanup}
	}
	if action == "save" && len(input.Attachments) > 0 {
		if err := c.verifyDraftAttachments(r.result.Draft, input.Attachments); err != nil {
			return r.result, &MutationError{Operation: action, Phase: "attachment_verification", Account: input.Account, Sender: r.result.Sender, Cause: err, Attempted: true, Saved: true, Uncertain: true}
		}
	}
	return r.result, nil
}
