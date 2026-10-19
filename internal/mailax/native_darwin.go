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
	"fmt"
	"os"
	"time"
	"unsafe"
)

type nativeDriver struct{}
type nativeSession struct {
	handle         C.uintptr_t
	lastDiagnostic string
}
type axReply struct {
	Code       int             `json:"code"`
	Error      string          `json:"error"`
	Text       string          `json:"text"`
	Value      bool            `json:"value"`
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
func (nativeDriver) FrontApplication() (string, error) {
	r, _, err := call(map[string]any{"op": "front"}, nil)
	return r.Text, err
}
func (nativeDriver) Restore(bundle string) error {
	_, _, err := call(map[string]any{"op": "restore", "bundle": bundle}, nil)
	return err
}
func (nativeDriver) OwnedFocus(viewerID int64) (bool, error) {
	r, _, err := call(map[string]any{"op": "focus", "viewer": viewerID}, nil)
	return r.Value, err
}
func (nativeDriver) Prepare(ctx context.Context, viewerID int64) (Session, error) {
	if viewerID <= 0 {
		return nil, fmt.Errorf("invalid owned Mail viewer ID")
	}
	deadline := time.Now().Add(3 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok {
		deadline = minTime(deadline, contextDeadline)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s := &nativeSession{}
		r, _, err := call(map[string]any{"op": "prepare", "viewer": viewerID, "budgetMs": max(int64(1), time.Until(deadline).Milliseconds())}, s)
		if err == nil {
			focusDeadline := minTime(deadline, time.Now().Add(2*time.Second))
			err = prepareOwnedFocus(ctx, func() (bool, error) {
				remaining := time.Until(focusDeadline)
				if remaining <= 0 {
					return false, nil
				}
				reply, _, err := call(map[string]any{"op": "focusowned", "budgetMs": max(int64(1), remaining.Milliseconds())}, s)
				return reply.Value, err
			}, func() (bool, error) {
				remaining := time.Until(focusDeadline)
				if remaining <= 0 {
					return false, nil
				}
				reply, _, err := call(map[string]any{"op": "ownedfocus", "budgetMs": min(remaining.Milliseconds(), int64(500))}, s)
				return reply.Value, err
			}, focusDeadline, 50*time.Millisecond)
			if err != nil {
				s.Release()
				return nil, err
			}
			return s, nil
		}
		s.Release()
		if r.Code != 1 || !time.Now().Before(deadline) {
			return nil, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (s *nativeSession) SelectNext(ctx context.Context, subject string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r, _, err := call(map[string]any{"op": "select", "subject": subject}, s)
	return r.Value, err
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
func (s *nativeSession) OwnedFocus() (bool, error) {
	r, _, err := call(map[string]any{"op": "ownedfocus"}, s)
	return r.Value, err
}
func (s *nativeSession) Release() {
	if s.handle != 0 {
		_, _, _ = call(map[string]any{"op": "release"}, s)
	}
}
