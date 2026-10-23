package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	netmail "net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"golang.org/x/net/html"
)

// MessageLink is a discovered destination, not a verified or visited URL.
// Selected means every MIME alternative ancestor selected this rendering.
type MessageLink struct {
	// URL preserves the decoded destination, resolving relative HTML URLs only with an absolute HTTP base.
	URL string `json:"url"`
	// Text is the anchor label or nearby plain-text context.
	Text string `json:"text,omitempty"`
	// Source is html, plain, or header.
	Source string `json:"source"`
	// MIMEPart is the source MIME tree path when locally available.
	MIMEPart string `json:"mimePart,omitempty"`
	// Selected reports selection through all alternative ancestors.
	Selected bool `json:"selected"`
	// Kind is link, unsubscribe, or preferences; body classifications are candidates.
	Kind string `json:"kind"`
	// Openable permits syntactically usable HTTP, HTTPS, and mailto destinations.
	Openable bool `json:"openable"`
}

// LinkExtractionStatus reports source extraction coverage. Complete means the
// available local MIME was parsed, not that remote or image content was examined.
type LinkExtractionStatus struct {
	Complete bool     `json:"complete"`
	Source   string   `json:"source"`
	Warnings []string `json:"warnings,omitempty"`
}

// MessageUnsubscribe preserves advertised list controls. OneClickAdvertised
// does not verify the sender, endpoint, or successful unsubscribe behavior.
// HeaderStatus distinguishes unavailable headers from available absent controls.
type MessageUnsubscribe struct {
	// ListID preserves the advertised raw List-Id header value.
	ListID              string   `json:"listId,omitempty"`
	ListUnsubscribe     []string `json:"listUnsubscribe"`
	ListUnsubscribePost []string `json:"listUnsubscribePost"`
	OneClickAdvertised  bool     `json:"oneClickAdvertised"`
	HeaderStatus        string   `json:"headerStatus"`
}

func makeMessageLink(raw, label, source, part string) MessageLink {
	raw = strings.TrimSpace(raw)
	kind := "link"
	classify := func(s string) string {
		s = strings.ToLower(s)
		if strings.Contains(s, "unsubscribe") || strings.Contains(s, "opt-out") || strings.Contains(s, "opt out") {
			return "unsubscribe"
		}
		if strings.Contains(s, "preferences") || strings.Contains(s, "manage subscription") {
			return "preferences"
		}
		return "link"
	}
	kind = classify(label)
	if kind == "link" {
		kind = classify(raw)
	}
	u, err := url.Parse(raw)
	openable := err == nil && !strings.ContainsAny(raw, "\r\n\x00") && ((strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "http")) && u.Hostname() != "" || strings.EqualFold(u.Scheme, "mailto") && u.Opaque != "")
	return MessageLink{URL: raw, Text: strings.Join(strings.Fields(label), " "), Source: source, MIMEPart: part, Selected: true, Kind: kind, Openable: openable}
}

var plainURLPattern = regexp.MustCompile(`(?i)(?:https?://|mailto:)[^\s<>"` + "`" + `]+`)

func plainMessageLinks(text, part string) []MessageLink {
	var links []MessageLink
	for _, position := range plainURLPattern.FindAllStringIndex(text, -1) {
		raw := text[position[0]:position[1]]
		delimited := position[0] > 0 && position[1] < len(text) && ((text[position[0]-1] == '<' && text[position[1]] == '>') || (text[position[0]-1] == '"' && text[position[1]] == '"'))
		if position[0] >= 2 && text[position[0]-2:position[0]] == "](" {
			depth := 1
			for i, ch := range raw {
				if ch == '(' {
					depth++
				}
				if ch == ')' {
					depth--
					if depth == 0 {
						raw = raw[:i]
						delimited = true
						break
					}
				}
			}
		}
		if !delimited {
			raw = strings.TrimRight(raw, ".,;")
		}
		for _, pair := range [][2]string{{"(", ")"}, {"[", "]"}, {"{", "}"}} {
			for !delimited && strings.HasSuffix(raw, pair[1]) && strings.Count(raw, pair[1]) > strings.Count(raw, pair[0]) {
				raw = strings.TrimSuffix(raw, pair[1])
			}
		}
		start := strings.LastIndex(text[:position[0]], "\n") + 1
		if position[0]-start > 160 {
			start = position[0] - 160
		}
		context := strings.TrimSpace(text[start:position[0]])
		if context == "" && start > 0 {
			previousEnd := start - 1
			previousStart := strings.LastIndex(text[:previousEnd], "\n") + 1
			if previousEnd-previousStart <= 160 {
				context = strings.TrimSpace(text[previousStart:previousEnd])
			}
		}
		links = append(links, makeMessageLink(raw, context, "plain", part))
	}
	return links
}

func htmlMessageLinks(source, part string) []MessageLink {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return nil
	}
	var links []MessageLink
	var base *url.URL
	var findBase func(*html.Node)
	findBase = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "base" && base == nil {
			for _, a := range n.Attr {
				if a.Key == "href" {
					u, e := url.Parse(strings.TrimSpace(a.Val))
					if e == nil && (strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "http")) && u.Host != "" {
						base = u
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findBase(c)
		}
	}
	findBase(root)
	var label func(*html.Node) string
	label = func(n *html.Node) string {
		if n.Type == html.TextNode {
			return n.Data
		}
		if n.Type == html.ElementNode && htmlHiddenTags[n.Data] {
			return ""
		}
		attrs := map[string]string{}
		for _, a := range n.Attr {
			attrs[a.Key] = a.Val
		}
		if htmlHidesElement(attrs) {
			return ""
		}
		if n.Type == html.ElementNode && n.Data == "img" {
			return attrs["alt"]
		}
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			b.WriteString(label(c))
			if c.Type == html.ElementNode && htmlBlockTags[c.Data] {
				b.WriteByte(' ')
			}
		}
		return b.String()
	}
	// Generic labels inherit only adjacent context inside their nearest block.
	// Stop at another anchor so a neighboring unsubscribe control cannot label
	// an unrelated destination in the same paragraph.
	var containsAnchor func(*html.Node) bool
	containsAnchor = func(n *html.Node) bool {
		if n.Type == html.ElementNode && (n.Data == "a" || n.Data == "area") {
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if containsAnchor(c) {
				return true
			}
		}
		return false
	}
	anchorContext := func(n *html.Node) string {
		prefix := ""
		for current := n; current.Parent != nil; current = current.Parent {
			parent := current.Parent
			if parent.Data == "body" || parent.Data == "html" || parent.Data == "article" || parent.Data == "section" {
				break
			}
			stopped := false
			for sibling := current.PrevSibling; sibling != nil; sibling = sibling.PrevSibling {
				if containsAnchor(sibling) {
					stopped = true
					break
				}
				prefix = label(sibling) + prefix
				if len(prefix) > 240 {
					prefix = prefix[len(prefix)-240:]
					stopped = true
					break
				}
			}
			if stopped || htmlBlockTags[parent.Data] {
				break
			}
		}
		return prefix
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		attrs := map[string]string{}
		for _, a := range n.Attr {
			attrs[a.Key] = a.Val
		}
		if n.Type == html.ElementNode && (htmlHiddenTags[n.Data] || htmlHidesElement(attrs)) {
			return
		}
		if n.Type == html.ElementNode && (n.Data == "a" || n.Data == "area") {
			if href, exists := attrs["href"]; exists && strings.TrimSpace(href) != "" {
				href = strings.TrimSpace(href)
				if u, e := url.Parse(href); e == nil && !u.IsAbs() && base != nil {
					href = base.ResolveReference(u).String()
				}
				text := label(n)
				if strings.TrimSpace(text) == "" {
					text = attrs["aria-label"]
					if text == "" {
						text = attrs["title"]
					}
				}
				link := makeMessageLink(href, text, "html", part)
				switch strings.ToLower(strings.Join(strings.Fields(text), " ")) {
				case "here", "click here", "this link", "click":
					candidate := makeMessageLink(href, anchorContext(n), "html", part)
					link.Kind = candidate.Kind
				}
				links = append(links, link)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return links
}

var unsubscribeDestination = regexp.MustCompile(`<([^<>]+)>`)

func populateUnsubscribe(message *Message, header netmail.Header) {
	u := &MessageUnsubscribe{ListUnsubscribe: []string{}, ListUnsubscribePost: []string{}, HeaderStatus: "available"}
	for name, values := range header {
		if strings.EqualFold(name, "List-Id") {
			u.ListID = strings.Join(values, "\n")
		}
		if strings.EqualFold(name, "List-Unsubscribe") {
			u.ListUnsubscribe = append(u.ListUnsubscribe, values...)
		}
		if strings.EqualFold(name, "List-Unsubscribe-Post") {
			u.ListUnsubscribePost = append(u.ListUnsubscribePost, values...)
		}
	}
	// Repeated enrichment must not duplicate header links.
	filtered := message.Links[:0]
	for _, link := range message.Links {
		if link.Source != "header" {
			filtered = append(filtered, link)
		}
	}
	message.Links = filtered
	for _, raw := range u.ListUnsubscribe {
		for _, match := range unsubscribeDestination.FindAllStringSubmatch(raw, -1) {
			link := makeMessageLink(match[1], "", "header", "")
			link.Kind = "unsubscribe"
			message.Links = append(message.Links, link)
		}
	}
	for _, raw := range u.ListUnsubscribePost {
		if strings.EqualFold(strings.TrimSpace(raw), "List-Unsubscribe=One-Click") {
			u.OneClickAdvertised = true
		}
	}
	message.Unsubscribe = u
}

func populateDiskLinks(message *Message, diagnostics *BodyDiagnostics, err error) {
	if diagnostics == nil {
		return
	}
	headers := []MessageLink{}
	for _, link := range message.Links {
		if link.Source == "header" {
			headers = append(headers, link)
		}
	}
	message.Links = append(headers, diagnostics.links...)
	// Empty visible text is compatible with complete image-only HTML
	// discovery, but unsupported/encrypted bodies cannot establish coverage.
	supportedBody := len(diagnostics.linkRenderableParts) > 0
	message.LinkStatus = &LinkExtractionStatus{Complete: supportedBody && (err == nil || err.Error() == "message file holds no text body") && !diagnostics.PartialFile, Source: "disk"}
	if diagnostics.PartialFile {
		message.LinkStatus.Warnings = append(message.LinkStatus.Warnings, "Local message source is partial; additional links may be missing.")
	}
	if err != nil && !message.LinkStatus.Complete {
		message.LinkStatus.Warnings = append(message.LinkStatus.Warnings, err.Error())
	}
}
func populateMailLinks(message *Message) {
	if message.LinkStatus != nil && message.LinkStatus.Source == "disk" {
		if !message.LinkStatus.Complete && message.ContentSource == BodySourceMail {
			for _, link := range plainMessageLinks(message.Content, "") {
				message.Links = append(message.Links, link)
			}
		}
		return
	}
	headers := []MessageLink{}
	for _, link := range message.Links {
		if link.Source == "header" {
			headers = append(headers, link)
		}
	}
	message.Links = append(headers, plainMessageLinks(message.Content, "")...)
	message.LinkStatus = &LinkExtractionStatus{Source: "mail", Warnings: []string{"Mail-rendered text does not expose HTML destinations; additional links may be missing."}}
	if message.Unsubscribe == nil {
		message.Unsubscribe = &MessageUnsubscribe{ListUnsubscribe: []string{}, ListUnsubscribePost: []string{}, HeaderStatus: "unavailable"}
	}
}

// GetMessageLinksJSON discovers destinations without visiting them. A usable
// partial local message is returned without asking Mail to render its body.
func (c *Client) GetMessageLinksJSON(account, mailbox, id string) (*Message, error) {
	local, _, err := c.getMessageDetailsFromDiskMode(account, mailbox, id, true)
	if err != nil && !isEnvelopeIndexUnavailable(err) {
		return local, err
	}
	if local != nil && local.LinkStatus != nil {
		return local, nil
	}
	// Header-only fallback never requests Mail's rendered body.
	var message *Message
	if c.nativeEnabled() {
		err = c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
			acc, e := n.account(account)
			if e != nil {
				return e
			}
			box, e := n.mailbox(acc, mailbox)
			if e != nil {
				return e
			}
			number, e := strconv.ParseInt(id, 10, 64)
			if e != nil || number <= 0 {
				return fmt.Errorf("invalid message ID: %q", id)
			}
			selector := appleevents.ByTest("mssg", appleevents.Comparison("=   ", appleevents.Property("ID  ", appleevents.Examined()), appleevents.Int(number)), box)
			value, e := n.get(appleevents.Property("ID  ", selector))
			if e != nil {
				return e
			}
			ids, e := nativeList(value, "scoped message IDs")
			if e != nil {
				return e
			}
			found := false
			for _, v := range ids {
				if v.Kind == "int" && v.Number == number {
					found = true
				}
			}
			if !found {
				return notFound("message", id)
			}
			ref, e := n.message(box, id)
			if e != nil {
				return e
			}
			message, e = n.messageMetadata(ref, account, mailbox)
			if e != nil {
				return e
			}
			return n.messageHeaders(ref, message)
		})
	} else {
		script := fmt.Sprintf(`const mail = Application('Mail');
%s
const acc = mail.accounts.byName('%s');
const mbox = %s;
if (mbox === null) throw new Error('mailbox not found');
const requestedId = Number('%s');
if (!mbox.messages.id().some(id => id === requestedId)) throw new Error('message not found in mailbox');
const msg = mbox.messages.byId(requestedId);
JSON.stringify({id:String(msg.id()), headers:msg.allHeaders()});`, jxaMailboxLookupHelper(), escapeJSString(account), jxaMailboxLookupExpression(mailbox), escapeJSString(id))
		var output string
		output, err = c.runJXAWithTimeout(script, defaultAutomationTimeout)
		if err == nil {
			var result struct {
				ID      string `json:"id"`
				Headers string `json:"headers"`
			}
			if err = json.Unmarshal([]byte(output), &result); err == nil {
				message = &Message{ID: result.ID, Account: account, Mailbox: mailbox}
				var header netmail.Header
				header, err = headerTextMessage(result.Headers)
				if err == nil {
					populateUnsubscribe(message, header)
				}
			}
		}
	}
	if err != nil && message == nil && local != nil {
		var timeout *AutomationTimeoutError
		if errors.As(err, &timeout) {
			message = local
		}
	}
	if message != nil {
		populateMailLinks(message)
		message.LinkStatus.Warnings = append(message.LinkStatus.Warnings, "Only headers were requested; body links are unavailable without local MIME source.")
		if err != nil {
			var timeout *AutomationTimeoutError
			if errors.As(err, &timeout) {
				message.LinkStatus.Warnings = append(message.LinkStatus.Warnings, err.Error())
				err = nil
			}
		}
	}
	return message, err
}

// headerTextMessage parses Mail's public header string using the same parser as
// disk reads. The empty body is intentional.
func headerTextMessage(text string) (netmail.Header, error) {
	parsed, err := netmail.ReadMessage(strings.NewReader(text + "\r\n\r\n"))
	if err != nil {
		return nil, fmt.Errorf("parse Mail message headers: %w", err)
	}
	_, _ = io.Copy(io.Discard, parsed.Body)
	return parsed.Header, nil
}
