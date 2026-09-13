package utils

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"time"
)

// ScanLimits bounds metadata work independently of file payload sizes.
type ScanLimits struct {
	Entries  int
	Depth    int
	Duration time.Duration
}

type ScanSummary struct {
	Visited    int    `json:"visited"`
	Skipped    int    `json:"skipped"`
	Incomplete bool   `json:"incomplete"`
	Reason     string `json:"reason,omitempty"`
}

type ScanEntry struct {
	Path string
	Info os.FileInfo
}

var errScanStopped = errors.New("scan stopped")

// WalkManaged visits descendants without using the truncated browser listing.
// The visitor returns false to stop with an explicitly incomplete result.
func WalkManaged(ctx context.Context, directory string, recursive bool, limits ScanLimits, visit func(ScanEntry) bool) (ScanSummary, error) {
	summary := ScanSummary{}
	if ctx == nil {
		ctx = context.Background()
	}
	if limits.Entries <= 0 || limits.Depth <= 0 || limits.Duration <= 0 {
		return summary, ErrInvalidPath
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Duration)
	defer cancel()
	_, relative, _, err := ResolveDirectory(directory, true)
	if err != nil {
		return summary, err
	}
	incomplete := func(reason string) {
		summary.Incomplete = true
		if summary.Reason == "" {
			summary.Reason = reason
		}
	}
	var walk func(string, int) error
	walk = func(relative string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		absolute, _, expected, err := ResolveDirectory(relative, true)
		if err != nil {
			summary.Skipped++
			incomplete("unavailable")
			return nil
		}
		handle, err := os.Open(absolute)
		if err != nil {
			summary.Skipped++
			incomplete("unavailable")
			return nil
		}
		defer handle.Close()
		opened, err := handle.Stat()
		if err != nil || !os.SameFile(expected, opened) {
			summary.Skipped++
			incomplete("source_changed")
			return nil
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, readErr := handle.ReadDir(64)
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				if summary.Visited >= limits.Entries {
					incomplete("entry_limit")
					return errScanStopped
				}
				summary.Visited++
				if ValidateLeafName(entry.Name()) != nil {
					summary.Skipped++
					incomplete("unsupported_entry")
					continue
				}
				child := path.Join(relative, entry.Name())
				_, _, info, err := ResolveExisting(child, false)
				if err != nil {
					summary.Skipped++
					incomplete("unavailable")
					continue
				}
				if !info.IsDir() && !info.Mode().IsRegular() {
					summary.Skipped++
					incomplete("unsupported_entry")
					continue
				}
				if !visit(ScanEntry{Path: child, Info: info}) {
					incomplete("result_limit")
					return errScanStopped
				}
				if recursive && info.IsDir() {
					if depth >= limits.Depth {
						incomplete("depth_limit")
						continue
					}
					if err := walk(child, depth+1); err != nil {
						return err
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				summary.Skipped++
				incomplete("unavailable")
				return nil
			}
		}
	}
	err = walk(relative, 1)
	if errors.Is(err, errScanStopped) {
		return summary, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		incomplete("time_limit")
		return summary, nil
	}
	return summary, err
}
