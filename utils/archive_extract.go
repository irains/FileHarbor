package utils

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
	xz "github.com/mikelolasagasti/xz"
	"github.com/nwaples/rardecode/v2"
	"github.com/pierrec/lz4/v4"
)

const (
	archiveDecoderMemory = uint64(64 << 20)
	archiveCopyBuffer    = 64 << 10
)

var archiveBeforePublishHook func()

// Unzip preserves the original boolean compatibility API.
func Unzip(src string) bool {
	_, err := ExtractArchive(src)
	return err == nil
}

// ExtractArchive preserves the original API while the HTTP layer uses the
// context-aware variant below.
func ExtractArchive(rawPath string) (string, error) {
	return ExtractArchiveContext(context.Background(), rawPath)
}

// ExtractArchiveContext safely extracts one supported archive into its parent
// directory. The source is opened once, extraction is staged on the output
// filesystem, and completed top-level entries are published without replacing
// existing data.
func ExtractArchiveContext(ctx context.Context, rawPath string) (string, error) {
	return ExtractArchiveTargetContext(ctx, rawPath, ExtractionTarget{})
}

// ExtractionTarget distinguishes legacy omission from an explicit managed root.
type ExtractionTarget struct {
	Mode      string `json:"mode"`
	Directory string `json:"directory,omitempty"`
	Name      string `json:"name,omitempty"`
}

// ExtractArchiveTargetContext stages on the selected output filesystem. The
// compatibility result remains the source path; callers know their target mode.
func ExtractArchiveTargetContext(ctx context.Context, rawPath string, target ExtractionTarget) (string, error) {
	return ExtractArchiveVersionContext(ctx, rawPath, target, "")
}

func ExtractArchiveVersionContext(ctx context.Context, rawPath string, target ExtractionTarget, expectedVersion string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := operationMu.LockContext(ctx); err != nil {
		return "", contextOperationError(ctx)
	}
	defer operationMu.Unlock()
	if err := contextOperationError(ctx); err != nil {
		return "", err
	}

	absolute, relative, resolvedInfo, err := ResolveExisting(rawPath, false)
	if err != nil {
		return "", err
	}
	if expectedVersion != "" && versionFor(resolvedInfo) != expectedVersion {
		return "", ErrSourceChanged
	}
	if !resolvedInfo.Mode().IsRegular() {
		return "", ErrUnsupportedType
	}
	format, suffix, ok := ClassifyArchive(relative)
	if !ok {
		return "", ErrUnsupportedArchive
	}
	if resolvedInfo.Size() < 0 || resolvedInfo.Size() > maxArchiveBytes {
		return "", ErrArchiveLimitExceeded
	}

	source, err := os.Open(absolute)
	if err != nil {
		return "", operationError("io_error")
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil {
		return "", operationError("io_error")
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(resolvedInfo, openedInfo) || versionFor(resolvedInfo) != versionFor(openedInfo) {
		return "", ErrSourceChanged
	}
	openedDigest, err := digestOpenFile(ctx, source)
	if err != nil {
		return "", err
	}
	if openedInfo.Size() == 0 {
		return "", ErrCorruptArchive
	}
	format, err = resolveExtractionFormat(source, format, openedInfo.Size())
	if err != nil {
		return "", err
	}

	outputRelative := path.Dir(relative)
	if outputRelative == "." {
		outputRelative = ""
	}
	newFolder := ""
	switch target.Mode {
	case "", "current":
		if target.Directory != "" || target.Name != "" {
			return "", ErrInvalidPath
		}
	case "chosen":
		if target.Name != "" {
			return "", ErrInvalidPath
		}
		outputRelative = target.Directory
	case "new_folder":
		if target.Directory != "" {
			return "", ErrInvalidPath
		}
		newFolder = target.Name
		if newFolder == "" {
			newFolder = strings.TrimSuffix(filepath.Base(relative), suffix)
		}
		if ValidateLeafName(newFolder) != nil {
			return "", ErrInvalidName
		}
	default:
		return "", ErrInvalidPath
	}
	outputParent, outputRelative, outputInfo, err := ResolveDirectory(outputRelative, true)
	if err != nil {
		return "", err
	}
	outputDir := outputParent
	if newFolder != "" {
		if err := preflightPromotionTargets(outputParent, []string{newFolder}); err != nil {
			return "", err
		}
		outputDir = filepath.Join(outputParent, newFolder)
	}
	stage, err := os.MkdirTemp(outputParent, InternalArchiveExtractPrefix)
	if err != nil {
		return "", operationError("io_error")
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		return "", operationError("io_error")
	}
	defer cleanupOperationStage(ctx, stage, path.Join(outputRelative, filepath.Base(stage)), stageInfo)
	if err := observeOperation(ctx, OperationEvent{Phase: "staging", Path: path.Join(outputRelative, filepath.Base(stage))}); err != nil {
		return "", err
	}

	var tops []string
	switch format {
	case ArchiveZIP:
		tops, err = extractZIPContext(ctx, source, openedInfo.Size(), stage, outputDir)
	case ArchiveRAR:
		tops, err = extractRARContext(ctx, source, stage, outputDir)
	default:
		if _, seekErr := source.Seek(0, io.SeekStart); seekErr != nil {
			return "", operationError("io_error")
		}
		if isTarArchive(format) {
			tops, err = extractTARContext(ctx, source, format, stage, outputDir)
		} else {
			var outputName string
			outputName, err = standaloneOutputName(filepath.Base(relative), suffix)
			if err == nil {
				err = preflightPromotionTargets(outputDir, []string{outputName})
			}
			if err == nil {
				err = extractStandaloneContext(ctx, source, format, filepath.Join(stage, outputName))
				tops = []string{outputName}
			}
		}
	}
	if err != nil {
		return "", sanitizeArchiveError(err)
	}
	if len(tops) == 0 {
		return "", ErrCorruptArchive
	}

	if archiveBeforePublishHook != nil {
		archiveBeforePublishHook()
	}
	if err := sourceStillUnchanged(ctx, source, absolute, openedInfo, openedDigest); err != nil {
		return "", err
	}
	if err := contextOperationError(ctx); err != nil {
		return "", err
	}
	currentParent, _, currentInfo, err := ResolveDirectory(outputRelative, true)
	if err != nil || currentParent != outputParent || !os.SameFile(outputInfo, currentInfo) {
		return "", ErrSourceChanged
	}
	if newFolder != "" {
		if err := preflightPromotionTargets(outputParent, []string{newFolder}); err != nil {
			return "", err
		}
		publishedPath := path.Join(outputRelative, newFolder)
		if err := observeOperation(ctx, OperationEvent{Phase: "publish_intent", Path: publishedPath}); err != nil {
			return "", err
		}
		if err := renameNoReplace(stage, outputDir); err != nil {
			if destinationAlreadyExists(outputDir) {
				return "", ErrDestinationExists
			}
			return "", operationError("io_error")
		}
		if err := syncOperationDirectory(outputParent); err != nil {
			return "", operationError("execution_partial")
		}
		if err := observeOperation(ctx, OperationEvent{Phase: "published", Path: publishedPath}); err != nil {
			return "", operationError("execution_partial")
		}
		return relative, nil
	}
	if err := preflightPromotionTargets(outputDir, tops); err != nil {
		return "", err
	}
	if hasOperationObserver(ctx) {
		for _, top := range tops {
			if err := contextOperationError(ctx); err != nil {
				return "", err
			}
			publishedPath := path.Join(outputRelative, top)
			if err := observeOperation(ctx, OperationEvent{Phase: "publish_intent", Path: publishedPath}); err != nil {
				return "", err
			}
			if err := renameNoReplace(filepath.Join(stage, top), filepath.Join(outputDir, top)); err != nil {
				if destinationAlreadyExists(filepath.Join(outputDir, top)) {
					return "", ErrDestinationExists
				}
				return "", operationError("io_error")
			}
			if err := syncOperationDirectory(outputParent); err != nil {
				return "", operationError("execution_partial")
			}
			if err := observeOperation(ctx, OperationEvent{Phase: "published", Path: publishedPath}); err != nil {
				return "", operationError("execution_partial")
			}
		}
		return relative, nil
	}
	if err := promoteExtractionStage(stage, outputDir, tops); err != nil {
		return "", err
	}
	return relative, nil
}

func contextOperationError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return operationError("request_cancelled")
	default:
		return nil
	}
}

func digestOpenFile(ctx context.Context, source *os.File) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return digest, operationError("io_error")
	}
	hash := sha256.New()
	reader := &contextBudgetReader{ctx: ctx, reader: source}
	if _, err := io.CopyBuffer(hash, reader, make([]byte, archiveCopyBuffer)); err != nil {
		if reader.exceeded {
			return digest, ErrArchiveLimitExceeded
		}
		if ErrorCode(err) == "request_cancelled" {
			return digest, err
		}
		return digest, operationError("io_error")
	}
	copy(digest[:], hash.Sum(nil))
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return digest, operationError("io_error")
	}
	return digest, nil
}

func sourceStillUnchanged(ctx context.Context, source *os.File, absolute string, opened os.FileInfo, openedDigest [sha256.Size]byte) error {
	current, err := source.Stat()
	if err != nil || versionFor(current) != versionFor(opened) || !os.SameFile(current, opened) {
		return ErrSourceChanged
	}
	pathInfo, err := safeEntryInfo(absolute)
	if err != nil || versionFor(pathInfo) != versionFor(opened) || !os.SameFile(pathInfo, opened) {
		return ErrSourceChanged
	}
	currentDigest, err := digestOpenFile(ctx, source)
	if err != nil {
		return err
	}
	if currentDigest != openedDigest {
		return ErrSourceChanged
	}
	return nil
}

// Only gzip-TAR aliases accept an uncompressed TAR with a mismatched suffix.
// A gzip signature commits to gzip decoding; decoder failures never fall back.
func resolveExtractionFormat(source *os.File, format ArchiveFormat, size int64) (ArchiveFormat, error) {
	if format == ArchiveTARGzip {
		var header [512]byte
		n, err := source.ReadAt(header[:], 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", operationError("io_error")
		}
		if !bytes.HasPrefix(header[:n], []byte{0x1f, 0x8b}) && validTarHeader(header[:n]) {
			format = ArchiveTAR
		}
	}
	if err := validateFormatHeader(source, format, size); err != nil {
		return "", err
	}
	return format, nil
}

func validateFormatHeader(source *os.File, format ArchiveFormat, size int64) error {
	need := 16
	if format == ArchiveTAR {
		need = 512
	}
	if size < int64(need) {
		need = int(size)
	}
	header := make([]byte, need)
	if need > 0 {
		n, err := source.ReadAt(header, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return operationError("io_error")
		}
		header = header[:n]
	}
	matches := true
	switch format {
	case ArchiveZIP:
		// archive/zip validates the central directory itself. Do not require a
		// local-file signature at byte zero: valid self-extracting ZIP files have
		// a preamble before their ZIP data.
	case ArchiveRAR:
		matches = bytes.HasPrefix(header, []byte("Rar!\x1a\x07\x00")) || bytes.HasPrefix(header, []byte("Rar!\x1a\x07\x01\x00"))
	case ArchiveGzip, ArchiveTARGzip:
		matches = bytes.HasPrefix(header, []byte{0x1f, 0x8b})
	case ArchiveBzip2, ArchiveTARBzip2:
		matches = len(header) >= 4 && bytes.Equal(header[:3], []byte("BZh")) && header[3] >= '1' && header[3] <= '9'
	case ArchiveXZ, ArchiveTARXZ:
		matches = bytes.HasPrefix(header, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00})
	case ArchiveZstd, ArchiveTARZstd:
		matches = bytes.HasPrefix(header, []byte{0x28, 0xb5, 0x2f, 0xfd})
	case ArchiveLZ4, ArchiveTARLZ4:
		matches = bytes.HasPrefix(header, []byte{0x04, 0x22, 0x4d, 0x18})
	case ArchiveSnappy, ArchiveTARSnappy:
		matches = bytes.HasPrefix(header, []byte("\xff\x06\x00\x00sNaPpY")) || bytes.HasPrefix(header, []byte("\xff\x06\x00\x00S2sTwO"))
	case ArchiveZlib, ArchiveTARZlib:
		matches = validZlibHeader(header)
	case ArchiveTAR:
		matches = validTarHeader(header)
	case ArchiveBrotli, ArchiveTARBrotli:
		// Brotli deliberately has no stream magic. Its bit-level header and full
		// stream are validated by the constrained decoder before publication.
	}
	if !matches {
		return ErrCorruptArchive
	}
	return nil
}

func validZlibHeader(header []byte) bool {
	if len(header) < 2 || header[0]&0x0f != 8 || header[0]>>4 > 7 {
		return false
	}
	return (uint16(header[0])<<8|uint16(header[1]))%31 == 0
}

func validTarHeader(header []byte) bool {
	if len(header) < 512 {
		return false
	}
	var stored int64
	field := bytes.Trim(header[148:156], " \x00")
	if len(field) == 0 {
		return false
	}
	for _, value := range field {
		if value < '0' || value > '7' {
			return false
		}
		stored = stored*8 + int64(value-'0')
	}
	var sum int64
	for index, value := range header[:512] {
		if index >= 148 && index < 156 {
			value = ' '
		}
		sum += int64(value)
	}
	return stored == sum
}

type contextBudgetReader struct {
	ctx      context.Context
	reader   io.Reader
	read     int64
	exceeded bool
}

func (r *contextBudgetReader) Read(buffer []byte) (int, error) {
	if err := contextOperationError(r.ctx); err != nil {
		return 0, err
	}
	remaining := maxArchiveBytes - r.read
	if remaining < 0 {
		r.exceeded = true
		return 0, ErrArchiveLimitExceeded
	}
	if int64(len(buffer)) > remaining+1 {
		buffer = buffer[:remaining+1]
	}
	n, err := r.reader.Read(buffer)
	r.read += int64(n)
	if r.read > maxArchiveBytes {
		r.exceeded = true
		return n, ErrArchiveLimitExceeded
	}
	if err != nil && !errors.Is(err, io.EOF) {
		var operation *OperationError
		if errors.As(err, &operation) {
			return n, err
		}
		return n, operationError("io_error")
	}
	return n, err
}

type contextReaderAt struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r contextReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if err := contextOperationError(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.reader.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		var operation *OperationError
		if errors.As(err, &operation) {
			return n, err
		}
		return n, operationError("io_error")
	}
	return n, err
}

type decodedStream struct {
	reader io.Reader
	close  *idempotentCloser
	budget *contextBudgetReader
}

type idempotentCloser struct {
	once sync.Once
	fn   func() error
	err  error
}

func newIdempotentCloser(fn func() error) *idempotentCloser {
	if fn == nil {
		fn = func() error { return nil }
	}
	return &idempotentCloser{fn: fn}
}

func (c *idempotentCloser) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		c.err = c.fn()
	})
	return c.err
}

func mapDecoderOpenError(err error) error {
	if err == nil {
		return nil
	}
	var operation *OperationError
	if errors.As(err, &operation) {
		return err
	}
	if errors.Is(err, xz.ErrMemlimit) || errors.Is(err, zstd.ErrWindowSizeExceeded) {
		return ErrArchiveLimitExceeded
	}
	return ErrCorruptArchive
}

func newDecodedStream(ctx context.Context, source io.Reader, format ArchiveFormat) (decodedStream, error) {
	budget := &contextBudgetReader{ctx: ctx, reader: source}
	stream := decodedStream{reader: budget, close: newIdempotentCloser(nil), budget: budget}
	base := compressionBaseFormat(format)
	switch base {
	case ArchiveTAR:
		return stream, nil
	case ArchiveGzip:
		reader, err := gzip.NewReader(budget)
		if err != nil {
			return decodedStream{}, mapDecoderOpenError(err)
		}
		stream.reader = reader
		stream.close = newIdempotentCloser(reader.Close)
	case ArchiveBzip2:
		stream.reader = bzip2.NewReader(budget)
	case ArchiveXZ:
		reader, err := xz.NewReader(budget, uint32(archiveDecoderMemory))
		if err != nil {
			return decodedStream{}, mapDecoderOpenError(err)
		}
		stream.reader = reader
	case ArchiveZstd:
		reader, err := zstd.NewReader(budget,
			zstd.WithDecoderMaxMemory(archiveDecoderMemory),
			zstd.WithDecoderMaxWindow(archiveDecoderMemory),
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderLowmem(true),
			zstd.WithDecodeBuffersBelow(0),
		)
		if err != nil {
			return decodedStream{}, mapDecoderOpenError(err)
		}
		stream.reader = reader
		stream.close = newIdempotentCloser(func() error { reader.Close(); return nil })
	case ArchiveLZ4:
		reader := lz4.NewReader(budget)
		if err := reader.Apply(lz4.ConcurrencyOption(1)); err != nil {
			return decodedStream{}, mapDecoderOpenError(err)
		}
		stream.reader = reader
	case ArchiveBrotli:
		stream.reader = brotli.NewReader(budget)
	case ArchiveSnappy:
		stream.reader = s2.NewReader(budget, s2.ReaderMaxBlockSize(4<<20), s2.ReaderAllocBlock(64<<10))
	case ArchiveZlib:
		reader, err := zlib.NewReader(budget)
		if err != nil {
			return decodedStream{}, mapDecoderOpenError(err)
		}
		stream.reader = reader
		stream.close = newIdempotentCloser(reader.Close)
	default:
		return decodedStream{}, ErrUnsupportedArchive
	}
	return stream, nil
}

func compressionBaseFormat(format ArchiveFormat) ArchiveFormat {
	switch format {
	case ArchiveTAR:
		return ArchiveTAR
	case ArchiveGzip, ArchiveTARGzip:
		return ArchiveGzip
	case ArchiveBzip2, ArchiveTARBzip2:
		return ArchiveBzip2
	case ArchiveXZ, ArchiveTARXZ:
		return ArchiveXZ
	case ArchiveZstd, ArchiveTARZstd:
		return ArchiveZstd
	case ArchiveLZ4, ArchiveTARLZ4:
		return ArchiveLZ4
	case ArchiveBrotli, ArchiveTARBrotli:
		return ArchiveBrotli
	case ArchiveSnappy, ArchiveTARSnappy:
		return ArchiveSnappy
	case ArchiveZlib, ArchiveTARZlib:
		return ArchiveZlib
	default:
		return format
	}
}

type archivePathKind uint8

const (
	archiveImplicitDirectory archivePathKind = iota
	archiveDirectory
	archiveFile
)

type archivePathRecord struct {
	name string
	kind archivePathKind
}

type archivePathRegistry struct {
	paths          map[string]archivePathRecord
	tops           map[string]string
	directoryModes map[string]fs.FileMode
	entries        int
	bytes          int64
}

func newArchivePathRegistry() *archivePathRegistry {
	return &archivePathRegistry{
		paths:          make(map[string]archivePathRecord),
		tops:           make(map[string]string),
		directoryModes: make(map[string]fs.FileMode),
	}
}

func (r *archivePathRegistry) countEntry(size int64) error {
	r.entries++
	if size < 0 || r.entries > maxArchiveEntries {
		return ErrArchiveLimitExceeded
	}
	return nil
}

func (r *archivePathRegistry) add(rawName string, directory bool, size int64, mode fs.FileMode) (string, error) {
	name, err := validatePortableArchivePath(rawName)
	if err != nil {
		return "", err
	}
	if err := r.countEntry(size); err != nil {
		return "", err
	}
	if !directory {
		if size > maxArchiveBytes || r.bytes > maxArchiveBytes-size {
			return "", ErrArchiveLimitExceeded
		}
		r.bytes += size
	}
	components := strings.Split(name, "/")
	for index := range components {
		current := strings.Join(components[:index+1], "/")
		key := strings.ToLower(current)
		last := index == len(components)-1
		wanted := archiveImplicitDirectory
		if last {
			if directory {
				wanted = archiveDirectory
			} else {
				wanted = archiveFile
			}
		}
		existing, found := r.paths[key]
		if found {
			if existing.name != current {
				return "", ErrArchiveUnsafeEntry
			}
			if last && existing.kind == archiveImplicitDirectory && wanted == archiveDirectory {
				r.paths[key] = archivePathRecord{name: current, kind: archiveDirectory}
			} else if !last && existing.kind != archiveFile {
				continue
			} else {
				return "", ErrArchiveUnsafeEntry
			}
		} else {
			r.paths[key] = archivePathRecord{name: current, kind: wanted}
		}
	}
	top := components[0]
	topKey := strings.ToLower(top)
	if existing, found := r.tops[topKey]; found && existing != top {
		return "", ErrArchiveUnsafeEntry
	}
	r.tops[topKey] = top
	if directory {
		r.directoryModes[name] = sanitizeArchiveMode(mode)
	}
	return name, nil
}

func (r *archivePathRegistry) topNames() []string {
	result := make([]string, 0, len(r.tops))
	for _, name := range r.tops {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

func validatePortableArchivePath(rawName string) (string, error) {
	if rawName == "" || !utf8.ValidString(rawName) || strings.ContainsRune(rawName, 0) {
		return "", ErrArchiveUnsafeEntry
	}
	rawName = strings.ReplaceAll(rawName, "\\", "/")
	if strings.HasPrefix(rawName, "/") || path.IsAbs(rawName) || len(rawName) > 4096 {
		return "", ErrArchiveUnsafeEntry
	}
	parts := strings.Split(rawName, "/")
	for index, part := range parts {
		if index == len(parts)-1 && part == "" {
			parts = parts[:index]
			break
		}
		if part == "" || part == "." || part == ".." || len(part) > 255 || ValidateLeafName(part) != nil {
			return "", ErrArchiveUnsafeEntry
		}
	}
	if len(parts) == 0 {
		return "", ErrArchiveUnsafeEntry
	}
	name := strings.Join(parts, "/")
	if path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", ErrArchiveUnsafeEntry
	}
	return name, nil
}

func archiveDestination(stage, name string) (string, error) {
	target := filepath.Join(stage, filepath.FromSlash(name))
	if !contained(filepath.Clean(stage), target) {
		return "", ErrArchiveUnsafeEntry
	}
	return target, nil
}

func sanitizeArchiveMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm() & 0777
}

func createArchiveDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrArchiveUnsafeEntry
		}
		return operationError("io_error")
	}
	return nil
}

func createArchiveFile(path string, mode fs.FileMode) (*os.File, error) {
	if err := createArchiveDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, ErrArchiveUnsafeEntry
		}
		return nil, operationError("io_error")
	}
	_ = mode
	return file, nil
}

func copyArchivePayload(ctx context.Context, destination *os.File, source io.Reader, registry *archivePathRegistry, expected int64) error {
	buffer := make([]byte, archiveCopyBuffer)
	var written int64
	for {
		if err := contextOperationError(ctx); err != nil {
			return err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			if err := contextOperationError(ctx); err != nil {
				return err
			}
			if registry.bytes > maxArchiveBytes-int64(n) {
				return ErrArchiveLimitExceeded
			}
			count, writeErr := destination.Write(buffer[:n])
			if writeErr != nil || count != n {
				return operationError("io_error")
			}
			registry.bytes += int64(n)
			written += int64(n)
			if err := observeOperation(ctx, OperationEvent{Phase: "writing", Bytes: int64(n)}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
		if n == 0 {
			return ErrCorruptArchive
		}
	}
	if expected >= 0 && written != expected {
		return ErrCorruptArchive
	}
	return observeOperation(ctx, OperationEvent{Phase: "writing", Items: 1})
}

func closeArchiveFile(file *os.File, mode fs.FileMode, prior error) error {
	if prior == nil {
		prior = file.Sync()
	}
	closeErr := file.Close()
	if prior != nil {
		return prior
	}
	if closeErr != nil {
		return operationError("io_error")
	}
	if err := os.Chmod(file.Name(), sanitizeArchiveMode(mode)); err != nil {
		return operationError("io_error")
	}
	return nil
}

func applyDirectoryModes(stage string, modes map[string]fs.FileMode) error {
	names := make([]string, 0, len(modes))
	for name := range modes {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := strings.Count(names[i], "/"), strings.Count(names[j], "/")
		if left != right {
			return left > right
		}
		return names[i] > names[j]
	})
	for _, name := range names {
		destination, err := archiveDestination(stage, name)
		if err != nil {
			return err
		}
		if err := os.Chmod(destination, modes[name]); err != nil {
			return operationError("io_error")
		}
	}
	return nil
}

func extractZIPContext(ctx context.Context, source *os.File, size int64, stage, outputDir string) ([]string, error) {
	reader, err := zip.NewReader(contextReaderAt{ctx: ctx, reader: source}, size)
	if err != nil {
		var operation *OperationError
		if errors.As(err, &operation) {
			return nil, err
		}
		return nil, ErrCorruptArchive
	}
	if len(reader.File) == 0 {
		return nil, ErrCorruptArchive
	}

	// ZIP exposes all metadata up front. Validate the entire central directory
	// and known destination conflicts before creating anything in the stage.
	registry := newArchivePathRegistry()
	names := make([]string, len(reader.File))
	for index, entry := range reader.File {
		if err := contextOperationError(ctx); err != nil {
			return nil, err
		}
		if entry.Flags&1 != 0 {
			return nil, ErrEncryptedArchive
		}
		info := entry.FileInfo()
		if entry.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !entry.Mode().IsRegular()) {
			return nil, ErrArchiveUnsafeEntry
		}
		entrySize := int64(entry.UncompressedSize64)
		if uint64(entrySize) != entry.UncompressedSize64 {
			return nil, ErrArchiveLimitExceeded
		}
		name, err := registry.add(entry.Name, info.IsDir(), entrySize, entry.Mode())
		if err != nil {
			return nil, err
		}
		names[index] = name
	}
	tops := registry.topNames()
	if err := preflightPromotionTargets(outputDir, tops); err != nil {
		return nil, err
	}

	for index, entry := range reader.File {
		if err := contextOperationError(ctx); err != nil {
			return nil, err
		}
		info := entry.FileInfo()
		name := names[index]
		destination, err := archiveDestination(stage, name)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			if err := createArchiveDirectory(destination); err != nil {
				return nil, err
			}
			continue
		}
		input, err := entry.Open()
		if errors.Is(err, zip.ErrAlgorithm) {
			return nil, ErrUnsupportedArchive
		}
		if err != nil {
			if ErrorCode(err) == "request_cancelled" {
				return nil, err
			}
			return nil, ErrCorruptArchive
		}
		output, err := createArchiveFile(destination, entry.Mode())
		if err != nil {
			_ = input.Close()
			return nil, err
		}
		entrySize := int64(entry.UncompressedSize64)
		registry.bytes -= entrySize
		copyErr := copyArchivePayload(ctx, output, input, registry, entrySize)
		inputCloseErr := input.Close()
		if copyErr == nil && inputCloseErr != nil {
			copyErr = ErrCorruptArchive
		}
		if err := closeArchiveFile(output, entry.Mode(), copyErr); err != nil {
			return nil, err
		}
	}
	if err := applyDirectoryModes(stage, registry.directoryModes); err != nil {
		return nil, err
	}
	return tops, nil
}

func extractTARContext(ctx context.Context, source io.Reader, format ArchiveFormat, stage, outputDir string) ([]string, error) {
	stream, err := newDecodedStream(ctx, source, format)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.close.Close() }()
	reader := tar.NewReader(stream.reader)
	registry := newArchivePathRegistry()
	for {
		if err := contextOperationError(ctx); err != nil {
			return nil, err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, mapDecoderError(err, stream)
		}
		directory := header.Typeflag == tar.TypeDir
		if !directory && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, ErrArchiveUnsafeEntry
		}
		if len(header.Name) > 4096 {
			return nil, ErrArchiveUnsafeEntry
		}
		// TAR tools commonly emit root metadata and literal ./ prefixes. Do not
		// clean other components or register root permissions as an output path.
		if directory && (header.Name == "." || header.Name == "./") {
			if err := registry.countEntry(header.Size); err != nil {
				return nil, err
			}
			continue
		}
		rawName := header.Name
		for strings.HasPrefix(rawName, "./") {
			rawName = strings.TrimPrefix(rawName, "./")
		}
		name, err := registry.add(rawName, directory, header.Size, fs.FileMode(header.Mode))
		if err != nil {
			return nil, err
		}
		if err := preflightTopDestination(outputDir, name); err != nil {
			return nil, err
		}
		destination, err := archiveDestination(stage, name)
		if err != nil {
			return nil, err
		}
		if directory {
			if err := createArchiveDirectory(destination); err != nil {
				return nil, err
			}
			continue
		}
		output, err := createArchiveFile(destination, fs.FileMode(header.Mode))
		if err != nil {
			return nil, err
		}
		registry.bytes -= header.Size
		copyErr := copyArchivePayload(ctx, output, io.LimitReader(reader, header.Size), registry, header.Size)
		if err := closeArchiveFile(output, fs.FileMode(header.Mode), copyErr); err != nil {
			return nil, mapDecoderError(err, stream)
		}
	}
	if len(registry.tops) == 0 {
		return nil, ErrCorruptArchive
	}
	if err := drainDecodedStream(ctx, stream); err != nil {
		return nil, err
	}
	if err := stream.close.Close(); err != nil {
		return nil, ErrCorruptArchive
	}
	if err := applyDirectoryModes(stage, registry.directoryModes); err != nil {
		return nil, err
	}
	return registry.topNames(), nil
}

func drainDecodedStream(ctx context.Context, stream decodedStream) error {
	buffer := make([]byte, archiveCopyBuffer)
	for {
		if err := contextOperationError(ctx); err != nil {
			return err
		}
		n, err := stream.reader.Read(buffer)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return mapDecoderError(err, stream)
		}
		if n == 0 {
			return ErrCorruptArchive
		}
	}
}

func standaloneOutputName(inputName, suffix string) (string, error) {
	if suffix == "" || len(inputName) <= len(suffix) {
		return "", ErrArchiveUnsafeEntry
	}
	name := inputName[:len(inputName)-len(suffix)]
	if ValidateLeafName(name) != nil || len(name) > 255 {
		return "", ErrArchiveUnsafeEntry
	}
	return name, nil
}

func extractStandaloneContext(ctx context.Context, source io.Reader, format ArchiveFormat, destination string) error {
	stream, err := newDecodedStream(ctx, source, format)
	if err != nil {
		return err
	}
	defer func() { _ = stream.close.Close() }()
	output, err := createArchiveFile(destination, 0644)
	if err != nil {
		return err
	}
	registry := newArchivePathRegistry()
	copyErr := copyArchivePayload(ctx, output, stream.reader, registry, -1)
	if copyErr == nil && registry.bytes == 0 {
		copyErr = ErrCorruptArchive
	}
	if copyErr == nil {
		copyErr = stream.close.Close()
	}
	var operation *OperationError
	if copyErr != nil && !errors.As(copyErr, &operation) {
		copyErr = mapDecoderError(copyErr, stream)
	}
	return closeArchiveFile(output, 0644, copyErr)
}

func extractRARContext(ctx context.Context, source io.Reader, stage, outputDir string) ([]string, error) {
	budget := &contextBudgetReader{ctx: ctx, reader: source}
	reader, err := rardecode.NewReader(budget, rardecode.MaxDictionarySize(int64(archiveDecoderMemory)))
	if err != nil {
		return nil, mapRARError(err)
	}
	registry := newArchivePathRegistry()
	for {
		if err := contextOperationError(ctx); err != nil {
			return nil, err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, mapRARError(err)
		}
		if header.Encrypted || header.HeaderEncrypted {
			return nil, ErrEncryptedArchive
		}
		mode := header.Mode()
		if header.LinkType != 0 || mode&os.ModeSymlink != 0 || (!header.IsDir && (mode&os.ModeType != 0 || !mode.IsRegular())) {
			return nil, ErrArchiveUnsafeEntry
		}
		name, err := registry.add(header.Name, header.IsDir, header.UnPackedSize, mode)
		if err != nil {
			return nil, err
		}
		if err := preflightTopDestination(outputDir, name); err != nil {
			return nil, err
		}
		destination, err := archiveDestination(stage, name)
		if err != nil {
			return nil, err
		}
		if header.IsDir {
			if err := createArchiveDirectory(destination); err != nil {
				return nil, err
			}
			continue
		}
		output, err := createArchiveFile(destination, mode)
		if err != nil {
			return nil, err
		}
		registry.bytes -= header.UnPackedSize
		copyErr := copyArchivePayload(ctx, output, io.LimitReader(reader, header.UnPackedSize), registry, header.UnPackedSize)
		if err := closeArchiveFile(output, mode, copyErr); err != nil {
			return nil, mapRARError(err)
		}
	}
	if registry.entries == 0 {
		return nil, ErrCorruptArchive
	}
	if err := applyDirectoryModes(stage, registry.directoryModes); err != nil {
		return nil, err
	}
	return registry.topNames(), nil
}

func mapDecoderError(err error, stream decodedStream) error {
	if err == nil {
		return nil
	}
	var operation *OperationError
	if errors.As(err, &operation) {
		return err
	}
	if stream.budget != nil && stream.budget.exceeded {
		return ErrArchiveLimitExceeded
	}
	if errors.Is(err, xz.ErrMemlimit) || errors.Is(err, zstd.ErrWindowSizeExceeded) {
		return ErrArchiveLimitExceeded
	}
	return ErrCorruptArchive
}

func mapRARError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, rardecode.ErrArchiveEncrypted), errors.Is(err, rardecode.ErrArchivedFileEncrypted), errors.Is(err, rardecode.ErrBadPassword):
		return ErrEncryptedArchive
	case errors.Is(err, rardecode.ErrMultiVolume), errors.Is(err, rardecode.ErrBadVolumeNumber):
		return ErrUnsupportedArchive
	case errors.Is(err, rardecode.ErrDictionaryTooLarge):
		return ErrArchiveLimitExceeded
	default:
		var operation *OperationError
		if errors.As(err, &operation) {
			return err
		}
		return ErrCorruptArchive
	}
}

func sanitizeArchiveError(err error) error {
	if err == nil {
		return nil
	}
	code := ErrorCode(err)
	switch code {
	case ErrUnsupportedArchive.Code, ErrCorruptArchive.Code, ErrEncryptedArchive.Code, ErrArchiveUnsafeEntry.Code, ErrArchiveLimitExceeded.Code, ErrDestinationExists.Code, ErrSourceChanged.Code, "request_cancelled", "io_error":
		return err
	default:
		return ErrCorruptArchive
	}
}

func preflightTopDestination(outputDir, archiveName string) error {
	top := strings.SplitN(archiveName, "/", 2)[0]
	target := filepath.Join(outputDir, filepath.FromSlash(top))
	if _, err := os.Lstat(target); err == nil {
		return ErrDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return operationError("io_error")
	}
	return nil
}

func preflightPromotionTargets(outputDir string, tops []string) error {
	seen := make(map[string]struct{}, len(tops))
	for _, top := range tops {
		key := strings.ToLower(top)
		if _, duplicate := seen[key]; duplicate {
			return ErrArchiveUnsafeEntry
		}
		seen[key] = struct{}{}
		if err := preflightTopDestination(outputDir, top); err != nil {
			return err
		}
	}
	return nil
}

func destinationAlreadyExists(destination string) bool {
	_, err := os.Lstat(destination)
	return err == nil
}

func promoteExtractionStage(stage, outputDir string, tops []string) error {
	promoted := make([]string, 0, len(tops))
	for _, top := range tops {
		source := filepath.Join(stage, top)
		target := filepath.Join(outputDir, top)
		if err := renameNoReplace(source, target); err != nil {
			rollbackOK := true
			for index := len(promoted) - 1; index >= 0; index-- {
				name := promoted[index]
				if rollbackErr := os.Rename(filepath.Join(outputDir, name), filepath.Join(stage, name)); rollbackErr != nil {
					rollbackOK = false
				}
			}
			if errors.Is(err, fs.ErrExist) || destinationAlreadyExists(target) {
				if rollbackOK {
					return ErrDestinationExists
				}
				return operationError("execution_partial")
			}
			if rollbackOK {
				return operationError("io_error")
			}
			return operationError("execution_partial")
		}
		promoted = append(promoted, top)
	}
	return nil
}
