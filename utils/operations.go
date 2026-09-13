package utils

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var operationMu = newOperationGate()

// WithOperationLock serializes filesystem mutations that share managed sources.
// Callers must keep the callback small and must not invoke another exported
// operation helper that acquires this lock itself.
func WithOperationLock(operation func() error) error {
	operationMu.Lock()
	defer operationMu.Unlock()
	return operation()
}

// ItemRequest is a direct-child item identified by a short-lived listing token.
type ItemRequest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ItemResult reports an operation's truth without leaking absolute filesystem paths.
type ItemResult struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	Code string `json:"code"`
}

// Selection is the fully resolved, revalidated form of a batch request.
type Selection struct {
	Directory string
	Items     []SelectedItem
}

type SelectedItem struct {
	Name     string
	Relative string
	Absolute string
	Info     os.FileInfo
}

// ValidateSelection rebuilds sources from a trusted listing directory, verifies
// versions, and rejects potentially ambiguous or unsafe requests before writes.
func ValidateSelection(directory string, allowed map[string]string, requested []ItemRequest) (Selection, error) {
	if len(requested) == 0 {
		return Selection{}, operationError("invalid_selection")
	}
	if len(requested) > MaxListEntries {
		return Selection{}, ErrBatchLimitExceeded
	}
	dirAbs, dirRel, _, err := ResolveDirectory(directory, true)
	if err != nil {
		return Selection{}, err
	}
	seen := make(map[string]struct{}, len(requested))
	selection := Selection{Directory: dirRel}
	for _, req := range requested {
		if ValidateLeafName(req.Name) != nil {
			return Selection{}, operationError("invalid_selection")
		}
		if _, duplicate := seen[req.Name]; duplicate {
			return Selection{}, operationError("invalid_selection")
		}
		seen[req.Name] = struct{}{}
		expected, ok := allowed[req.Name]
		if !ok || expected != req.Version {
			return Selection{}, ErrSourceChanged
		}
		absolute := filepath.Join(dirAbs, req.Name)
		fileInfo, err := safeEntryInfo(absolute)
		if err != nil {
			return Selection{}, err
		}
		if versionFor(fileInfo) != req.Version {
			return Selection{}, ErrSourceChanged
		}
		rel, err := relativeToRoot(absolute)
		if err != nil {
			return Selection{}, err
		}
		selection.Items = append(selection.Items, SelectedItem{Name: req.Name, Relative: rel, Absolute: absolute, Info: fileInfo})
	}
	sort.Slice(selection.Items, func(i, j int) bool { return selection.Items[i].Relative < selection.Items[j].Relative })
	return selection, nil
}

func destinationFor(destination string, source SelectedItem, selection Selection) (string, string, error) {
	dirAbs, dirRel, _, err := ResolveDirectory(destination, true)
	if err != nil {
		return "", "", err
	}
	if dirRel == selection.Directory {
		return "", "", operationError("destination_same_directory")
	}
	target := filepath.Join(dirAbs, source.Name)
	root, err := Root()
	if err != nil || !contained(root, target) {
		return "", "", ErrInvalidPath
	}
	if _, err := os.Lstat(target); err == nil {
		return "", "", ErrDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", "", operationError("io_error")
	}
	if source.Info.IsDir() && (dirAbs == source.Absolute || strings.HasPrefix(dirAbs, source.Absolute+string(filepath.Separator))) {
		return "", "", ErrSelfDescendant
	}
	return target, filepath.ToSlash(filepath.Join(dirRel, source.Name)), nil
}

func validateDestination(selection Selection, destination string) ([]string, []string, error) {
	absolute := make([]string, 0, len(selection.Items))
	relative := make([]string, 0, len(selection.Items))
	for _, item := range selection.Items {
		target, rel, err := destinationFor(destination, item, selection)
		if err != nil {
			return nil, nil, err
		}
		absolute = append(absolute, target)
		relative = append(relative, rel)
	}
	return absolute, relative, nil
}

// revalidateSelection refreshes every source while an operation lock is held. A
// listing token is only an authorization envelope: its entries must still be the
// same direct children when an irreversible operation begins.
func RevalidateSelection(selection Selection) (Selection, error) {
	return revalidateSelection(selection)
}

func revalidateSelection(selection Selection) (Selection, error) {
	if len(selection.Items) == 0 || len(selection.Items) > MaxListEntries {
		return Selection{}, operationError("invalid_selection")
	}
	directory, err := CleanRelative(selection.Directory, true)
	if err != nil || directory != selection.Directory {
		return Selection{}, ErrInvalidPath
	}
	refreshed := Selection{Directory: directory, Items: make([]SelectedItem, 0, len(selection.Items))}
	seen := make(map[string]struct{}, len(selection.Items))
	for _, item := range selection.Items {
		if item.Info == nil || ValidateLeafName(item.Name) != nil {
			return Selection{}, operationError("invalid_selection")
		}
		if _, duplicate := seen[item.Name]; duplicate {
			return Selection{}, operationError("invalid_selection")
		}
		seen[item.Name] = struct{}{}
		relative, err := relativeToRoot(item.Absolute)
		if err != nil {
			return Selection{}, err
		}
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
		if parent == "." {
			parent = ""
		}
		if relative != item.Relative || filepath.Base(filepath.FromSlash(relative)) != item.Name || parent != directory {
			return Selection{}, ErrSourceChanged
		}
		info, err := safeEntryInfo(item.Absolute)
		if err != nil {
			return Selection{}, err
		}
		if versionFor(info) != versionFor(item.Info) {
			return Selection{}, ErrSourceChanged
		}
		item.Info = info
		refreshed.Items = append(refreshed.Items, item)
	}
	return refreshed, nil
}

// RenameItem atomically renames a single non-root direct target when its final
// destination does not already exist.
func RenameItem(rawPath, name string) (string, error) {
	if err := ValidateLeafName(name); err != nil {
		return "", err
	}
	operationMu.Lock()
	defer operationMu.Unlock()
	absolute, rel, _, err := ResolveExisting(rawPath, false)
	if err != nil {
		return "", err
	}
	info, err := safeEntryInfo(absolute)
	if err != nil {
		return "", err
	}
	_ = info
	target := filepath.Join(filepath.Dir(absolute), name)
	if _, err := os.Lstat(target); err == nil {
		return "", ErrDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", operationError("io_error")
	}
	if err := os.Rename(absolute, target); err != nil {
		return "", operationError("io_error")
	}
	return filepath.ToSlash(filepath.Join(filepath.Dir(rel), name)), nil
}

// MoveItem performs a safe same-volume rename. If the operating system reports a
// cross-device rename, it stages a complete copy on the destination volume before
// publishing it and only then removes the source.
func MoveItem(rawPath, destination, optionalName string) (string, error) {
	operationMu.Lock()
	defer operationMu.Unlock()
	absolute, rel, _, err := ResolveExisting(rawPath, false)
	if err != nil {
		return "", err
	}
	info, err := safeEntryInfo(absolute)
	if err != nil {
		return "", err
	}
	name := filepath.Base(rel)
	if optionalName != "" {
		if err := ValidateLeafName(optionalName); err != nil {
			return "", err
		}
		name = optionalName
	}
	destAbs, destRel, _, err := ResolveDirectory(destination, true)
	if err != nil {
		return "", err
	}
	if info.IsDir() && (destAbs == absolute || strings.HasPrefix(destAbs, absolute+string(filepath.Separator))) {
		return "", ErrSelfDescendant
	}
	target := filepath.Join(destAbs, name)
	if _, err := os.Lstat(target); err == nil {
		return "", ErrDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", operationError("io_error")
	}
	if err := os.Rename(absolute, target); err == nil {
		return filepath.ToSlash(filepath.Join(destRel, name)), nil
	} else if !isCrossDeviceError(err) {
		return "", operationError("io_error")
	}
	stage, err := os.MkdirTemp(destAbs, ".fileharbor-move-")
	if err != nil {
		return "", operationError("io_error")
	}
	defer func() {
		if info, statErr := os.Lstat(stage); statErr == nil {
			if info.IsDir() {
				_ = os.RemoveAll(stage)
			} else {
				_ = os.Remove(stage)
			}
		}
	}()
	stagedTarget := filepath.Join(stage, name)
	if err := copyEntry(absolute, stagedTarget, info); err != nil {
		return "", err
	}
	if err := os.Rename(stagedTarget, target); err != nil {
		return "", operationError("io_error")
	}
	if info.IsDir() {
		err = os.RemoveAll(absolute)
	} else {
		err = os.Remove(absolute)
	}
	if err != nil {
		if rollbackErr := os.Rename(target, absolute); rollbackErr != nil {
			return "", operationError("execution_partial")
		}
		return "", operationError("io_error")
	}
	return filepath.ToSlash(filepath.Join(destRel, name)), nil
}

func CopyItem(rawPath, destination, optionalName string) (string, error) {
	operationMu.Lock()
	defer operationMu.Unlock()
	absolute, rel, _, err := ResolveExisting(rawPath, false)
	if err != nil {
		return "", err
	}
	info, err := safeEntryInfo(absolute)
	if err != nil {
		return "", err
	}
	name := filepath.Base(rel)
	if optionalName != "" {
		if err := ValidateLeafName(optionalName); err != nil {
			return "", err
		}
		name = optionalName
	}
	destAbs, destRel, _, err := ResolveDirectory(destination, true)
	if err != nil {
		return "", err
	}
	if info.IsDir() && (destAbs == absolute || strings.HasPrefix(destAbs, absolute+string(filepath.Separator))) {
		return "", ErrSelfDescendant
	}
	target := filepath.Join(destAbs, name)
	if _, err := os.Lstat(target); err == nil {
		return "", ErrDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", operationError("io_error")
	}
	if err := copyEntry(absolute, target, info); err != nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			if info.IsDir() {
				_ = os.RemoveAll(target)
			} else {
				_ = os.Remove(target)
			}
		}
		return "", err
	}
	return filepath.ToSlash(filepath.Join(destRel, name)), nil
}

// BatchCopy validates every target before changing anything. Outputs first land
// under private names and are promoted only after all copies finish successfully.
func BatchCopy(selection Selection, destination string) ([]string, error) {
	operationMu.Lock()
	defer operationMu.Unlock()
	selection, err := revalidateSelection(selection)
	if err != nil {
		return nil, err
	}
	targets, rels, err := validateDestination(selection, destination)
	if err != nil {
		return nil, err
	}
	staged := make([]string, 0, len(targets))
	cleanupStaged := func() {
		for _, path := range staged {
			info, err := os.Lstat(path)
			if err != nil {
				continue
			}
			if info.IsDir() {
				_ = os.RemoveAll(path)
			} else {
				_ = os.Remove(path)
			}
		}
	}
	for i, item := range selection.Items {
		stage := targets[i] + ".fileharbor-copy-staging"
		if _, err := os.Lstat(stage); err == nil {
			cleanupStaged()
			return nil, operationError("destination_busy")
		} else if !errors.Is(err, fs.ErrNotExist) {
			cleanupStaged()
			return nil, operationError("io_error")
		}
		staged = append(staged, stage)
		if err := copyEntry(item.Absolute, stage, item.Info); err != nil {
			cleanupStaged()
			return nil, err
		}
	}
	promoted := make([]string, 0, len(targets))
	for i, stage := range staged {
		if err := os.Rename(stage, targets[i]); err != nil {
			for _, path := range promoted {
				info, statErr := os.Lstat(path)
				if statErr != nil {
					continue
				}
				if info.IsDir() {
					_ = os.RemoveAll(path)
				} else {
					_ = os.Remove(path)
				}
			}
			cleanupStaged()
			return nil, operationError("execution_partial")
		}
		promoted = append(promoted, targets[i])
	}
	return rels, nil
}

// BatchMove permits only native rename semantics. On Windows, volume names make
// cross-volume moves detectable before changing any item. On other platforms,
// os.Rename is used and any failure triggers the existing best-effort rollback.
func BatchMove(selection Selection, destination string) ([]string, error) {
	operationMu.Lock()
	defer operationMu.Unlock()
	selection, err := revalidateSelection(selection)
	if err != nil {
		return nil, err
	}
	targets, rels, err := validateDestination(selection, destination)
	if err != nil {
		return nil, err
	}
	if err := ensureSameVolume(selection, destination); err != nil {
		return nil, err
	}
	for i, item := range selection.Items {
		if err := os.Rename(item.Absolute, targets[i]); err != nil {
			for j := i - 1; j >= 0; j-- {
				_ = os.Rename(targets[j], selection.Items[j].Absolute)
			}
			return nil, operationError("execution_partial")
		}
	}
	return rels, nil
}

// BatchDelete preflights every item before starting deletion, then reports the
// actual outcome per item because a filesystem cannot make permanent deletion
// transactional. An item that changed after the browser listing is skipped rather
// than allowing it to invalidate deletion of independent, still-valid items.
func BatchDelete(selection Selection) []ItemResult {
	operationMu.Lock()
	defer operationMu.Unlock()

	results := make([]ItemResult, 0, len(selection.Items))
	if len(selection.Items) == 0 || len(selection.Items) > MaxListEntries {
		return []ItemResult{{Code: "invalid_selection"}}
	}
	directory, err := CleanRelative(selection.Directory, true)
	if err != nil || directory != selection.Directory {
		for _, item := range selection.Items {
			results = append(results, ItemResult{Name: item.Name, Code: ErrInvalidPath.Code})
		}
		return results
	}

	seen := make(map[string]struct{}, len(selection.Items))
	valid := make([]SelectedItem, 0, len(selection.Items))
	for _, item := range selection.Items {
		if item.Info == nil || ValidateLeafName(item.Name) != nil {
			results = append(results, ItemResult{Name: item.Name, Code: "invalid_selection"})
			continue
		}
		if _, duplicate := seen[item.Name]; duplicate {
			results = append(results, ItemResult{Name: item.Name, Code: "invalid_selection"})
			continue
		}
		seen[item.Name] = struct{}{}
		relative, err := relativeToRoot(item.Absolute)
		if err != nil {
			results = append(results, ItemResult{Name: item.Name, Code: ErrorCode(err)})
			continue
		}
		parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
		if parent == "." {
			parent = ""
		}
		if relative != item.Relative || filepath.Base(filepath.FromSlash(relative)) != item.Name || parent != directory {
			results = append(results, ItemResult{Name: item.Name, Code: ErrSourceChanged.Code})
			continue
		}
		info, err := safeEntryInfo(item.Absolute)
		if err != nil {
			results = append(results, ItemResult{Name: item.Name, Code: ErrorCode(err)})
			continue
		}
		if versionFor(info) != versionFor(item.Info) {
			results = append(results, ItemResult{Name: item.Name, Code: ErrSourceChanged.Code})
			continue
		}
		item.Info = info
		valid = append(valid, item)
	}

	for _, item := range valid {
		info, err := safeEntryInfo(item.Absolute)
		if err != nil {
			results = append(results, ItemResult{Name: item.Name, Code: ErrorCode(err)})
			continue
		}
		if versionFor(info) != versionFor(item.Info) {
			results = append(results, ItemResult{Name: item.Name, Code: ErrSourceChanged.Code})
			continue
		}
		if info.IsDir() {
			err = os.RemoveAll(item.Absolute)
		} else {
			err = os.Remove(item.Absolute)
		}
		if err != nil {
			results = append(results, ItemResult{Name: item.Name, Code: "failed"})
			continue
		}
		results = append(results, ItemResult{Name: item.Name, Code: "deleted"})
	}
	return results
}

func ensureSameVolume(selection Selection, destination string) error {
	if len(selection.Items) == 0 {
		return operationError("invalid_selection")
	}
	destinationAbsolute, _, _, err := ResolveDirectory(destination, true)
	if err != nil {
		return err
	}
	if filepath.VolumeName(selection.Items[0].Absolute) != "" && filepath.VolumeName(selection.Items[0].Absolute) != filepath.VolumeName(destinationAbsolute) {
		return operationError("cross_device_move")
	}
	return nil
}

// IsCrossDeviceError reports whether a native rename failed because its source
// and target are on different filesystem volumes.
func IsCrossDeviceError(err error) bool {
	return isCrossDeviceError(err)
}

// RenameNoReplace publishes a source at an absent destination without allowing
// a concurrent destination to be replaced. Unsupported platforms fail closed.
func RenameNoReplace(source, destination string) error {
	return renameNoReplace(source, destination)
}

func copyEntry(source, target string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return ErrUnsupportedType
	}
	if info.IsDir() {
		if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
			return operationError("io_error")
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return operationError("io_error")
		}
		for _, entry := range entries {
			childSource := filepath.Join(source, entry.Name())
			childInfo, err := safeEntryInfo(childSource)
			if err != nil {
				return err
			}
			if err := copyEntry(childSource, filepath.Join(target, entry.Name()), childInfo); err != nil {
				return err
			}
		}
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return operationError("io_error")
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		_ = input.Close()
		return operationError("io_error")
	}
	limited := io.LimitReader(input, info.Size()+1)
	written, copyErr := io.Copy(output, limited)
	closeErr := output.Close()
	inputCloseErr := input.Close()
	if copyErr != nil || closeErr != nil || inputCloseErr != nil {
		return operationError("io_error")
	}
	if written != info.Size() {
		return ErrSourceChanged
	}
	return nil
}

// MakeDirectory creates one safe direct child below an existing target directory.
func MakeDirectory(parent, name string) (string, error) {
	if err := ValidateLeafName(name); err != nil {
		return "", err
	}
	operationMu.Lock()
	defer operationMu.Unlock()
	dir, rel, _, err := ResolveDirectory(parent, true)
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, name)
	if err := os.Mkdir(target, 0755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", ErrDestinationExists
		}
		return "", operationError("io_error")
	}
	return filepath.ToSlash(filepath.Join(rel, name)), nil
}

func MakeFile(parent, name string) (string, error) {
	if err := ValidateLeafName(name); err != nil {
		return "", err
	}
	operationMu.Lock()
	defer operationMu.Unlock()
	dir, rel, _, err := ResolveDirectory(parent, true)
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, name)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", ErrDestinationExists
		}
		return "", operationError("io_error")
	}
	if err := file.Close(); err != nil {
		return "", operationError("io_error")
	}
	return filepath.ToSlash(filepath.Join(rel, name)), nil
}
