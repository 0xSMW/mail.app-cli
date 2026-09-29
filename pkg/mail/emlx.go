package mail

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
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
	indexPath, err := mailEnvelopeIndexPath()
	if err != nil {
		return "", err
	}
	mailboxDir, err := mailboxDirectory(filepath.Dir(filepath.Dir(indexPath)), rows[0].URL)
	if err != nil {
		return "", err
	}
	// Mail inserts one directory it names itself between the mailbox and its
	// data. The mailbox path can hold glob characters, so list it instead.
	entries, err := os.ReadDir(mailboxDir)
	if err != nil {
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
			}
		}
	}
	return "", errNoBodyOnDisk
}

func mailboxDirectory(root, mailboxURL string) (string, error) {
	parsed, err := url.Parse(mailboxURL)
	if err != nil || parsed.Host == "" {
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

// readMessageBodyFromDisk returns the message text without asking Mail.app
// for it. It fails when the file is missing or when it cannot decode a text
// body, and the caller then asks Mail.app instead.
func (c *Client) readMessageBodyFromDisk(messageID string) (string, error) {
	path, err := c.messageFilePath(messageID)
	if err != nil {
		return "", err
	}
	return readEMLXBody(path)
}

func readEMLXBody(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxMessageFileSize {
		return "", fmt.Errorf("message file is larger than %d bytes", maxMessageFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// The first line is the byte length of the message that follows it. A
	// property list follows the message.
	newline := bytes.IndexByte(data, '\n')
	if newline < 0 {
		return "", fmt.Errorf("message file has no length line")
	}
	length, err := strconv.Atoi(strings.TrimSpace(string(data[:newline])))
	if err != nil || length < 0 || newline+1+length > len(data) {
		return "", fmt.Errorf("message file has an invalid length line")
	}
	message, err := mail.ReadMessage(bytes.NewReader(data[newline+1 : newline+1+length]))
	if err != nil {
		return "", fmt.Errorf("parse message file: %w", err)
	}
	header := map[string][]string(message.Header)
	text, err := mimeBodyText(func(key string) string { return firstHeader(header, key) }, message.Body, false, 0)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return "", fmt.Errorf("message file holds no text body")
	}
	return text, nil
}

func firstHeader(header map[string][]string, key string) string {
	for name, values := range header {
		if strings.EqualFold(name, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

// mimeBodyText returns the readable text of one MIME entity. decoded is true
// when the reader already removed the transfer encoding.
func mimeBodyText(header func(string) string, body io.Reader, decoded bool, depth int) (string, error) {
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
	if disposition, _, err := mime.ParseMediaType(header("Content-Disposition")); err == nil && disposition == "attachment" {
		return "", nil
	}
	if !decoded {
		switch strings.ToLower(strings.TrimSpace(header("Content-Transfer-Encoding"))) {
		case "base64":
			body = base64.NewDecoder(base64.StdEncoding, newlineStripper{bufio.NewReader(body)})
		case "quoted-printable":
			body = quotedprintable.NewReader(body)
		}
	}
	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		boundary := params["boundary"]
		if boundary == "" {
			return "", fmt.Errorf("multipart message has no boundary")
		}
		reader := multipart.NewReader(body, boundary)
		var plain, rich []string
		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", fmt.Errorf("read MIME part: %w", err)
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			text, err := mimeBodyText(part.Header.Get, part, false, depth+1)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			if partType == "text/html" {
				rich = append(rich, text)
			} else {
				plain = append(plain, text)
			}
		}
		if mediaType == "multipart/alternative" {
			// The parts are renderings of one body. Mail.app shows the last
			// one it can display, which is the HTML part when there is one.
			if len(rich) > 0 {
				return rich[len(rich)-1], nil
			}
			if len(plain) > 0 {
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
		if mediaType == "text/html" {
			return htmlToText(text), nil
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

// newlineStripper drops line breaks, which base64 bodies carry every 76
// characters and the decoder rejects.
type newlineStripper struct{ reader io.Reader }

func (s newlineStripper) Read(p []byte) (int, error) {
	for {
		n, err := s.reader.Read(p)
		kept := 0
		for _, b := range p[:n] {
			if b != '\r' && b != '\n' {
				p[kept] = b
				kept++
			}
		}
		if kept > 0 || err != nil {
			return kept, err
		}
	}
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
		attributes := map[string]string{}
		for {
			key, value, more := tokenizer.TagAttr()
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
