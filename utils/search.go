package utils

import (
	"context"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

type SearchOptions struct {
	Directory      string
	Recursive      bool
	Name           string
	Extension      string
	MinSize        *int64
	MaxSize        *int64
	ModifiedAfter  *time.Time
	ModifiedBefore *time.Time
}

type SearchEntry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Parent   string    `json:"parent"`
	Kind     string    `json:"kind"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Version  string    `json:"version"`
}

type SearchResult struct {
	Entries []SearchEntry `json:"entries"`
	ScanSummary
}

func SearchManaged(ctx context.Context, options SearchOptions) (SearchResult, error) {
	result := SearchResult{Entries: []SearchEntry{}}
	if len(options.Name) > 512 || len(options.Extension) > 128 || !utf8.ValidString(options.Name) || !utf8.ValidString(options.Extension) {
		return result, ErrInvalidPath
	}
	if options.MinSize != nil && *options.MinSize < 0 || options.MaxSize != nil && *options.MaxSize < 0 || options.MinSize != nil && options.MaxSize != nil && *options.MinSize > *options.MaxSize {
		return result, ErrInvalidPath
	}
	if options.ModifiedAfter != nil && options.ModifiedBefore != nil && options.ModifiedAfter.After(*options.ModifiedBefore) {
		return result, ErrInvalidPath
	}
	name := strings.ToLower(options.Name)
	extension := strings.TrimPrefix(strings.ToLower(options.Extension), ".")
	responseBytes := 0
	summary, err := WalkManaged(ctx, options.Directory, options.Recursive, ScanLimits{100000, 128, 5 * time.Second}, func(entry ScanEntry) bool {
		info := entry.Info
		if !strings.Contains(strings.ToLower(info.Name()), name) {
			return true
		}
		if options.ModifiedAfter != nil && info.ModTime().Before(*options.ModifiedAfter) || options.ModifiedBefore != nil && info.ModTime().After(*options.ModifiedBefore) {
			return true
		}
		if extension != "" || options.MinSize != nil || options.MaxSize != nil {
			if !info.Mode().IsRegular() {
				return true
			}
			if extension != "" && strings.TrimPrefix(strings.ToLower(path.Ext(info.Name())), ".") != extension {
				return true
			}
			if options.MinSize != nil && info.Size() < *options.MinSize || options.MaxSize != nil && info.Size() > *options.MaxSize {
				return true
			}
		}
		// Reserve worst-case JSON escaping overhead for all path/name fields.
		weight := 6*(len(entry.Path)*2+len(info.Name())) + 512
		if len(result.Entries) >= 500 || responseBytes+weight > 2<<20 {
			return false
		}
		responseBytes += weight
		parent := path.Dir(entry.Path)
		if parent == "." {
			parent = ""
		}
		kind := "file"
		size := info.Size()
		if info.IsDir() {
			kind = "directory"
			size = 0
		}
		result.Entries = append(result.Entries, SearchEntry{Name: info.Name(), Path: entry.Path, Parent: parent, Kind: kind, Size: size, Modified: info.ModTime().UTC(), Version: EntryVersion(info)})
		return true
	})
	result.ScanSummary = summary
	return result, err
}
