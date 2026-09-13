package utils

import (
	"archive/tar"
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwaples/rardecode/v2"
)

type ArchivePreviewEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size *int64 `json:"size,omitempty"`
}

type ArchivePreview struct {
	Entries        []ArchivePreviewEntry `json:"entries"`
	Complete       bool                  `json:"complete"`
	Truncated      bool                  `json:"truncated"`
	Reason         string                `json:"reason,omitempty"`
	Verification   string                `json:"verification"`
	EntriesScanned int                   `json:"entries_scanned"`
}

type previewReader struct {
	ctx       context.Context
	reader    io.Reader
	remaining int64
}

func (r *previewReader) Read(p []byte) (int, error) {
	if err := contextOperationError(r.ctx); err != nil {
		return 0, err
	}
	if r.remaining <= 0 {
		return 0, ErrArchiveLimitExceeded
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

// boundedZIPDirectory checks allocation-driving metadata before zip.NewReader.
func boundedZIPDirectory(ctx context.Context, source *os.File, size int64) error {
	if err := contextOperationError(ctx); err != nil {
		return err
	}
	length := int64(65557)
	if size < length {
		length = size
	}
	tail := make([]byte, length)
	if _, err := source.ReadAt(tail, size-length); err != nil {
		return ErrCorruptArchive
	}
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) != 0x06054b50 {
			continue
		}
		if i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) != len(tail) {
			continue
		}
		if binary.LittleEndian.Uint16(tail[i+4:]) != 0 || binary.LittleEndian.Uint16(tail[i+6:]) != 0 {
			return ErrUnsupportedArchive
		}
		count := uint64(binary.LittleEndian.Uint16(tail[i+10:]))
		if uint64(binary.LittleEndian.Uint16(tail[i+8:])) != count {
			return ErrUnsupportedArchive
		}
		directorySize := uint64(binary.LittleEndian.Uint32(tail[i+12:]))
		offset := uint64(binary.LittleEndian.Uint32(tail[i+16:]))
		end := uint64(size - length + int64(i))
		if count == 0xffff || directorySize == 0xffffffff || offset == 0xffffffff {
			if i < 20 || binary.LittleEndian.Uint32(tail[i-20:]) != 0x07064b50 {
				return ErrCorruptArchive
			}
			locator := tail[i-20 : i]
			if binary.LittleEndian.Uint32(locator[4:]) != 0 || binary.LittleEndian.Uint32(locator[16:]) != 1 {
				return ErrUnsupportedArchive
			}
			at := binary.LittleEndian.Uint64(locator[8:])
			if at > uint64(size) || uint64(size)-at < 56 {
				return ErrCorruptArchive
			}
			header := make([]byte, 56)
			if _, err := source.ReadAt(header, int64(at)); err != nil {
				return ErrCorruptArchive
			}
			if binary.LittleEndian.Uint32(header) != 0x06064b50 || binary.LittleEndian.Uint64(header[4:]) < 44 {
				return ErrCorruptArchive
			}
			if binary.LittleEndian.Uint32(header[16:]) != 0 || binary.LittleEndian.Uint32(header[20:]) != 0 {
				return ErrUnsupportedArchive
			}
			recordSize := binary.LittleEndian.Uint64(header[4:])
			locatorAt := uint64(size - length + int64(i) - 20)
			if at > locatorAt || locatorAt-at < 12 || recordSize != locatorAt-at-12 {
				return ErrCorruptArchive
			}
			count = binary.LittleEndian.Uint64(header[32:])
			if binary.LittleEndian.Uint64(header[24:]) != count {
				return ErrUnsupportedArchive
			}
			directorySize = binary.LittleEndian.Uint64(header[40:])
			offset = binary.LittleEndian.Uint64(header[48:])
			end = at
		}
		if count > 10000 || directorySize > 16<<20 {
			return ErrArchiveLimitExceeded
		}
		if offset > end || directorySize > end-offset {
			return ErrCorruptArchive
		}
		// Validate actual central records too: declared counts alone can lie.
		position := offset
		actual := uint64(0)
		header := make([]byte, 46)
		for position < offset+directorySize {
			if err := contextOperationError(ctx); err != nil {
				return err
			}
			if offset+directorySize-position < 46 {
				return ErrCorruptArchive
			}
			if _, err := source.ReadAt(header, int64(position)); err != nil {
				return ErrCorruptArchive
			}
			if binary.LittleEndian.Uint32(header) != 0x02014b50 {
				return ErrCorruptArchive
			}
			recordSize := uint64(46) + uint64(binary.LittleEndian.Uint16(header[28:])) + uint64(binary.LittleEndian.Uint16(header[30:])) + uint64(binary.LittleEndian.Uint16(header[32:]))
			if recordSize > offset+directorySize-position {
				return ErrCorruptArchive
			}
			position += recordSize
			actual++
			if actual > 10000 {
				return ErrArchiveLimitExceeded
			}
		}
		if actual != count {
			return ErrCorruptArchive
		}
		return nil
	}
	return ErrCorruptArchive
}

// PreviewArchive only reads metadata. Completion is not payload CRC verification.
func PreviewArchive(ctx context.Context, rawPath, version string) (ArchivePreview, error) {
	result := ArchivePreview{Entries: []ArchivePreviewEntry{}, Verification: "metadata_only"}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contextOperationError(ctx); err != nil {
		return result, err
	}
	absolute, relative, info, err := ResolveExisting(rawPath, false)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, ErrUnsupportedType
	}
	if version != "" && EntryVersion(info) != version {
		return result, ErrSourceChanged
	}
	if info.Size() == 0 {
		return result, ErrCorruptArchive
	}
	if info.Size() > maxArchiveBytes {
		return result, ErrArchiveLimitExceeded
	}
	format, suffix, ok := ClassifyArchive(relative)
	if !ok {
		return result, ErrUnsupportedArchive
	}
	source, err := os.Open(absolute)
	if err != nil {
		return result, operationError("io_error")
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(info, opened) || EntryVersion(info) != EntryVersion(opened) {
		return result, ErrSourceChanged
	}
	format, err = resolveExtractionFormat(source, format, info.Size())
	if err != nil {
		return result, err
	}
	registry := newArchivePathRegistry()
	responseBytes := 0
	add := func(name string, directory bool, size int64, mode os.FileMode) error {
		normalized, err := registry.add(name, directory, size, mode)
		if err != nil {
			return err
		}
		result.EntriesScanned++
		if len(result.Entries) >= 1000 || responseBytes+6*len(normalized)+128 > 2<<20 {
			return ErrArchiveLimitExceeded
		}
		responseBytes += 6*len(normalized) + 128
		kind := "file"
		if directory {
			kind = "directory"
		}
		result.Entries = append(result.Entries, ArchivePreviewEntry{Name: normalized, Kind: kind, Size: &size})
		return nil
	}
	scan := func() error {
		switch format {
		case ArchiveZIP:
			if err := boundedZIPDirectory(ctx, source, info.Size()); err != nil {
				return err
			}
			reader, err := zip.NewReader(contextReaderAt{ctx: ctx, reader: source}, info.Size())
			if err != nil {
				return ErrCorruptArchive
			}
			for _, file := range reader.File {
				if err := contextOperationError(ctx); err != nil {
					return err
				}
				if file.Flags&1 != 0 {
					return ErrEncryptedArchive
				}
				mode := file.Mode()
				directory := file.FileInfo().IsDir()
				if mode&os.ModeSymlink != 0 || !directory && !mode.IsRegular() {
					return ErrArchiveUnsafeEntry
				}
				if file.UncompressedSize64 > uint64(maxArchiveBytes) {
					return ErrArchiveLimitExceeded
				}
				if err := add(file.Name, directory, int64(file.UncompressedSize64), mode); err != nil {
					return err
				}
			}
		case ArchiveRAR:
			reader, err := rardecode.NewReader(&previewReader{ctx, source, maxArchiveBytes}, rardecode.MaxDictionarySize(int64(archiveDecoderMemory)))
			if err != nil {
				return mapRARError(err)
			}
			remaining := int64(256 << 20)
			for {
				if err := contextOperationError(ctx); err != nil {
					return err
				}
				header, err := reader.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return mapRARError(err)
				}
				if header.Encrypted || header.HeaderEncrypted {
					return ErrEncryptedArchive
				}
				mode := header.Mode()
				if header.LinkType != 0 || mode&os.ModeSymlink != 0 || !header.IsDir && !mode.IsRegular() {
					return ErrArchiveUnsafeEntry
				}
				if err := add(header.Name, header.IsDir, header.UnPackedSize, mode); err != nil {
					return err
				}
				budget := &previewReader{ctx, reader, remaining}
				if _, err := io.Copy(io.Discard, budget); err != nil {
					return err
				}
				remaining = budget.remaining
			}
		default:
			stream, err := newDecodedStream(ctx, source, format)
			if err != nil {
				return err
			}
			defer stream.close.Close()
			budget := &previewReader{ctx, stream.reader, 256 << 20}
			if isTarArchive(format) {
				reader := tar.NewReader(budget)
				for {
					header, err := reader.Next()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						return err
					}
					directory := header.Typeflag == tar.TypeDir
					if !directory && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
						return ErrArchiveUnsafeEntry
					}
					if len(header.Name) > 4096 {
						return ErrArchiveUnsafeEntry
					}
					if directory && (header.Name == "." || header.Name == "./") {
						if err := registry.countEntry(header.Size); err != nil {
							return err
						}
						continue
					}
					name := header.Name
					for strings.HasPrefix(name, "./") {
						name = strings.TrimPrefix(name, "./")
					}
					if err := add(name, directory, header.Size, os.FileMode(header.Mode)); err != nil {
						return err
					}
				}
				if _, err := io.Copy(io.Discard, budget); err != nil {
					return err
				}
			} else {
				name, err := standaloneOutputName(filepath.Base(relative), suffix)
				if err != nil {
					return err
				}
				if err := add(name, false, 0, 0600); err != nil {
					return err
				}
				result.Entries[0].Size = nil
			}
		}
		if len(result.Entries) == 0 {
			return ErrCorruptArchive
		}
		return nil
	}
	err = scan()
	if err != nil {
		result.Reason = ErrorCode(sanitizeArchiveError(err))
		result.Truncated = errors.Is(err, ErrArchiveLimitExceeded) || ctx.Err() != nil

	} else {
		result.Complete = true
	}
	current, statErr := source.Stat()
	if statErr != nil || EntryVersion(current) != EntryVersion(opened) {
		return result, ErrSourceChanged
	}
	_, _, resolved, resolveErr := ResolveExisting(rawPath, false)
	if resolveErr != nil || !os.SameFile(opened, resolved) || EntryVersion(opened) != EntryVersion(resolved) {
		return result, ErrSourceChanged
	}
	return result, nil
}
