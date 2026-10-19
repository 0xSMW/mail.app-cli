package mail

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"golang.org/x/net/html"
)

// Mail can save a plain outgoing body in its URL-share HTML wrapper with an
// empty plain alternative. Only this inert wrapper is reconstructible as text;
// arbitrary HTML, even when its visible words match, can carry formatting.
func applePlainAlternativeBody(snapshot *draftSnapshot, source io.Reader, params map[string]string) (string, error) {
	reject := func() (string, error) {
		return "", fmt.Errorf("draft MIME multipart/alternative cannot be reconstructed without losing formatting; send this draft in Mail")
	}
	if len(params) != 1 || params["boundary"] == "" {
		return reject()
	}
	reader := multipart.NewReader(source, params["boundary"])
	var bodies [2]string
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
		if err != nil || kind != want {
			return reject()
		}
		bodies[i], err = plainDraftBody(part.Header, part, partParams)
		if err != nil {
			return reject()
		}
	}
	if _, err := reader.NextRawPart(); err != io.EOF || strings.Trim(bodies[0], "\r\n") != "" {
		return reject()
	}
	text, ok := applePlainWrapperText(bodies[1])
	if !ok || strings.ContainsRune(snapshot.Message.Content, 0) {
		return reject()
	}
	// The leading placeholder and containing paragraphs add edge newlines to
	// Mail's text readback. Keep that exact readback, after proving all actual
	// body text and internal line breaks against the HTML source.
	normalize := func(value string) string {
		return strings.Trim(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	}
	if normalize(text) != normalize(snapshot.Message.Content) {
		return "", fmt.Errorf("draft body differs from its Apple plain-text wrapper source")
	}
	return snapshot.Message.Content, nil
}

func applePlainWrapperText(source string) (string, bool) {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", false
	}
	root := appleWrapperElements(doc)
	if len(root) != 1 || !appleWrapperElement(root[0], "html", nil) {
		return "", false
	}
	parts := appleWrapperElements(root[0])
	if len(parts) != 2 || !appleWrapperElement(parts[0], "head", nil) || len(appleWrapperElements(parts[0])) != 0 || !appleWrapperElement(parts[1], "body", map[string]string{"dir": "auto", "style": "overflow-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space;"}) {
		return "", false
	}
	parts = appleWrapperElements(parts[1])
	if len(parts) != 2 || !appleWrapperElement(parts[0], "div", map[string]string{"class": "Apple-Mail-URLShareUserContentTopClass"}) || !appleWrapperElement(parts[1], "div", map[string]string{"class": "Apple-Mail-URLShareWrapperClass", "style": "position: relative !important;"}) {
		return "", false
	}
	top := appleWrapperElements(parts[0])
	if len(top) != 1 || !appleWrapperElement(top[0], "br", nil) || top[0].FirstChild != nil {
		return "", false
	}
	quote := appleWrapperElements(parts[1])
	if len(quote) != 1 || !appleWrapperElement(quote[0], "blockquote", map[string]string{"type": "cite", "style": "border-left-style: none; color: inherit; padding: inherit; margin: inherit;"}) {
		return "", false
	}
	var text strings.Builder
	for node := quote[0].FirstChild; node != nil; node = node.NextSibling {
		switch {
		case node.Type == html.TextNode:
			text.WriteString(node.Data)
		case appleWrapperElement(node, "br", nil) && node.FirstChild == nil:
			text.WriteByte('\n')
		default:
			return "", false
		}
	}
	return text.String(), true
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
