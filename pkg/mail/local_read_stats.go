package mail

import "sync/atomic"

var localFilesRead atomic.Int64
var localBodyExtractions atomic.Int64
var localBodyNanoseconds atomic.Int64
var localPartialBodies atomic.Int64
var localExtractionErrors atomic.Int64

// LocalReadStats measures disk extraction without retaining message evidence.
type LocalReadStats struct {
	FilesRead        int64   `json:"filesRead"`
	BodyExtractions  int64   `json:"bodyExtractions"`
	BodyMS           float64 `json:"bodyMs"`
	PartialBodies    int64   `json:"partialBodies"`
	ExtractionErrors int64   `json:"extractionErrors"`
}

func LocalReadStatistics() LocalReadStats {
	return LocalReadStats{FilesRead: localFilesRead.Load(), BodyExtractions: localBodyExtractions.Load(), BodyMS: float64(localBodyNanoseconds.Load()) / 1e6, PartialBodies: localPartialBodies.Load(), ExtractionErrors: localExtractionErrors.Load()}
}
