//go:build darwin && cgo

package appleevents

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework CoreServices
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unsafe"
)

func Available() bool { return true }

type nativeReply struct {
	Value            Value   `json:"value"`
	Code             int     `json:"code"`
	Message          string  `json:"message"`
	Attempted        bool    `json:"attempted"`
	Failed           bool    `json:"failed"`
	PermissionMS     float64 `json:"permissionMS"`
	DispatchMS       float64 `json:"dispatchMS"`
	PermissionCached bool    `json:"permissionCached"`
}

func nativeCall(ctx context.Context, input any, send bool, ticks int64) (nativeReply, error) {
	deadline := time.Now().Add(time.Duration(ticks) * time.Second / 60)

	data, err := json.Marshal(input)
	if err != nil {
		return nativeReply{}, err
	}
	payload := C.CString(string(data))
	defer C.free(unsafe.Pointer(payload))
	var mode C.int
	if send {
		mode = 1
	}
	if send {
		if err := ctx.Err(); err != nil {
			return nativeReply{}, err
		}
		remaining := time.Until(deadline)
		ticks = int64(remaining/time.Second)*60 + int64((remaining%time.Second)*60/time.Second)
		if ticks < 1 {
			return nativeReply{Failed: true, Code: -1712, Message: "Apple Event deadline expired before dispatch"}, nil
		}
	}
	var attempted C.int
	result := C.mail_ae_call(payload, mode, C.long(ticks), &attempted)
	if result == nil {
		return nativeReply{Attempted: attempted != 0}, errors.New("native Apple Event bridge could not allocate its response")
	}
	defer C.free(unsafe.Pointer(result))
	var reply nativeReply
	if err := json.Unmarshal([]byte(C.GoString(result)), &reply); err != nil {
		return nativeReply{Attempted: attempted != 0}, err
	}
	reply.Attempted = attempted != 0
	return reply, nil
}

func Send(ctx context.Context, event Event) (Value, Stats, error) {
	start := time.Now()
	stats := Stats{}
	finish := func() { stats.ElapsedMS = float64(time.Since(start)) / float64(time.Millisecond) }
	if err := validateEvent(event); err != nil {
		finish()
		return Value{}, stats, err
	}
	ticks, err := timeoutTicks(ctx, event.Timeout)
	if err != nil {
		finish()
		return Value{}, stats, err
	}
	// The native call retains all ownership until AESendMessage returns. We do
	// not abandon an in-flight call when context cancellation arrives.
	reply, err := nativeCall(ctx, event, true, ticks)
	stats.Attempted = reply.Attempted
	if reply.Attempted {
		stats.Events = 1
	}
	finish()
	if err := replyError(reply, err, ctx.Err()); err != nil {
		return Value{}, stats, err
	}
	return reply.Value, stats, nil
}

// A post-dispatch Go-side failure has no native OSStatus, so Code remains zero.
// It still carries dispatch ownership metadata and the original error cause.
func replyError(reply nativeReply, bridgeErr, contextErr error) error {
	if bridgeErr != nil {
		if reply.Attempted {
			return &Error{Message: bridgeErr.Error(), Attempted: true, Cause: bridgeErr}
		}
		return bridgeErr
	}
	if reply.Failed {
		nativeErr := &Error{Code: reply.Code, Message: reply.Message, Attempted: reply.Attempted}
		if reply.Code == -1712 {
			nativeErr.Cause = context.DeadlineExceeded
		} else {
			nativeErr.Cause = contextErr
		}
		return nativeErr
	}
	if contextErr != nil {
		if reply.Attempted {
			return &Error{Message: contextErr.Error(), Attempted: true, Cause: contextErr}
		}
		return contextErr
	}
	return nil
}

func RoundTrip(value Value) (Value, error) {
	if err := validateValue(value, 0); err != nil {
		return Value{}, err
	}
	reply, err := nativeCall(context.Background(), value, false, 0)
	if err != nil {
		return Value{}, err
	}
	if reply.Failed {
		return Value{}, &Error{Code: reply.Code, Message: reply.Message}
	}
	return reply.Value, nil
}

// clearPermissionCache is an internal diagnostic reset; it never grants access.
func clearPermissionCache() { C.mail_ae_clear_permission_cache() }
