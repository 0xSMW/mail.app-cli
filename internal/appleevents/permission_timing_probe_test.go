//go:build darwin && cgo

package appleevents

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"
)

func TestReadOnlyPermissionTimingProbe(t *testing.T) {
	if os.Getenv("MAIL_AE_PERMISSION_TIMING_PROBE") != "1" {
		t.Skip("read-only permission timing probe disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	event := Event{Class: "core", ID: "getd", Params: map[string]Value{"----": Property("vers", Null())}}
	uncachedPermission, uncachedDispatch, uncachedTotal := []float64{}, []float64{}, []float64{}
	cachedPermission, cachedDispatch, cachedTotal := []float64{}, []float64{}, []float64{}
	call := func() (nativeReply, float64) {
		t.Helper()
		start := time.Now()
		reply, err := nativeCall(ctx, event, true, 300)
		elapsed := float64(time.Since(start)) / float64(time.Millisecond)
		if err != nil {
			t.Fatalf("version timing bridge failed: errorType=%T", err)
		}
		if reply.Failed || !reply.Attempted || reply.Value.Kind != "string" {
			t.Fatalf("version timing failed: nativeCode=%d attempted=%t kind=%s", reply.Code, reply.Attempted, reply.Value.Kind)
		}
		return reply, elapsed
	}
	clearPermissionCache()
	warmup, warmupTotal := call()
	t.Logf("warmup permissionCached=%t permissionMS=%.3f dispatchMS=%.3f totalMS=%.3f", warmup.PermissionCached, warmup.PermissionMS, warmup.DispatchMS, warmupTotal)
	for i := 0; i < 20; i++ {
		clearPermissionCache()
		uncached, uncachedElapsed := call()
		cached, cachedElapsed := call()
		if uncached.PermissionCached || !cached.PermissionCached {
			t.Fatal("permission success cache did not respect reset and reuse")
		}
		uncachedPermission = append(uncachedPermission, uncached.PermissionMS)
		uncachedDispatch = append(uncachedDispatch, uncached.DispatchMS)
		uncachedTotal = append(uncachedTotal, uncachedElapsed)
		cachedPermission = append(cachedPermission, cached.PermissionMS)
		cachedDispatch = append(cachedDispatch, cached.DispatchMS)
		cachedTotal = append(cachedTotal, cachedElapsed)
		t.Logf("sample=%d uncachedPermissionMS=%.3f uncachedDispatchMS=%.3f uncachedTotalMS=%.3f cachedPermissionMS=%.3f cachedDispatchMS=%.3f cachedTotalMS=%.3f", i, uncached.PermissionMS, uncached.DispatchMS, uncachedElapsed, cached.PermissionMS, cached.DispatchMS, cachedElapsed)
	}
	median := func(values []float64) float64 {
		sort.Float64s(values)
		return (values[len(values)/2-1] + values[len(values)/2]) / 2
	}
	uncachedMedian, cachedMedian := median(uncachedTotal), median(cachedTotal)
	t.Logf("pairedSamples=20 uncachedPermissionMedianMS=%.3f uncachedDispatchMedianMS=%.3f uncachedTotalMedianMS=%.3f cachedPermissionMedianMS=%.3f cachedDispatchMedianMS=%.3f cachedTotalMedianMS=%.3f totalImprovementPercent=%.2f", median(uncachedPermission), median(uncachedDispatch), uncachedMedian, median(cachedPermission), median(cachedDispatch), cachedMedian, (uncachedMedian-cachedMedian)/uncachedMedian*100)
}
