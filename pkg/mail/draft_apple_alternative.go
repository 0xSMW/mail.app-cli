package mail

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"
)

// Mail can save a plain outgoing body in its URL-share HTML wrapper with an
// empty plain alternative. Its default paragraph/font form can also refer to
// explicit files inside a related part. Only these exact wrappers are accepted;
// arbitrary HTML, even when its visible words match, can carry formatting.
func applePlainAlternativeBody(snapshot *draftSnapshot, source io.Reader, params map[string]string) (string, []draftMIMEAttachment, error) {
	reject := func() (string, []draftMIMEAttachment, error) {
		return "", nil, fmt.Errorf("draft MIME multipart/alternative cannot be reconstructed without losing formatting; send this draft in Mail")
	}
	if len(params) != 1 || params["boundary"] == "" {
		return reject()
	}
	reader := multipart.NewReader(source, params["boundary"])
	var bodies [2]string
	var attachments []draftMIMEAttachment
	var contentIDs []string
	for i, want := range []string{"text/plain", "text/html"} {
		part, err := reader.NextRawPart()
		if err != nil {
			return reject()
		}
		for key, values := range part.Header {
			if len(values) != 1 || (key != "Content-Type" && key != "Content-Transfer-Encoding") {
				return reject()
			}
		}
		kind, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			return reject()
		}
		if i == 1 && kind == "multipart/related" {
			if part.Header.Get("Content-Transfer-Encoding") != "" {
				return reject()
			}
			bodies[i], attachments, contentIDs, err = appleRelatedDraftParts(part, partParams)
		} else if kind == want {
			bodies[i], err = plainDraftBody(part.Header, part, partParams)
		} else {
			return reject()
		}
		if err != nil {
			return reject()
		}
	}
	if _, err := reader.NextRawPart(); err != io.EOF || strings.Trim(bodies[0], "\r\n") != "" {
		return reject()
	}
	text, referencedIDs, canonicalParagraphs, ok := applePlainWrapperContentLayout(bodies[1])
	if !ok || strings.ContainsRune(snapshot.Message.Content, 0) {
		return reject()
	}
	if len(referencedIDs) != len(contentIDs) {
		return reject()
	}
	// Related MIME siblings may be serialized in a different order from the
	// attachment objects in the HTML. Content-ID, rather than sibling position,
	// links each object to its payload. Both parsers reject duplicate IDs.
	partsByID := make(map[string]draftMIMEAttachment, len(contentIDs))
	for i, id := range contentIDs {
		partsByID[id] = attachments[i]
	}
	orderedAttachments := make([]draftMIMEAttachment, 0, len(referencedIDs))
	for _, id := range referencedIDs {
		attachment, found := partsByID[id]
		if !found {
			return reject()
		}
		orderedAttachments = append(orderedAttachments, attachment)
	}
	// Exact default paragraphs and CID-backed files establish the complete
	// rendering map, including the wrapper LF for nonblank bodies. Recompose
	// canonical paragraph text so wrapper layout cannot become authored text.
	if canonicalParagraphs && len(orderedAttachments) > 0 {
		stored := strings.ReplaceAll(snapshot.Message.Content, "\r\n", "\n")
		rendered, ok := attachmentParagraphReadback(text)
		if ok && stored == rendered {
			return text, orderedAttachments, nil
		}
		// Preserve an exact canonical readback, but do not trim edge blank
		// paragraphs or accept partially rendered separators.
		if stored == strings.ReplaceAll(text, "\r\n", "\n") {
			return snapshot.Message.Content, orderedAttachments, nil
		}
		return "", nil, fmt.Errorf("draft body differs from its Apple plain-text wrapper source")
	}
	// The leading placeholder and containing paragraphs add edge newlines to
	// Mail's text readback. Keep that exact readback, after proving all actual
	// body text and internal line breaks against the HTML source.
	normalize := func(value string) string {
		return strings.Trim(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	}
	if normalize(text) != normalize(snapshot.Message.Content) {
		return "", nil, fmt.Errorf("draft body differs from its Apple plain-text wrapper source")
	}
	return snapshot.Message.Content, orderedAttachments, nil
}

func applePlainWrapperContent(source string) (string, []string, bool) {
	text, ids, _, ok := applePlainWrapperContentLayout(source)
	return text, ids, ok
}

func applePlainWrapperContentLayout(source string) (string, []string, bool, bool) {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", nil, false, false
	}
	root := appleWrapperElements(doc)
	if len(root) != 1 || !appleWrapperElement(root[0], "html", nil) {
		return "", nil, false, false
	}
	parts := appleWrapperElements(root[0])
	if len(parts) != 2 || !appleWrapperElement(parts[0], "head", nil) || len(appleWrapperElements(parts[0])) != 0 || !appleWrapperElement(parts[1], "body", map[string]string{"dir": "auto", "style": "overflow-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space;"}) {
		return "", nil, false, false
	}
	parts = appleWrapperElements(parts[1])
	if len(parts) != 2 || !appleWrapperElement(parts[0], "div", map[string]string{"class": "Apple-Mail-URLShareUserContentTopClass"}) || !appleWrapperElement(parts[1], "div", map[string]string{"class": "Apple-Mail-URLShareWrapperClass", "style": "position: relative !important;"}) {
		return "", nil, false, false
	}
	top := appleWrapperElements(parts[0])
	if len(top) != 1 || !appleWrapperElement(top[0], "br", nil) || top[0].FirstChild != nil {
		return "", nil, false, false
	}
	quote := appleWrapperElements(parts[1])
	if len(quote) != 1 || !appleWrapperElement(quote[0], "blockquote", map[string]string{"type": "cite", "style": "border-left-style: none; color: inherit; padding: inherit; margin: inherit;"}) {
		return "", nil, false, false
	}
	var text strings.Builder
	var contentIDs []string
	paragraphs := false
	blankParagraphs := false
	canonicalParagraphs := true
	unprovenParagraphBreaks := false
	bodyParagraph := false
	for node := quote[0].FirstChild; node != nil; node = node.NextSibling {
		switch {
		case node.Type == html.TextNode:
			if paragraphs && strings.Trim(node.Data, "\r\n") == "" {
				continue
			}
			if len(contentIDs) != 0 || paragraphs {
				return "", nil, false, false
			}
			canonicalParagraphs = false
			text.WriteString(node.Data)
		case appleWrapperElement(node, "br", nil) && node.FirstChild == nil && !paragraphs && len(contentIDs) == 0:
			canonicalParagraphs = false
			text.WriteByte('\n')
		case len(contentIDs) == 0 && (paragraphs || text.Len() == 0) && appleDefaultBlankParagraph(node):
			blankParagraphs = true
			paragraphs = true
			text.WriteByte('\n')
		case appleWrapperElement(node, "p", map[string]string{"style": "margin: 0.0px 0.0px 0.0px 0.0px"}) && len(contentIDs) == 0:
			if !paragraphs && strings.Trim(text.String(), "\r\n") != "" {
				return "", nil, false, false
			}
			paragraph, ok := appleDefaultParagraphText(node)
			if !ok {
				return "", nil, false, false
			}
			paragraphs = true
			if paragraph != "" {
				bodyParagraph = true
			}
			if paragraph == "" || strings.ContainsRune(paragraph, '\n') {
				unprovenParagraphBreaks = true
			}
			text.WriteString(paragraph)
			text.WriteByte('\n')
		case appleWrapperElement(node, "span", map[string]string{"class": "Apple-string-attachment"}):
			id, ok := appleAttachmentObjectID(node)
			if !ok || !paragraphs {
				return "", nil, false, false
			}
			for _, previous := range contentIDs {
				if previous == id {
					return "", nil, false, false
				}
			}
			contentIDs = append(contentIDs, id)
		default:
			return "", nil, false, false
		}
	}
	if blankParagraphs && (!bodyParagraph || len(contentIDs) == 0 || unprovenParagraphBreaks) {
		return "", nil, false, false
	}
	return text.String(), contentIDs, paragraphs && bodyParagraph && canonicalParagraphs && !unprovenParagraphBreaks, true
}

func appleDefaultBlankParagraph(node *html.Node) bool {
	if !appleWrapperElement(node, "p", map[string]string{"style": "margin: 0.0px 0.0px 0.0px 0.0px; font: 12.0px Helvetica; color: #000000; -webkit-text-stroke: #000000; min-height: 14.0px"}) {
		return false
	}
	font := node.FirstChild
	if font == nil || !appleWrapperElement(font, "font", map[string]string{"style": "font-kerning: none"}) || font.FirstChild != nil {
		return false
	}
	br := font.NextSibling
	return br != nil && appleWrapperElement(br, "br", nil) && br.FirstChild == nil && br.NextSibling == nil
}

func appleDefaultParagraphText(paragraph *html.Node) (string, bool) {
	var text strings.Builder
	for font := paragraph.FirstChild; font != nil; font = font.NextSibling {
		validFont := false
		for _, face := range []string{"Helvetica", "PingFang SC", "Apple Color Emoji"} {
			styleFace := face
			if strings.Contains(face, " ") {
				styleFace = "'" + face + "'"
			}
			if appleWrapperElement(font, "font", map[string]string{"face": face, "size": "3", "color": "#000000", "style": "font: 12.0px " + styleFace + "; font-kerning: none; color: #000000; -webkit-text-stroke: 0px #000000"}) {
				validFont = true
				break
			}
		}
		if !validFont {
			return "", false
		}
		for node := font.FirstChild; node != nil; node = node.NextSibling {
			if node.Type == html.TextNode {
				text.WriteString(node.Data)
			} else if appleWrapperElement(node, "br", nil) && node.FirstChild == nil {
				text.WriteByte('\n')
			} else {
				return "", false
			}
		}
	}
	return text.String(), true
}

func appleAttachmentObjectID(span *html.Node) (string, bool) {
	nodes := appleWrapperElements(span)
	if len(nodes) != 1 {
		return "", false
	}
	object := nodes[0]
	attrs := make(map[string]string, len(object.Attr))
	for _, attr := range object.Attr {
		attrs[attr.Key] = attr.Val
	}
	width, err := strconv.Atoi(attrs["width"])
	id := strings.TrimPrefix(attrs["data"], "cid:")
	if err != nil || width < 1 || width > 4096 || !strings.HasPrefix(attrs["data"], "cid:") || !appleAttachmentContentID(id) || !appleWrapperElement(object, "object", map[string]string{"type": "application/x-apple-msg-attachment", "width": attrs["width"], "data": "cid:" + id}) || object.FirstChild != nil {
		return "", false
	}
	return id, true
}

func appleAttachmentContentID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", ch) {
			return false
		}
	}
	return true
}

func appleRelatedDraftParts(source io.Reader, params map[string]string) (string, []draftMIMEAttachment, []string, error) {
	reject := func() (string, []draftMIMEAttachment, []string, error) {
		return "", nil, nil, fmt.Errorf("unsupported Apple draft related content")
	}
	if len(params) != 2 || params["type"] != "text/html" || params["boundary"] == "" {
		return reject()
	}
	reader := multipart.NewReader(source, params["boundary"])
	part, err := reader.NextRawPart()
	if err != nil {
		return reject()
	}
	for key, values := range part.Header {
		if len(values) != 1 || (key != "Content-Type" && key != "Content-Transfer-Encoding") {
			return reject()
		}
	}
	kind, bodyParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || kind != "text/html" {
		return reject()
	}
	body, err := plainDraftBody(part.Header, part, bodyParams)
	if err != nil {
		return reject()
	}
	var attachments []draftMIMEAttachment
	var contentIDs []string
	for {
		part, err = reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return reject()
		}
		for key, values := range part.Header {
			if len(values) != 1 || (key != "Content-Type" && key != "Content-Transfer-Encoding" && key != "Content-Disposition" && key != "Content-Id") {
				return reject()
			}
		}
		kind, fileParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil || strings.HasPrefix(kind, "multipart/") || strings.HasPrefix(kind, "message/") || kind == "application/pkcs7-mime" || kind == "application/pgp-encrypted" {
			return reject()
		}
		for key, value := range fileParams {
			if key != "name" && (key != "x-unix-mode" || value != "0666") {
				return reject()
			}
		}
		disposition, dispositionParams, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		name := dispositionParams["filename"]
		if err != nil || disposition != "attachment" || len(dispositionParams) != 1 || name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, 0) {
			return reject()
		}
		if encodedName, present := fileParams["name"]; present {
			decodedName, err := new(mime.WordDecoder).DecodeHeader(encodedName)
			if err != nil || norm.NFC.String(decodedName) != norm.NFC.String(name) {
				return reject()
			}
		}
		headerID := part.Header.Get("Content-Id")
		id := strings.TrimSuffix(strings.TrimPrefix(headerID, "<"), ">")
		if headerID != "<"+id+">" || !appleAttachmentContentID(id) {
			return reject()
		}
		for _, previous := range contentIDs {
			if previous == id {
				return reject()
			}
		}
		data, err := decodeDraftPart(part.Header, part)
		if err != nil {
			return reject()
		}
		// Keep bytes verbatim, including an application/macbinary payload.
		// The caller compares them against Mail's exported file before any
		// replacement; a real resource-fork wrapper cannot be silently stripped.
		attachments = append(attachments, draftMIMEAttachment{name, data})
		contentIDs = append(contentIDs, id)
	}
	if len(attachments) == 0 {
		return reject()
	}
	return body, attachments, contentIDs, nil
}

// Non-element content is permitted only as structural line breaks. Returning
// the node as-is on any other content makes each exact shape check fail.
func appleWrapperElements(parent *html.Node) []*html.Node {
	var nodes []*html.Node
	for node := parent.FirstChild; node != nil; node = node.NextSibling {
		if node.Type == html.TextNode && strings.Trim(node.Data, "\r\n") == "" {
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func appleWrapperElement(node *html.Node, name string, attrs map[string]string) bool {
	if node.Type != html.ElementNode || node.Namespace != "" || node.Data != name || len(node.Attr) != len(attrs) {
		return false
	}
	seen := make(map[string]bool, len(node.Attr))
	for _, attr := range node.Attr {
		value, ok := attrs[attr.Key]
		if !ok || attr.Namespace != "" || seen[attr.Key] || value != attr.Val {
			return false
		}
		seen[attr.Key] = true
	}
	return true
}
