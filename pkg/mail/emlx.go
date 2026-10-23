package mail

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/htmlindex"
)

// maxMessageFileSize bounds a body read from disk. A larger message is left
// to Mail.app.
const maxMessageFileSize = 64 << 20

var errNoBodyOnDisk = errors.New("no message file on disk")
var errPartialBodyOnDisk = errors.New("local message file is partial")

// messageFilePath finds the file Mail.app stores a message in. Mail names
// the file after the message's local ID and keeps it under the mailbox that
// owns the row, which for Gmail is All Mail even when the message is listed
// under INBOX or a label.
func (c *Client) messageFilePath(messageID string) (string, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("message ID %q is not numeric", messageID)
	}
	var rows []struct {
		URL string `json:"URL"`
	}
	query := fmt.Sprintf("select mb.url as URL from messages m join mailboxes mb on mb.ROWID = m.mailbox where m.ROWID = %d and m.deleted = 0 limit 1;", id)
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", errNoBodyOnDisk
	}
	return messageFilePathForMailbox(id, rows[0].URL)
}

func messageFilePathForMailbox(id int64, mailboxURL string) (string, error) {
	indexPath, err := mailEnvelopeIndexPath()
	if err != nil {
		return "", err
	}
	mailboxDir, err := mailboxDirectory(filepath.Dir(filepath.Dir(indexPath)), mailboxURL)
	if err != nil {
		return "", err
	}
	// Mail inserts one directory it names itself between the mailbox and its
	// data. The mailbox path can hold glob characters, so list it instead.
	entries, err := os.ReadDir(mailboxDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read mailbox files: %w", err)
		}
		return "", errNoBodyOnDisk
	}
	relative := filepath.Join(messageDataDirectory(id), "Messages")
	for _, suffix := range []string{".emlx", ".partial.emlx"} {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(mailboxDir, entry.Name(), relative, strconv.FormatInt(id, 10)+suffix)
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				return path, nil
			} else if err != nil && os.IsPermission(err) {
				return "", err
			}
		}
	}
	return "", errNoBodyOnDisk
}

func mailboxDirectory(root, mailboxURL string) (string, error) {
	parsed, err := url.Parse(mailboxURL)
	if err != nil || parsed.Host == "" || parsed.Host == "." || parsed.Host == ".." || strings.ContainsAny(parsed.Host, "/\\") {
		return "", fmt.Errorf("mailbox URL has no account: %q", mailboxURL)
	}
	dir := filepath.Join(root, parsed.Host)
	for _, component := range strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/") {
		name, err := url.PathUnescape(component)
		if err != nil || name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
			return "", fmt.Errorf("mailbox URL has an unusable path: %q", mailboxURL)
		}
		dir = filepath.Join(dir, name+".mbox")
	}
	return dir, nil
}

// messageDataDirectory spreads messages across directories by the digits of
// the ID above the thousands, lowest digit first: 256493 is under Data/6/5/2.
func messageDataDirectory(id int64) string {
	dir := "Data"
	for n := id / 1000; n > 0; n /= 10 {
		dir = filepath.Join(dir, strconv.FormatInt(n%10, 10))
	}
	return dir
}

func (c *Client) readMessageBodyFromDiskWithDiagnostics(messageID string) (string, *BodyDiagnostics, error) {
	_, body, diagnostics, err := c.readMessageFromDiskWithDiagnostics(messageID)
	return body, diagnostics, err
}

// readMessageFromDiskWithDiagnostics resolves and opens the local file once.
// The returned RFC envelope remains usable after body extraction consumes Body.
func (c *Client) readMessageFromDiskWithDiagnostics(messageID string) (*mail.Message, string, *BodyDiagnostics, error) {
	path, err := c.messageFilePath(messageID)
	if err != nil {
		return nil, "", nil, err
	}
	message, err := readEMLXMessage(path)
	if err != nil {
		return nil, "", failedEMLXDiagnostics(path, err), err
	}
	body, diagnostics, err := extractEMLXBody(message, path)
	if err == nil && diagnostics.PartialFile {
		err = errPartialBodyOnDisk
	}
	return message, body, diagnostics, err
}

func readEMLXBody(path string) (string, error) {
	text, _, err := readEMLXBodyWithDiagnostics(path)
	return text, err
}

func readEMLXBodyWithDiagnostics(path string) (content string, diagnostics *BodyDiagnostics, extractionErr error) {
	message, err := readEMLXMessage(path)
	if err != nil {
		return "", failedEMLXDiagnostics(path, err), err
	}
	return extractEMLXBody(message, path)
}

// readEMLXMessage bounds both allocation and the RFC message reader. The plist
// following the declared byte count never enters the MIME parser.
func readEMLXMessage(path string) (*mail.Message, error) {
	localFilesRead.Add(1)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("message file is not regular")
	}
	if info.Size() > maxMessageFileSize {
		return nil, fmt.Errorf("message file is larger than %d bytes", maxMessageFileSize)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMessageFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMessageFileSize {
		return nil, fmt.Errorf("message file is larger than %d bytes", maxMessageFileSize)
	}
	// The first line is the byte length of the message that follows it. A
	// property list follows the message.
	newline := bytes.IndexByte(data, '\n')
	if newline < 0 {
		return nil, fmt.Errorf("message file has no length line")
	}
	length, err := strconv.Atoi(strings.TrimSpace(string(data[:newline])))
	if err != nil || length < 0 || length > len(data)-newline-1 {
		return nil, fmt.Errorf("message file has an invalid length line")
	}
	message, err := mail.ReadMessage(bytes.NewReader(data[newline+1 : newline+1+length]))
	if err != nil {
		return nil, fmt.Errorf("parse message file: %w", err)
	}
	return message, nil
}

func firstHeader(header map[string][]string, key string) string {
	for name, values := range header {
		if strings.EqualFold(name, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func mimeBodyTextWithDiagnostics(header func(string) string, body io.Reader, decoded bool, depth int, partPath string, diagnostics *BodyDiagnostics) (string, error) {
	if depth > 8 {
		return "", fmt.Errorf("message nests MIME parts too deeply")
	}
	contentType := header("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("parse content type %q: %w", contentType, err)
	}
	if diagnostics != nil {
		diagnostics.MIMEPartCount++
		if strings.HasPrefix(mediaType, "image/") {
			diagnostics.ImagePartCount++
		}
		if !strings.HasPrefix(mediaType, "multipart/") && mediaType != "text/plain" && mediaType != "text/html" {
			diagnostics.NonTextPartCount++
		}
	}
	if disposition, _, err := mime.ParseMediaType(header("Content-Disposition")); err == nil && disposition == "attachment" {
		if diagnostics != nil {
			diagnostics.AttachmentPartCount++
		}
		return "", nil
	}
	if !decoded {
		body, _ = localMIMETransferReader(body, header("Content-Transfer-Encoding"))
	}
	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		boundary := params["boundary"]
		if boundary == "" {
			return "", fmt.Errorf("multipart message has no boundary")
		}
		reader := multipart.NewReader(body, boundary)
		var plain, rich []string
		var plainAlternatives, richAlternatives []int
		partNumber := 0
		// Link rendering selection is independent of the legacy nonempty-text
		// projection. Even an image-only HTML part is a supported rendering.
		linkChosenPath, linkRichPath, relatedRootPath := "", "", ""
		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", fmt.Errorf("read MIME part: %w", err)
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			partNumber++
			childPath := partPath + "." + strconv.Itoa(partNumber)
			text, err := mimeBodyTextWithDiagnostics(part.Header.Get, part, false, depth+1, childPath, diagnostics)
			if err != nil {
				return "", err
			}
			if diagnostics != nil {
				if mediaType == "multipart/related" {
					if params["start"] == "" && partNumber == 1 || params["start"] != "" && strings.TrimSpace(part.Header.Get("Content-ID")) == strings.TrimSpace(params["start"]) {
						relatedRootPath = childPath
					}
				} else if diagnostics.linkRenderableParts[childPath] {
					linkChosenPath = childPath
					if partType == "text/html" {
						linkRichPath = childPath
					}
				}
			}
			alternativeIndex := -1
			if diagnostics != nil && mediaType == "multipart/alternative" {
				alternativeIndex = len(diagnostics.Alternatives)
				normalized := normalizeBodyText(text)
				alternativeType := partType
				if alternativeType == "" && part.Header.Get("Content-Type") == "" {
					alternativeType = "text/plain"
				}
				diagnostics.Alternatives = append(diagnostics.Alternatives, BodyAlternative{Group: partPath, Part: childPath, MediaType: alternativeType, Text: normalized, ExtractedCharCount: utf8.RuneCountInString(normalized)})
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			if partType == "text/html" {
				rich = append(rich, text)
				richAlternatives = append(richAlternatives, alternativeIndex)
			} else {
				plain = append(plain, text)
				plainAlternatives = append(plainAlternatives, alternativeIndex)
			}
		}
		if diagnostics != nil {
			if mediaType == "multipart/alternative" && linkRichPath != "" {
				linkChosenPath = linkRichPath
			}
			if mediaType == "multipart/related" {
				if diagnostics.linkRenderableParts[relatedRootPath] {
					linkChosenPath = relatedRootPath
				}
			}
			if linkChosenPath != "" {
				diagnostics.linkRenderableParts[partPath] = true
			}
		}
		if mediaType == "multipart/alternative" {
			if diagnostics != nil {
				for i := range diagnostics.links {
					link := &diagnostics.links[i]
					if strings.HasPrefix(link.MIMEPart, partPath+".") && link.MIMEPart != linkChosenPath && !strings.HasPrefix(link.MIMEPart, linkChosenPath+".") {
						link.Selected = false
					}
				}
			}
			// The parts are renderings of one body. Mail.app shows the last
			// one it can display, which is the HTML part when there is one.
			if len(rich) > 0 {
				markSelectedAlternative(diagnostics, richAlternatives[len(richAlternatives)-1])
				return rich[len(rich)-1], nil
			}
			if len(plain) > 0 {
				markSelectedAlternative(diagnostics, plainAlternatives[len(plainAlternatives)-1])
				return plain[len(plain)-1], nil
			}
			return "", nil
		}
		return strings.Join(append(plain, rich...), "\n"), nil
	case mediaType == "text/plain", mediaType == "text/html":
		raw, err := io.ReadAll(body)
		if err != nil {
			return "", fmt.Errorf("decode %s part: %w", mediaType, err)
		}
		text, err := decodeCharset(raw, params["charset"])
		if err != nil {
			return "", err
		}
		if diagnostics != nil {
			if diagnostics.linkRenderableParts == nil {
				diagnostics.linkRenderableParts = make(map[string]bool)
			}
			diagnostics.linkRenderableParts[partPath] = true
		}
		if mediaType == "text/html" {
			if diagnostics != nil {
				diagnostics.links = append(diagnostics.links, htmlMessageLinks(text, partPath)...)
			}
			return htmlToTextWithDiagnostics(text, diagnostics), nil
		}
		if diagnostics != nil {
			diagnostics.links = append(diagnostics.links, plainMessageLinks(text, partPath)...)
		}
		return unquotePlainText(text), nil
	}
	return "", nil
}

// unquotePlainText removes the "> " reply markers, as Mail.app does when it
// renders quoted text.
func unquotePlainText(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		rest := line
		for strings.HasPrefix(rest, ">") {
			rest = strings.TrimPrefix(strings.TrimPrefix(rest, ">"), " ")
		}
		lines[i] = rest
	}
	return strings.Join(lines, "\n")
}

func decodeCharset(raw []byte, charset string) (string, error) {
	charset = strings.ToLower(strings.TrimSpace(charset))
	switch charset {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return strings.ToValidUTF8(string(raw), "�"), nil
	}
	encoding, err := htmlindex.Get(charset)
	if err != nil {
		return "", fmt.Errorf("unsupported charset %q", charset)
	}
	decoded, err := encoding.NewDecoder().Bytes(raw)
	if err != nil {
		return "", fmt.Errorf("decode charset %q: %w", charset, err)
	}
	return string(decoded), nil
}

var htmlBlockTags = map[string]bool{
	"p": true, "div": true, "br": true, "tr": true, "li": true, "table": true, "blockquote": true, "hr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "ul": true, "ol": true, "pre": true,
	"dt": true, "dd": true, "section": true, "article": true, "header": true, "footer": true, "center": true,
}

var htmlHiddenTags = map[string]bool{"script": true, "style": true, "head": true, "title": true}

var htmlVoidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true,
	"link": true, "meta": true, "source": true, "track": true, "wbr": true,
}

var htmlListBullets = []string{"\u2022", "\u25E6", "\u25AA"}

// htmlHidesElement reports an element Mail.app leaves out of the text. That
// is display:none only. Mail.app keeps text that is laid out but invisible,
// such as a visibility:hidden or zero-height preview line.
func htmlHidesElement(attributes map[string]string) bool {
	if _, hidden := attributes["hidden"]; hidden {
		return true
	}
	return htmlStyleValue(attributes["style"], "display") == "none"
}

// htmlStyleValue reads one property from an inline style attribute.
func htmlStyleValue(style, property string) string {
	value := ""
	for _, rule := range strings.Split(style, ";") {
		name, setting, found := strings.Cut(rule, ":")
		if found && strings.EqualFold(strings.TrimSpace(name), property) {
			value = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(setting), "!important")))
		}
	}
	return value
}

// htmlToText keeps the text a reader would see, as Mail.app's rendering
// does. It breaks lines at block elements, marks list items, and leaves out
// elements the markup hides.
func htmlToText(source string) string {
	return htmlToTextWithDiagnostics(source, nil)
}

func htmlToTextWithDiagnostics(source string, diagnostics *BodyDiagnostics) string {
	tokenizer := html.NewTokenizer(strings.NewReader(source))
	var out strings.Builder
	// hiddenAt is the depth of the outermost hidden element, or zero.
	depth, hiddenAt := 0, 0
	type list struct {
		ordered bool
		next    int
		bullet  string
	}
	var lists []list
	// transforms holds the inherited text-transform of each open element.
	transforms := []string{""}
	open := func(tag string, selfClosing bool) {
		// Inventory every opening tag before visibility handling. Hidden markup
		// remains diagnostic evidence and never contributes replacement text.
		if diagnostics != nil {
			if tag == "img" {
				diagnostics.HTMLImageCount++
			}
			switch tag {
			case "svg", "canvas", "video", "audio", "object", "embed", "iframe":
				diagnostics.RichContentElements++
			}
		}
		attributes := map[string]string{}
		for {
			key, value, more := tokenizer.TagAttr()
			if diagnostics != nil {
				if tag == "img" && string(key) == "alt" && strings.TrimSpace(string(value)) != "" {
					diagnostics.HTMLImageAltTexts = append(diagnostics.HTMLImageAltTexts, string(value))
				}
				if string(key) == "style" && htmlStyleValue(string(value), "background-image") != "" && htmlStyleValue(string(value), "background-image") != "none" {
					diagnostics.RichContentElements++
				}
			}
			if len(key) > 0 {
				attributes[string(key)] = string(value)
			}
			if !more {
				break
			}
		}
		void := selfClosing || htmlVoidTags[tag]
		if !void {
			depth++
			transform := transforms[len(transforms)-1]
			if value := htmlStyleValue(attributes["style"], "text-transform"); value != "" {
				transform = value
			}
			transforms = append(transforms, transform)
			if hiddenAt == 0 && (htmlHiddenTags[tag] || htmlHidesElement(attributes)) {
				hiddenAt = depth
			}
		}
		if hiddenAt != 0 {
			return
		}
		if htmlBlockTags[tag] {
			out.WriteByte('\n')
		}
		switch tag {
		case "td", "th":
			out.WriteByte(' ')
		case "ul":
			bullet := htmlListBullets[min(len(lists)+1, len(htmlListBullets))-1]
			switch htmlStyleValue(attributes["style"], "list-style-type") {
			case "disc":
				bullet = htmlListBullets[0]
			case "circle":
				bullet = htmlListBullets[1]
			case "square":
				bullet = htmlListBullets[2]
			}
			lists = append(lists, list{bullet: bullet})
		case "ol":
			lists = append(lists, list{ordered: true, next: 1})
		case "li":
			if len(lists) == 0 {
				break
			}
			current := &lists[len(lists)-1]
			if current.ordered {
				out.WriteString("\t" + strconv.Itoa(current.next) + ".\t")
				current.next++
			} else {
				out.WriteString("\t" + current.bullet + "\t")
			}
		}
	}
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return collapseBlankLines(out.String())
		case html.TextToken:
			if hiddenAt != 0 {
				continue
			}
			// Source line breaks and indentation are not visible; one space
			// stands for each run, and none is added between inline elements.
			// Mail.app renders a zero-width space as a space.
			text := strings.ReplaceAll(string(tokenizer.Text()), "\u200b", " ")
			fields := strings.Fields(text)
			if len(fields) == 0 {
				if text != "" {
					out.WriteByte(' ')
				}
				continue
			}
			switch transforms[len(transforms)-1] {
			case "uppercase":
				text = strings.ToUpper(text)
				fields = strings.Fields(text)
			case "lowercase":
				text = strings.ToLower(text)
				fields = strings.Fields(text)
			}
			if first, _ := utf8.DecodeRuneInString(text); unicode.IsSpace(first) {
				out.WriteByte(' ')
			}
			out.WriteString(strings.Join(fields, " "))
			if last, _ := utf8.DecodeLastRuneInString(text); unicode.IsSpace(last) {
				out.WriteByte(' ')
			}
		case html.StartTagToken:
			name, _ := tokenizer.TagName()
			open(string(name), false)
		case html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			open(string(name), true)
		case html.EndTagToken:
			name, _ := tokenizer.TagName()
			tag := string(name)
			if htmlVoidTags[tag] || depth == 0 {
				continue
			}
			if hiddenAt == 0 {
				if htmlBlockTags[tag] {
					out.WriteByte('\n')
				}
				if (tag == "ul" || tag == "ol") && len(lists) > 0 {
					lists = lists[:len(lists)-1]
				}
			}
			if hiddenAt == depth {
				hiddenAt = 0
			}
			depth--
			transforms = transforms[:len(transforms)-1]
		}
	}
}

func collapseBlankLines(text string) string {
	var lines []string
	blank := true
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank {
				lines = append(lines, "")
			}
			blank = true
			continue
		}
		lines = append(lines, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
