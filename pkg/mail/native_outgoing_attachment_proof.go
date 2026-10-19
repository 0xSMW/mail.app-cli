package mail

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"golang.org/x/text/unicode/norm"
)

type outgoingAttachmentProof struct {
	name string
	hash [sha256.Size]byte
}

func hashOutgoingAttachmentInputs(ctx context.Context, paths []string) ([]outgoingAttachmentProof, error) {
	proofs := make([]outgoingAttachmentProof, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, localContextReader{ctx, file})
		closeErr := file.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		proof := outgoingAttachmentProof{name: norm.NFC.String(filepath.Base(path))}
		copy(proof.hash[:], hash.Sum(nil))
		proofs = append(proofs, proof)
	}
	return proofs, nil
}

func proveOutgoingMIMEAttachments(ctx context.Context, source string, expected []outgoingAttachmentProof) error {
	parsed, err := mail.ReadMessage(strings.NewReader(source))
	if err != nil {
		return fmt.Errorf("read saved attachment MIME: %w", err)
	}
	remaining := make(map[outgoingAttachmentProof]int, len(expected))
	for _, proof := range expected {
		remaining[proof]++
	}
	err = walkLocalAttachmentsLimit(ctx, parsed.Header.Get, parsed.Body, 0, int64(len(source)), func(attachment Attachment, reader io.Reader) (int64, error) {
		hash := sha256.New()
		size, err := io.Copy(hash, reader)
		if err != nil {
			return size, err
		}
		proof := outgoingAttachmentProof{name: norm.NFC.String(attachment.Name)}
		copy(proof.hash[:], hash.Sum(nil))
		if remaining[proof] == 0 {
			return size, fmt.Errorf("saved attachment payload differs from requested files")
		}
		remaining[proof]--
		return size, nil
	})
	if err != nil {
		return fmt.Errorf("verify saved attachment MIME: %w", err)
	}
	for _, count := range remaining {
		if count != 0 {
			return fmt.Errorf("saved attachment MIME is missing requested payloads")
		}
	}
	return nil
}

// Save and inspect this same owned composition before dispatch. Rich-text
// element counts and filenames are insertion evidence, never payload proof.
func (r *nativeOutgoingRun) proveAttachmentReadiness() error {
	r.step = "attachment_payload_snapshot"
	original, err := r.n.ownedComposeSnapshot(r.owned)
	if err != nil {
		return err
	}
	r.step = "attachment_payload_save"
	if err := r.n.phase("outgoing_attachment_payload_save", func() error {
		_, err := r.n.command("core", "save", map[string]ae.Value{"----": r.owned})
		return err
	}); err != nil {
		return err
	}
	r.saved = true
	r.step = "attachment_payload_identity"
	if err := r.verifySavedDraft(); err != nil {
		return err
	}
	draft := r.result.Draft
	id, err := strconv.ParseInt(draft.ID, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("saved attachment draft ID unavailable")
	}
	ref := ae.ByID("mssg", ae.Int(id), r.box)
	r.step = "attachment_payload_verification"
	return r.n.phase("outgoing_attachment_payload_verification", func() error {
		before, err := r.n.outgoingSnapshotMessage(ref, r.input.Account, r.box)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(before, draft) {
			return fmt.Errorf("saved attachment draft changed before payload verification")
		}
		source, err := r.n.get(ae.Property("raso", ref))
		if err != nil {
			return err
		}
		raw, err := nativeString(source, "saved attachment raw MIME")
		if err != nil {
			return err
		}
		if err = proveOutgoingMIMEAttachments(r.n.ctx, raw, r.attachmentProofs); err != nil {
			return err
		}
		current, err := r.n.outgoingSnapshotMessage(ref, r.input.Account, r.box)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(before, current) {
			return fmt.Errorf("saved attachment draft changed during payload verification")
		}
		inputs, err := hashOutgoingAttachmentInputs(r.n.ctx, r.input.Attachments)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(inputs, r.attachmentProofs) {
			return fmt.Errorf("attachment input files changed during composition")
		}
		if err = r.n.outgoingVerify(r.owned, r.input, r.result.AccountID, r.result.Sender, r.body, false); err != nil {
			return err
		}
		currentOwned, err := r.n.ownedComposeSnapshot(r.owned)
		if err != nil {
			return err
		}
		if _, available := original["files"]; !available {
			delete(currentOwned, "files")
		}
		if !reflect.DeepEqual(original, currentOwned) {
			return fmt.Errorf("owned composition changed during attachment payload verification")
		}
		r.result.Draft = nil
		r.result.Status = ""
		return nil
	})
}
