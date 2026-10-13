package mailindex

import "sync/atomic"

var queryCount atomic.Int64
var connectionOpens atomic.Int64
var queryNanoseconds atomic.Int64

// Stats counts only local observations and read-only connection opens; it
// contains no message, account, query text, or filesystem path information.
type Stats struct {
	Queries         int64   `json:"queries"`
	ConnectionOpens int64   `json:"connectionOpens"`
	QueryMS         float64 `json:"queryMs"`
}

func Statistics() Stats {
	return Stats{Queries: queryCount.Load(), ConnectionOpens: connectionOpens.Load(), QueryMS: float64(queryNanoseconds.Load()) / 1e6}
}
