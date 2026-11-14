//go:build darwin && cgo

package mailax

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework ApplicationServices
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"
)

type nativeDriver struct{}
type nativeSession struct {
	handle         C.uintptr_t
	lastDiagnostic string
	lookupDeadline time.Time
}
type axReply struct {
	Code       int             `json:"code"`
	Error      string          `json:"error"`
	Value      bool            `json:"value"`
	Exhausted  bool            `json:"exhausted"`
	Diagnostic json.RawMessage `json:"diagnostic"`
}

func New() Driver { return nativeDriver{} }

// Kept in the native bridge so foreground status handling can be tested without
// querying any applications or opening Mail.
func validFrontPIDRead(frontStatus, pidStatus, pid int) bool {
	return C.mail_ax_front_pid_valid(C.int(frontStatus), C.int(pidStatus), C.int(pid)) != 0
}

func call(input map[string]any, session *nativeSession) (axReply, bool, error) {
	diagnostics := os.Getenv("MAIL_APP_CLI_NATIVE_ARCHIVE_DIAGNOSTICS") == "1"
	if diagnostics {
		input["diagnostic"] = true
	}
	data, err := json.Marshal(input)
	if err != nil {
		return axReply{}, false, err
	}
	request := C.CString(string(data))
	defer C.free(unsafe.Pointer(request))
	var handle C.uintptr_t
	if session != nil {
		handle = session.handle
	}
	var attempted C.int
	output := C.mail_ax_call(request, &handle, &attempted)
	if session != nil {
		session.handle = handle
	}
	if output == nil {
		return axReply{}, attempted != 0, fmt.Errorf("Mail Accessibility bridge returned no response")
	}
	defer C.free(unsafe.Pointer(output))
	var reply axReply
	if err := json.Unmarshal([]byte(C.GoString(output)), &reply); err != nil {
		return reply, attempted != 0, err
	}
	if diagnostics {
		var previous *string
		if session != nil {
			previous = &session.lastDiagnostic
		}
		emitFocusDiagnostic(os.Stderr, reply.Diagnostic, previous)
	}
	if reply.Error != "" {
		return reply, attempted != 0, fmt.Errorf("%s", reply.Error)
	}
	return reply, attempted != 0, nil
}

func (nativeDriver) Trusted() error {
	_, _, err := call(map[string]any{"op": "trusted"}, nil)
	return err
}
func (nativeDriver) Prepare(ctx context.Context, viewerID int64) (Session, error) {
	if viewerID <= 0 {
		return nil, fmt.Errorf("invalid owned Mail viewer ID")
	}
	return prepareOwnedViewer(ctx, func(remaining time.Duration) (Session, bool, error) {
		session := &nativeSession{}
		reply, _, err := call(map[string]any{"op": "prepare", "viewer": viewerID, "budgetMs": max(int64(1), remaining.Milliseconds())}, session)
		session.lookupDeadline = time.Now().Add(15 * time.Second)
		return session, reply.Code == 1, err
	}, time.Now().Add(3*time.Second), 50*time.Millisecond)
}

func (s *nativeSession) SelectNext(ctx context.Context, subject string, beforeStep func() error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	exhausted := errTraversalExhausted
	found, err := pollOwnedRows(ctx, s.lookupDeadline, 50*time.Millisecond, func(remaining time.Duration) (bool, error) {
		return scanAfterGuard(ctx, s.lookupDeadline, beforeStep, func(remaining time.Duration) (bool, error) {
			r, _, err := call(map[string]any{"op": "select", "subject": subject, "budgetMs": max(int64(1), remaining.Milliseconds())}, s)
			if err == nil && r.Exhausted {
				return false, exhausted
			}
			return r.Value, err
		})
	})
	if errors.Is(err, exhausted) {
		return false, nil
	}
	return found, err
}
func (s *nativeSession) Guard() error {
	_, _, err := call(map[string]any{"op": "guard"}, s)
	return err
}
func (s *nativeSession) ValidateArchive() error {
	_, _, err := call(map[string]any{"op": "archive"}, s)
	return err
}
func (s *nativeSession) Press() (bool, error) {
	_, attempted, err := call(map[string]any{"op": "press"}, s)
	return attempted, err
}
func (s *nativeSession) Release() {
	if s.handle != 0 {
		_, _, _ = call(map[string]any{"op": "release"}, s)
	}
}
