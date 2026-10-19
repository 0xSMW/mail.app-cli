package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// scopedLocalMessageFile distinguishes an absent scoped member from a member
// whose physical file has not been downloaded. It never follows label names
// when choosing the owning mailbox's on-disk directory.
func (c *Client) scopedLocalMessageFile(account, mailbox, messageID string) (path string, absent bool, err error) {
	id, err := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64)
	if err != nil || id <= 0 {
		return "", false, nil
	}
	_, membership, err := c.resolveLocalMessageScope(account, mailbox)
	if err != nil || membership == "" {
		return "", false, err
	}
	query := fmt.Sprintf("select mb.url as URL from messages m join mailboxes mb on mb.ROWID=m.mailbox where m.ROWID=%d and m.deleted=0 and %s limit 1;", id, membership)
	var rows []struct{ URL string }
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", true, nil
	}
	path, err = messageFilePathForMailbox(id, rows[0].URL)
	if err != nil {
		if denied := c.nativeDiskAccessError(err); denied != nil {
			return "", false, denied
		}
		return "", false, nil
	}
	return path, false, nil
}

func (c *Client) scopedLocalMIME(account, mailbox, messageID string) (*netmail.Message, bool, error) {
	path, absent, err := c.scopedLocalMessageFile(account, mailbox, messageID)
	if err != nil || absent {
		return nil, absent, err
	}
	if path == "" || strings.HasSuffix(path, ".partial.emlx") {
		return nil, false, nil
	}
	rfc, err := readEMLXMessage(path)
	if err != nil {
		return nil, false, c.nativeDiskAccessError(err)
	}
	return rfc, false, nil
}

// getAttachmentsFromDisk handles a complete, scoped MIME file. Attachment
// sizes count decoded payload bytes. Partial files, broken encodings, and
// Mail's external-payload placeholders leave the operation to Mail.
func (c *Client) getAttachmentsFromDisk(account, mailbox, messageID string) ([]Attachment, bool, error) {
	rfc, absent, err := c.scopedLocalMIME(account, mailbox, messageID)
	if err != nil {
		return nil, false, err
	}
	if absent {
		return []Attachment{}, true, nil
	}
	if rfc == nil {
		return nil, false, nil
	}
	attachments := make([]Attachment, 0)
	err = walkLocalAttachments(c.Context(), rfc.Header.Get, rfc.Body, 0, func(attachment Attachment, reader io.Reader) (int64, error) {
		size, err := io.Copy(io.Discard, reader)
		if err == nil {
			attachment.Index = len(attachments)
			attachment.FileSize = int(size)
			attachments = append(attachments, attachment)
		}
		return size, err
	})
	if err != nil {
		if ctxErr := c.Done(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, false, nil
	}
	return attachments, true, nil
}

// saveAttachmentFromDisk stages one exact, unique filename and publishes it
// only after validating the complete MIME tree. A hard link in the destination
// directory makes publication atomic and refuses overwrite, including a
// pre-existing symlink. The index helper uses this same MIME traversal order.
func (c *Client) saveAttachmentFromDisk(account, mailbox, messageID, name, destination string) (savedPath string, handled bool, saveErr error) {
	return c.saveLocalAttachment(account, mailbox, messageID, name, -1, destination)
}

// saveAttachmentFromDiskByIndex uses the same MIME traversal order emitted by
// getAttachmentsFromDisk, including duplicate filenames. It reads and streams
// once, and publishes only after validating the whole MIME tree.
func (c *Client) saveAttachmentFromDiskByIndex(account, mailbox, messageID string, index int, destination string) (string, bool, error) {
	if index < 0 {
		return "", true, fmt.Errorf("attachment index must be non-negative")
	}
	return c.saveLocalAttachment(account, mailbox, messageID, "", index, destination)
}

// saveAttachmentFromDiskSelection also preserves an expected name supplied
// with an index by SaveAttachmentByIndex.
func (c *Client) saveAttachmentFromDiskSelection(account, mailbox, messageID, name string, index int, destination string) (string, bool, error) {
	return c.saveLocalAttachment(account, mailbox, messageID, name, index, destination)
}

func (c *Client) saveLocalAttachment(account, mailbox, messageID, name string, selectionIndex int, destination string) (savedPath string, handled bool, saveErr error) {
	rfc, absent, err := c.scopedLocalMIME(account, mailbox, messageID)
	if err != nil {
		return "", false, err
	}
	if absent {
		return "", true, notFound("message", messageID)
	}
	if rfc == nil {
		return "", false, nil
	}
	if selectionIndex < 0 && (name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00")) {
		return "", true, fmt.Errorf("attachment name must be a filename")
	}
	if strings.TrimSpace(destination) == "" || strings.IndexByte(destination, 0) >= 0 {
		return "", true, fmt.Errorf("attachment destination is required")
	}
	target, err := filepath.Abs(destination)
	if err != nil {
		return "", true, err
	}
	parent := filepath.Dir(target)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return "", true, fmt.Errorf("attachment destination directory does not exist: %s", parent)
	}
	if _, err := os.Lstat(target); err == nil {
		return "", true, fmt.Errorf("attachment destination already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return "", true, err
	}
	staging, err := os.CreateTemp(parent, ".mail-attachment-*")
	if err != nil {
		return "", true, err
	}
	tempPath := staging.Name()
	defer func() {
		_ = staging.Close()
		// Only this helper's existing, absolute temporary staging file may be
		// unlinked. Failure of any validation leaves it recoverable in place.
		if filepath.IsAbs(tempPath) && filepath.Dir(tempPath) == parent && strings.HasPrefix(filepath.Base(tempPath), ".mail-attachment-") {
			if info, err := os.Lstat(tempPath); err == nil && info.Mode().IsRegular() {
				_ = os.Remove(tempPath)
			}
		}
	}()
	matches := 0
	attachmentIndex := 0
	var selectionErr error
	err = walkLocalAttachments(c.Context(), rfc.Header.Get, rfc.Body, 0, func(attachment Attachment, reader io.Reader) (int64, error) {
		currentIndex := attachmentIndex
		attachmentIndex++
		if (selectionIndex < 0 && attachmentNamesEqual(attachment.Name, name)) || (selectionIndex >= 0 && currentIndex == selectionIndex) {
			matches++
			if selectionIndex >= 0 && name != "" && !attachmentNamesEqual(attachment.Name, name) {
				selectionErr = fmt.Errorf("attachment index %d has name %q, expected %q", selectionIndex, attachment.Name, name)
				return io.Copy(io.Discard, reader)
			}
			if attachment.Name == "" || attachment.Name == "." || attachment.Name == ".." || filepath.Base(attachment.Name) != attachment.Name || strings.ContainsAny(attachment.Name, "/\\\x00") {
				selectionErr = fmt.Errorf("attachment name must be a filename")
				return io.Copy(io.Discard, reader)
			}
			if matches == 1 {
				return io.Copy(staging, reader)
			}
		}
		return io.Copy(io.Discard, reader)
	})
	if err != nil {
		if ctxErr := c.Done(); ctxErr != nil {
			return "", true, ctxErr
		}
		var outputErr *os.PathError
		if errors.As(err, &outputErr) {
			return "", true, err
		}
		return "", false, nil
	}
	if matches == 0 {
		if selectionIndex >= 0 {
			return "", true, notFound("attachment", strconv.Itoa(selectionIndex))
		}
		return "", true, notFound("attachment", name)
	}
	if selectionErr != nil {
		return "", true, selectionErr
	}
	if matches != 1 {
		return "", true, fmt.Errorf("attachment name is ambiguous: %s; select its index", name)
	}
	if err := staging.Chmod(0o644); err != nil {
		return "", true, err
	}
	if err := staging.Close(); err != nil {
		return "", true, err
	}
	if err := c.Done(); err != nil {
		return "", true, err
	}
	if err := os.Link(tempPath, target); err != nil {
		return "", true, fmt.Errorf("publish attachment without overwrite: %w", err)
	}
	return target, true, nil
}

type localAttachmentVisitor func(Attachment, io.Reader) (int64, error)

type localContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r localContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func walkLocalAttachments(ctx context.Context, header func(string) string, body io.Reader, depth int, visit localAttachmentVisitor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 8 {
		return fmt.Errorf("message nests MIME parts too deeply")
	}
	contentType := header("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("parse attachment content type: %w", err)
	}
	var disposition string
	var dispositionParams map[string]string
	if raw := header("Content-Disposition"); raw != "" {
		disposition, dispositionParams, err = mime.ParseMediaType(raw)
		if err != nil {
			return fmt.Errorf("parse attachment disposition: %w", err)
		}
	}
	body, supported := localMIMETransferReader(localContextReader{ctx, body}, header("Content-Transfer-Encoding"))
	if !supported {
		return fmt.Errorf("unsupported attachment transfer encoding")
	}
	name := dispositionParams["filename"]
	if name == "" {
		name = params["name"]
	}
	if name != "" || disposition == "attachment" {
		if name == "" {
			return fmt.Errorf("attachment filename is unavailable in cached MIME")
		}
		name, err = headerWordDecoder().DecodeHeader(name)
		if err != nil {
			return fmt.Errorf("decode attachment filename: %w", err)
		}
		bounded := &io.LimitedReader{R: body, N: maxMessageFileSize + 1}
		size, err := visit(Attachment{Name: name, MimeType: mediaType}, bounded)
		if err != nil {
			return err
		}
		if size > maxMessageFileSize {
			return fmt.Errorf("decoded attachment exceeds message limit")
		}
		if raw := header("X-Apple-Content-Length"); raw != "" {
			declared, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil || declared < 0 || declared != size {
				return fmt.Errorf("attachment payload is not fully cached")
			}
		}
		return nil
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		if params["boundary"] == "" {
			return fmt.Errorf("multipart message has no boundary")
		}
		reader := multipart.NewReader(body, params["boundary"])
		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("read attachment MIME part: %w", err)
			}
			if err := walkLocalAttachments(ctx, part.Header.Get, part, depth+1, visit); err != nil {
				part.Close()
				return err
			}
			if err := part.Close(); err != nil {
				return err
			}
		}
	}
	// Drain non-attachment leaves as well so malformed encodings or a truncated
	// closing boundary cannot make an incomplete tree appear complete.
	_, err = io.Copy(io.Discard, io.LimitReader(body, maxMessageFileSize+1))
	return err
}
