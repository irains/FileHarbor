package utils

import (
	"context"
	"math"
	"time"
)

// DirectorySize counts regular-file logical bytes, not allocated disk blocks.
type DirectorySize struct {
	Bytes       int64     `json:"bytes"`
	Files       int       `json:"files"`
	Directories int       `json:"directories"`
	ScannedAt   time.Time `json:"scanned_at"`
	ScanSummary
}

func ScanDirectorySize(ctx context.Context, directory string) (DirectorySize, error) {
	result := DirectorySize{ScannedAt: time.Now().UTC()}
	overflow := false
	summary, err := WalkManaged(ctx, directory, true, ScanLimits{100000, 128, 10 * time.Second}, func(entry ScanEntry) bool {
		if entry.Info.IsDir() {
			result.Directories++
			return true
		}
		size := entry.Info.Size()
		if size < 0 || result.Bytes > math.MaxInt64-size {
			overflow = true
			return false
		}
		result.Bytes += size
		result.Files++
		return true
	})
	result.ScanSummary = summary
	if overflow {
		result.Incomplete = true
		result.Reason = "size_overflow"
	}
	return result, err
}
