package main

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/irains/fileharbor/utils"
)

// ScanSize visits all records, not the paginated browser listing. Private
// metadata and unknown records are never counted as verified payload bytes.
func (bin *RecycleBin) ScanSize(ctx context.Context) (utils.DirectorySize, error) {
	result := utils.DirectorySize{ScannedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stop := errors.New("scan limit")
	incomplete := func(reason string) {
		result.Incomplete = true
		if result.Reason == "" {
			result.Reason = reason
		}
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			incomplete("request_cancelled")
			return err
		}
		if result.Visited >= 100000 {
			incomplete("entry_limit")
			return stop
		}
		result.Visited++
		return nil
	}
	inspect := func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil, utils.ErrUnsupportedType
		}
		return info, nil
	}
	readDirectory := func(path string, info os.FileInfo, visit func(os.DirEntry) error) error {
		handle, err := os.Open(path)
		if err != nil {
			return err
		}
		defer handle.Close()
		opened, err := handle.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return utils.ErrSourceChanged
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, err := handle.ReadDir(64)
			for _, entry := range entries {
				if err := visit(entry); err != nil {
					return err
				}
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if err := check(); err != nil {
			return err
		}
		info, err := inspect(path)
		if err != nil {
			result.Skipped++
			incomplete("unavailable")
			return nil
		}
		if !info.IsDir() {
			if info.Size() < 0 || result.Bytes > math.MaxInt64-info.Size() {
				incomplete("size_overflow")
				return stop
			}
			result.Bytes += info.Size()
			result.Files++
			return nil
		}
		result.Directories++
		if depth >= 128 {
			result.Skipped++
			incomplete("depth_limit")
			return nil
		}
		err = readDirectory(path, info, func(entry os.DirEntry) error {
			if utils.ValidateLeafName(entry.Name()) != nil {
				if err := check(); err != nil {
					return err
				}
				result.Skipped++
				incomplete("unsupported_entry")
				return nil
			}
			return walk(filepath.Join(path, entry.Name()), depth+1)
		})
		if err != nil && !errors.Is(err, stop) && ctx.Err() == nil {
			result.Skipped++
			incomplete("unavailable")
			return nil
		}
		return err
	}
	root, err := inspect(bin.directory)
	if err != nil || !root.IsDir() {
		return result, utils.ErrTrashRecord
	}
	err = readDirectory(bin.directory, root, func(entry os.DirEntry) error {
		if err := check(); err != nil {
			return err
		}
		invalid := func() error { result.Skipped++; incomplete("trash_record_invalid"); return nil }
		if !isTrashRecordID(entry.Name()) {
			return invalid()
		}
		directory := filepath.Join(bin.directory, entry.Name())
		info, err := inspect(directory)
		if err != nil || !info.IsDir() {
			return invalid()
		}
		count := 0
		valid := true
		err = readDirectory(directory, info, func(child os.DirEntry) error {
			if err := check(); err != nil {
				return err
			}
			count++
			if count > 2 || child.Name() != trashMetadataFile && child.Name() != trashPayloadName {
				valid = false
				return stop
			}
			return nil
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if result.Visited >= 100000 {
			incomplete("entry_limit")
			return stop
		}
		if err != nil || !valid || count != 2 {
			return invalid()
		}
		metadataPath := filepath.Join(directory, trashMetadataFile)
		metadataInfo, err := inspect(metadataPath)
		if err != nil || !metadataInfo.Mode().IsRegular() || metadataInfo.Size() > maxTrashMetadataBytes {
			return invalid()
		}
		metadata, err := decodeTrashMetadata(metadataPath)
		if err != nil || metadata.ID != entry.Name() {
			return invalid()
		}
		payload := filepath.Join(directory, trashPayloadName)
		payloadInfo, err := inspect(payload)
		if err != nil || payloadInfo.IsDir() != (metadata.Kind == "directory") {
			return invalid()
		}
		return walk(payload, 0)
	})
	if ctx.Err() != nil {
		incomplete("request_cancelled")
		return result, ctx.Err()
	}
	if errors.Is(err, stop) {
		return result, nil
	}
	return result, err
}
