package mail

import (
	"sync/atomic"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

var nativeEventCount atomic.Int64
var nativeEventNanoseconds atomic.Int64
var nativeContentRequestCount atomic.Int64
var legacyProcessCount atomic.Int64
var legacyProcessNanoseconds atomic.Int64
var legacySQLiteProcessCount atomic.Int64
var legacySQLiteProcessNanoseconds atomic.Int64

type BackendStats struct {
	NativeEvents          int64   `json:"nativeEvents"`
	NativeEventMS         float64 `json:"nativeEventMs"`
	NativeContentRequests int64   `json:"nativeContentRequests"`
	LegacyProcesses       int64   `json:"legacyProcesses"`
	LegacyProcessMS       float64 `json:"legacyProcessMs"`
	LegacySQLiteProcesses int64   `json:"legacySQLiteProcesses"`
	LegacySQLiteProcessMS float64 `json:"legacySQLiteProcessMs"`
}

func recordNativeEvent(s appleevents.Stats) {
	nativeEventCount.Add(s.Events)
	nativeEventNanoseconds.Add(int64(s.ElapsedMS * 1e6))
}
func recordLegacyProcess(elapsed time.Duration) {
	legacyProcessCount.Add(1)
	legacyProcessNanoseconds.Add(int64(elapsed))
}
func recordLegacySQLiteProcess(elapsed time.Duration) {
	legacySQLiteProcessCount.Add(1)
	legacySQLiteProcessNanoseconds.Add(int64(elapsed))
}
func BackendStatistics() BackendStats {
	return BackendStats{NativeContentRequests: nativeContentRequestCount.Load(), LegacySQLiteProcesses: legacySQLiteProcessCount.Load(), LegacySQLiteProcessMS: float64(legacySQLiteProcessNanoseconds.Load()) / 1e6, NativeEvents: nativeEventCount.Load(), NativeEventMS: float64(nativeEventNanoseconds.Load()) / 1e6, LegacyProcesses: legacyProcessCount.Load(), LegacyProcessMS: float64(legacyProcessNanoseconds.Load()) / 1e6}
}
