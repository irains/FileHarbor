package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/irains/fileharbor/utils"
)

const (
	trashRecordSchemaVersion = 1
	trashRecordIDLength      = 32
	trashMetadataFile        = "metadata.json"
	trashPayloadName         = "payload"
	trashPayloadStageName    = "payload.staging"
	trashPageSize            = 50
	maxTrashMetadataBytes    = 32 << 10
)

type trashMetadata struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	OriginalPath  string    `json:"original_path"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	SizeBytes     int64     `json:"size_bytes"`
	DeletedAt     time.Time `json:"deleted_at"`
}

// TrashEntry is the safe, browser-visible summary of one recycled workspace item.
type TrashEntry struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	OriginalPath string    `json:"original_path"`
	Kind         string    `json:"kind"`
	SizeBytes    int64     `json:"size_bytes"`
	DeletedAt    time.Time `json:"deleted_at"`
}

type TrashPage struct {
	Entries    []TrashEntry `json:"entries"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type trashRecord struct {
	metadata     trashMetadata
	directory    string
	metadataPath string
	payloadPath  string
	payloadInfo  os.FileInfo
}

// RecycleBin owns durable private records below RuntimeState.TrashDir. Workspace
// sources and recycle records share utils.WithOperationLock so a source cannot be
// modified by another FileHarbor mutation midway through a transfer.
type RecycleBin struct {
	directory                        string
	renameNoReplace                  func(source, destination string) error
	isCrossDeviceError               func(error) bool
	afterCrossDevicePayloadPublished func()
}

// renameTransfer preserves native no-replace behavior unless a directory is
// confirmed to cross volumes. Stage publication always uses renameNoReplace.
func (bin *RecycleBin) renameTransfer(source, destination string, info os.FileInfo) (bool, error) {
	if info.IsDir() && confirmedTrashCrossVolume(source, destination) {
		return true, nil
	}
	err := bin.renameNoReplace(source, destination)
	return err != nil && bin.isCrossDeviceError(err), err
}

func newRecycleBin(state *RuntimeState) (*RecycleBin, error) {
	if state == nil || state.TrashDir == "" {
		return nil, errors.New("recycle bin state is unavailable")
	}
	if err := ensurePrivateDirectory(state.TrashDir); err != nil {
		return nil, err
	}
	return &RecycleBin{
		directory:          state.TrashDir,
		renameNoReplace:    utils.RenameNoReplace,
		isCrossDeviceError: utils.IsCrossDeviceError,
	}, nil
}

func isTrashRecordID(value string) bool {
	if len(value) != trashRecordIDLength {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func newTrashRecordID() (string, error) {
	bytes := make([]byte, trashRecordIDLength/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", errors.New("could not generate recycle-bin record")
	}
	return hex.EncodeToString(bytes), nil
}

func (bin *RecycleBin) recordPath(id string) (string, error) {
	if bin == nil || !isTrashRecordID(id) {
		return "", utils.ErrTrashRecord
	}
	path := filepath.Join(bin.directory, id)
	if filepath.Dir(path) != filepath.Clean(bin.directory) {
		return "", utils.ErrTrashRecord
	}
	return path, nil
}

func validateTrashTree(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, utils.ErrNotFound
	}
	if err != nil {
		return nil, errors.New("could not inspect recycled item")
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return nil, utils.ErrUnsupportedType
	}
	if !info.IsDir() {
		return info, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, errors.New("could not inspect recycled item")
	}
	for _, entry := range entries {
		if utils.ValidateLeafName(entry.Name()) != nil {
			return nil, utils.ErrUnsupportedType
		}
		if _, err := validateTrashTree(filepath.Join(path, entry.Name())); err != nil {
			return nil, err
		}
	}
	return info, nil
}

func removeValidatedTree(path string, info os.FileInfo) error {
	current, err := validateTrashTree(path)
	if err != nil {
		return err
	}
	if info == nil || current.IsDir() != info.IsDir() {
		return utils.ErrUnsupportedType
	}
	if current.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return errors.New("could not remove recycled item")
		}
		return nil
	}
	if err := os.Remove(path); err != nil {
		return errors.New("could not remove recycled item")
	}
	return nil
}

func copyValidatedTree(source, target string, info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return utils.ErrUnsupportedType
	}
	if info.IsDir() {
		if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
			return errors.New("could not create recycled item")
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return errors.New("could not inspect recycled item")
		}
		for _, entry := range entries {
			if utils.ValidateLeafName(entry.Name()) != nil {
				return utils.ErrUnsupportedType
			}
			childSource := filepath.Join(source, entry.Name())
			childInfo, err := validateTrashTree(childSource)
			if err != nil {
				return err
			}
			if err := copyValidatedTree(childSource, filepath.Join(target, entry.Name()), childInfo); err != nil {
				return err
			}
		}
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return errors.New("could not read recycled item")
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return errors.New("could not create recycled item")
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, info.Size()+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != info.Size() {
		return errors.New("could not copy recycled item")
	}
	return nil
}

func syncDirectories(paths ...string) error {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		if err := syncRuntimeDirectory(path); err != nil {
			return errors.New("could not sync recycle-bin state")
		}
	}
	return nil
}

func (bin *RecycleBin) createRecord(metadata trashMetadata) (trashRecord, error) {
	for attempt := 0; attempt < 8; attempt++ {
		id, err := newTrashRecordID()
		if err != nil {
			return trashRecord{}, err
		}
		directory, err := bin.recordPath(id)
		if err != nil {
			return trashRecord{}, err
		}
		if err := os.Mkdir(directory, 0700); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return trashRecord{}, errors.New("could not create recycle-bin record")
		}
		cleanup := true
		cleanupDirectory := directory
		defer func() {
			if cleanup {
				_ = os.RemoveAll(cleanupDirectory)
			}
		}()
		if err := protectPrivateDirectory(directory); err != nil {
			return trashRecord{}, errors.New("could not protect recycle-bin record")
		}
		metadata.ID = id
		payloadPath := filepath.Join(directory, trashPayloadName)
		metadataPath := filepath.Join(directory, trashMetadataFile)
		encoded, err := json.Marshal(metadata)
		if err != nil || len(encoded) > maxTrashMetadataBytes {
			return trashRecord{}, errors.New("could not encode recycle-bin metadata")
		}
		file, err := os.OpenFile(metadataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return trashRecord{}, errors.New("could not create recycle-bin metadata")
		}
		_, writeErr := file.Write(encoded)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			return trashRecord{}, errors.New("could not save recycle-bin metadata")
		}
		if err := protectPrivateFile(metadataPath); err != nil {
			return trashRecord{}, errors.New("could not protect recycle-bin metadata")
		}
		if err := syncDirectories(directory, bin.directory); err != nil {
			return trashRecord{}, err
		}
		cleanup = false
		return trashRecord{metadata: metadata, directory: directory, metadataPath: metadataPath, payloadPath: payloadPath}, nil
	}
	return trashRecord{}, errors.New("could not allocate recycle-bin record")
}

func decodeTrashMetadata(path string) (trashMetadata, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return trashMetadata{}, utils.ErrTrashRecord
	}
	if err != nil {
		return trashMetadata{}, errors.New("could not inspect recycle-bin metadata")
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxTrashMetadataBytes+1))
	if err != nil || len(contents) == 0 || len(contents) > maxTrashMetadataBytes {
		return trashMetadata{}, utils.ErrTrashRecord
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var metadata trashMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return trashMetadata{}, utils.ErrTrashRecord
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return trashMetadata{}, utils.ErrTrashRecord
	}
	if metadata.SchemaVersion != trashRecordSchemaVersion || !isTrashRecordID(metadata.ID) || metadata.DeletedAt.IsZero() || metadata.SizeBytes < 0 {
		return trashMetadata{}, utils.ErrTrashRecord
	}
	cleaned, err := utils.CleanRelative(metadata.OriginalPath, false)
	if err != nil || cleaned != metadata.OriginalPath || utils.ValidateLeafName(metadata.Name) != nil || filepath.Base(filepath.FromSlash(cleaned)) != metadata.Name {
		return trashMetadata{}, utils.ErrTrashRecord
	}
	if metadata.Kind != "file" && metadata.Kind != "directory" {
		return trashMetadata{}, utils.ErrTrashRecord
	}
	return metadata, nil
}

func (bin *RecycleBin) loadRecord(id string) (trashRecord, error) {
	directory, err := bin.recordPath(id)
	if err != nil {
		return trashRecord{}, err
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return trashRecord{}, utils.ErrNotFound
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return trashRecord{}, utils.ErrTrashRecord
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return trashRecord{}, errors.New("could not inspect recycle-bin record")
	}
	if len(entries) != 2 {
		return trashRecord{}, utils.ErrTrashRecord
	}
	metadataPath := filepath.Join(directory, trashMetadataFile)
	payloadPath := filepath.Join(directory, trashPayloadName)
	seenMetadata, seenPayload := false, false
	for _, entry := range entries {
		switch entry.Name() {
		case trashMetadataFile:
			seenMetadata = true
		case trashPayloadName:
			seenPayload = true
		default:
			return trashRecord{}, utils.ErrTrashRecord
		}
	}
	if !seenMetadata || !seenPayload {
		return trashRecord{}, utils.ErrTrashRecord
	}
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil || metadataInfo.Mode()&os.ModeSymlink != 0 || !metadataInfo.Mode().IsRegular() {
		return trashRecord{}, utils.ErrTrashRecord
	}
	metadata, err := decodeTrashMetadata(metadataPath)
	if err != nil || metadata.ID != id {
		return trashRecord{}, utils.ErrTrashRecord
	}
	payloadInfo, err := validateTrashTree(payloadPath)
	if err != nil {
		return trashRecord{}, err
	}
	kind := "file"
	if payloadInfo.IsDir() {
		kind = "directory"
	}
	if kind != metadata.Kind {
		return trashRecord{}, utils.ErrTrashRecord
	}
	return trashRecord{metadata: metadata, directory: directory, metadataPath: metadataPath, payloadPath: payloadPath, payloadInfo: payloadInfo}, nil
}

func validatedTreeSize(path string) (int64, error) {
	info, err := validateTrashTree(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, errors.New("could not inspect recycled item")
	}
	for _, entry := range entries {
		childSize, err := validatedTreeSize(filepath.Join(path, entry.Name()))
		if err != nil {
			return 0, err
		}
		if childSize < 0 || total > int64(^uint64(0)>>1)-childSize {
			return 0, errors.New("recycled item is too large")
		}
		total += childSize
	}
	return total, nil
}

func publicTrashEntry(record trashRecord) TrashEntry {
	size, err := validatedTreeSize(record.payloadPath)
	if err != nil {
		size = record.metadata.SizeBytes
	}
	return TrashEntry{
		ID:           record.metadata.ID,
		Name:         record.metadata.Name,
		OriginalPath: record.metadata.OriginalPath,
		Kind:         record.metadata.Kind,
		SizeBytes:    size,
		DeletedAt:    record.metadata.DeletedAt,
	}
}

func (bin *RecycleBin) listRecords() ([]trashRecord, error) {
	entries, err := os.ReadDir(bin.directory)
	if err != nil {
		return nil, errors.New("could not inspect recycle bin")
	}
	records := make([]trashRecord, 0, len(entries))
	for _, entry := range entries {
		if !isTrashRecordID(entry.Name()) {
			continue
		}
		record, err := bin.loadRecord(entry.Name())
		if err != nil {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(first, second int) bool {
		if records[first].metadata.DeletedAt.Equal(records[second].metadata.DeletedAt) {
			return records[first].metadata.ID > records[second].metadata.ID
		}
		return records[first].metadata.DeletedAt.After(records[second].metadata.DeletedAt)
	})
	return records, nil
}

func (bin *RecycleBin) List(cursor string) (TrashPage, error) {
	if cursor != "" && !isTrashRecordID(cursor) {
		return TrashPage{}, utils.ErrTrashRecord
	}
	records, err := bin.listRecords()
	if err != nil {
		return TrashPage{}, err
	}
	start := 0
	if cursor != "" {
		found := false
		for index, record := range records {
			if record.metadata.ID == cursor {
				start = index + 1
				found = true
				break
			}
		}
		if !found {
			return TrashPage{}, utils.ErrNotFound
		}
	}
	end := start + trashPageSize
	if end > len(records) {
		end = len(records)
	}
	page := TrashPage{Entries: make([]TrashEntry, 0, end-start)}
	for _, record := range records[start:end] {
		page.Entries = append(page.Entries, publicTrashEntry(record))
	}
	if end < len(records) {
		page.NextCursor = records[end-1].metadata.ID
	}
	return page, nil
}

func sourceMetadata(relative string, info os.FileInfo) (trashMetadata, error) {
	if utils.ValidateLeafName(filepath.Base(filepath.FromSlash(relative))) != nil {
		return trashMetadata{}, utils.ErrInvalidPath
	}
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	return trashMetadata{
		SchemaVersion: trashRecordSchemaVersion,
		OriginalPath:  relative,
		Name:          filepath.Base(filepath.FromSlash(relative)),
		Kind:          kind,
		SizeBytes:     info.Size(),
		DeletedAt:     time.Now().UTC(),
	}, nil
}

func normalizeNoReplaceError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrExist) {
		return utils.ErrDestinationExists
	}
	return err
}

func (bin *RecycleBin) moveResolved(absolute, relative string, info os.FileInfo) (TrashEntry, error) {
	validated, err := validateTrashTree(absolute)
	if err != nil {
		return TrashEntry{}, err
	}
	if utils.EntryVersion(validated) != utils.EntryVersion(info) {
		return TrashEntry{}, utils.ErrSourceChanged
	}
	info = validated
	metadata, err := sourceMetadata(relative, info)
	if err != nil {
		return TrashEntry{}, err
	}
	record, err := bin.createRecord(metadata)
	if err != nil {
		return TrashEntry{}, err
	}
	cleanupRecord := true
	defer func() {
		if cleanupRecord {
			_ = os.RemoveAll(record.directory)
		}
	}()

	if crossVolume, err := bin.renameTransfer(absolute, record.payloadPath, info); !crossVolume && err == nil {
		record.payloadInfo = info
		if err := syncDirectories(record.directory, bin.directory, filepath.Dir(absolute)); err != nil {
			cleanupRecord = false
			return publicTrashEntry(record), utils.ErrExecutionPartial
		}
		cleanupRecord = false
		return publicTrashEntry(record), nil
	} else if !crossVolume {
		return TrashEntry{}, normalizeNoReplaceError(err)
	}

	stage := filepath.Join(record.directory, trashPayloadStageName)
	if err := copyValidatedTree(absolute, stage, info); err != nil {
		return TrashEntry{}, err
	}
	stageInfo, err := validateTrashTree(stage)
	if err != nil {
		return TrashEntry{}, err
	}
	if err := bin.renameNoReplace(stage, record.payloadPath); err != nil {
		return TrashEntry{}, normalizeNoReplaceError(err)
	}
	record.payloadInfo = stageInfo
	if err := syncDirectories(record.directory, bin.directory); err != nil {
		cleanupRecord = false
		return publicTrashEntry(record), utils.ErrExecutionPartial
	}
	if bin.afterCrossDevicePayloadPublished != nil {
		bin.afterCrossDevicePayloadPublished()
	}
	current, err := validateTrashTree(absolute)
	if err != nil || utils.EntryVersion(current) != utils.EntryVersion(info) {
		cleanupRecord = false
		return publicTrashEntry(record), utils.ErrExecutionPartial
	}
	if err := removeValidatedTree(absolute, current); err != nil {
		cleanupRecord = false
		return publicTrashEntry(record), utils.ErrExecutionPartial
	}
	if err := syncDirectories(filepath.Dir(absolute)); err != nil {
		cleanupRecord = false
		return publicTrashEntry(record), utils.ErrExecutionPartial
	}
	cleanupRecord = false
	return publicTrashEntry(record), nil
}

// Move moves one direct workspace item into the private recycle bin.
func (bin *RecycleBin) Move(rawPath string) (TrashEntry, error) {
	var entry TrashEntry
	err := utils.WithOperationLock(func() error {
		absolute, relative, info, err := utils.ResolveExisting(rawPath, false)
		if err != nil {
			return err
		}
		entry, err = bin.moveResolved(absolute, relative, info)
		return err
	})
	return entry, err
}

// BatchMove retains the existing selection result semantics: independently
// changed entries are skipped, while safe entries still move to the recycle bin.
func (bin *RecycleBin) BatchMove(selection utils.Selection) []utils.ItemResult {
	returnResults := make([]utils.ItemResult, 0, len(selection.Items))
	_ = utils.WithOperationLock(func() error {
		for _, original := range selection.Items {
			one := utils.Selection{Directory: selection.Directory, Items: []utils.SelectedItem{original}}
			refreshed, err := utils.RevalidateSelection(one)
			if err != nil {
				returnResults = append(returnResults, utils.ItemResult{Name: original.Name, Path: original.Relative, Code: utils.ErrorCode(err)})
				continue
			}
			entry, err := bin.moveResolved(refreshed.Items[0].Absolute, refreshed.Items[0].Relative, refreshed.Items[0].Info)
			if err != nil {
				returnResults = append(returnResults, utils.ItemResult{Name: original.Name, Path: original.Relative, Code: utils.ErrorCode(err)})
				continue
			}
			returnResults = append(returnResults, utils.ItemResult{Name: entry.Name, Path: entry.OriginalPath, Code: "trashed"})
		}
		return nil
	})
	return returnResults
}

func (bin *RecycleBin) removeRecord(record trashRecord) error {
	if err := removeValidatedTree(record.payloadPath, record.payloadInfo); err != nil {
		return err
	}
	if err := os.Remove(record.metadataPath); err != nil {
		return utils.ErrExecutionPartial
	}
	if err := os.Remove(record.directory); err != nil {
		return utils.ErrExecutionPartial
	}
	if err := syncDirectories(bin.directory); err != nil {
		return utils.ErrExecutionPartial
	}
	return nil
}

// Purge permanently removes one fully validated recycle-bin record.
func (bin *RecycleBin) Purge(id string) error {
	return utils.WithOperationLock(func() error {
		record, err := bin.loadRecord(id)
		if err != nil {
			return err
		}
		return bin.removeRecord(record)
	})
}

func parentFor(originalPath string) string {
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(originalPath)))
	if parent == "." {
		return ""
	}
	return parent
}

func (bin *RecycleBin) restoreRecord(record trashRecord) (string, error) {
	payloadInfo, err := validateTrashTree(record.payloadPath)
	if err != nil {
		return "", err
	}
	if payloadInfo.IsDir() != record.payloadInfo.IsDir() {
		return "", utils.ErrTrashRecord
	}
	record.payloadInfo = payloadInfo
	parent := parentFor(record.metadata.OriginalPath)
	parentAbsolute, _, _, err := utils.ResolveDirectory(parent, true)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parentAbsolute, record.metadata.Name)
	if info, err := os.Lstat(target); err == nil {
		_ = info
		return "", utils.ErrDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("could not inspect restore destination")
	}
	if crossVolume, err := bin.renameTransfer(record.payloadPath, target, payloadInfo); !crossVolume && err == nil {
		if err := os.Remove(record.metadataPath); err != nil {
			return record.metadata.OriginalPath, utils.ErrExecutionPartial
		}
		if err := os.Remove(record.directory); err != nil {
			return record.metadata.OriginalPath, utils.ErrExecutionPartial
		}
		if err := syncDirectories(parentAbsolute, bin.directory); err != nil {
			return record.metadata.OriginalPath, utils.ErrExecutionPartial
		}
		return record.metadata.OriginalPath, nil
	} else if !crossVolume {
		return "", normalizeNoReplaceError(err)
	}

	stageDirectory, err := os.MkdirTemp(parentAbsolute, utils.InternalTrashRestorePrefix)
	if err != nil {
		return "", errors.New("could not create restore staging directory")
	}
	defer func() { _ = os.RemoveAll(stageDirectory) }()
	stagePayload := filepath.Join(stageDirectory, trashPayloadName)
	if err := copyValidatedTree(record.payloadPath, stagePayload, record.payloadInfo); err != nil {
		return "", err
	}
	if _, err := validateTrashTree(stagePayload); err != nil {
		return "", err
	}
	if err := bin.renameNoReplace(stagePayload, target); err != nil {
		return "", normalizeNoReplaceError(err)
	}
	if err := syncDirectories(parentAbsolute); err != nil {
		return record.metadata.OriginalPath, utils.ErrExecutionPartial
	}
	if err := removeValidatedTree(record.payloadPath, record.payloadInfo); err != nil {
		return record.metadata.OriginalPath, utils.ErrExecutionPartial
	}
	if err := os.Remove(record.metadataPath); err != nil {
		return record.metadata.OriginalPath, utils.ErrExecutionPartial
	}
	if err := os.Remove(record.directory); err != nil {
		return record.metadata.OriginalPath, utils.ErrExecutionPartial
	}
	if err := syncDirectories(bin.directory); err != nil {
		return record.metadata.OriginalPath, utils.ErrExecutionPartial
	}
	return record.metadata.OriginalPath, nil
}

// Restore publishes a recycled payload only at its original path and only when
// the original parent remains a safe directory with no conflicting target.
func (bin *RecycleBin) Restore(id string) (string, error) {
	var path string
	err := utils.WithOperationLock(func() error {
		record, err := bin.loadRecord(id)
		if err != nil {
			return err
		}
		path, err = bin.restoreRecord(record)
		return err
	})
	return path, err
}

// Empty permanently removes every valid listed record. Malformed records stay
// private and untouched so the service never recursively deletes unknown data.
func (bin *RecycleBin) Empty() (int, []utils.ItemResult, error) {
	var affected int
	var results []utils.ItemResult
	err := utils.WithOperationLock(func() error {
		records, err := bin.listRecords()
		if err != nil {
			return err
		}
		results = make([]utils.ItemResult, 0, len(records))
		for _, record := range records {
			if err := bin.removeRecord(record); err != nil {
				results = append(results, utils.ItemResult{Name: record.metadata.Name, Path: record.metadata.OriginalPath, Code: utils.ErrorCode(err)})
				continue
			}
			affected++
			results = append(results, utils.ItemResult{Name: record.metadata.Name, Path: record.metadata.OriginalPath, Code: "purged"})
		}
		return nil
	})
	if err != nil {
		return affected, results, err
	}
	for _, result := range results {
		if result.Code != "purged" {
			return affected, results, utils.ErrExecutionPartial
		}
	}
	return affected, results, nil
}
