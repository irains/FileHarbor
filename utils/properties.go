package utils

import (
	"context"
	"github.com/irains/fileharbor/auth"
	"path/filepath"
	"strings"
)

// Properties holds read-only, portable information about a managed item.
type Properties struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Extension  string `json:"extension,omitempty"`
	Size       int64  `json:"size"`
	Modified   int64  `json:"modified"`
	Mode       string `json:"mode"`
	EntryCount int    `json:"entry_count,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

func GetProperties(rawPath string) (Properties, error) {
	return GetPropertiesContext(context.Background(), rawPath)
}

func GetPropertiesContext(ctx context.Context, rawPath string) (Properties, error) {
	return propertiesContext(ctx, rawPath, true)
}

func GetBasicProperties(rawPath string) (Properties, error) {
	return propertiesContext(context.Background(), rawPath, false)
}

func propertiesContext(ctx context.Context, rawPath string, recursive bool) (Properties, error) {
	absolute, rel, _, err := ResolveExisting(rawPath, false)
	if err != nil {
		return Properties{}, err
	}
	info, err := safeEntryInfo(absolute)
	if err != nil {
		return Properties{}, err
	}
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	properties := Properties{
		Name:      filepath.Base(rel),
		Path:      rel,
		Kind:      kind,
		Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), "."),
		Size:      info.Size(),
		Modified:  info.ModTime().Unix(),
		Mode:      info.Mode().String(),
	}
	if !info.IsDir() {
		return properties, nil
	}
	if !recursive {
		properties.Size = 0
		return properties, nil
	}
	result, err := ScanDirectorySize(ctx, rel)
	if err != nil {
		return Properties{}, err
	}
	properties.Size = result.Bytes
	properties.EntryCount = result.Files + result.Directories
	properties.Incomplete = result.Incomplete
	return properties, nil
}

// SelectionFromArchiveItems revalidates a one-use ticket immediately before ZIP
// streaming. It rejects changed items rather than silently archiving a new file.
func SelectionFromArchiveItems(items []auth.ArchiveItem) (Selection, error) {
	if len(items) == 0 || len(items) > MaxListEntries {
		return Selection{}, operationError("invalid_selection")
	}
	selection := Selection{}
	var directory string
	for _, item := range items {
		absolute, rel, _, err := ResolveExisting(item.Path, false)
		if err != nil {
			return Selection{}, err
		}
		info, err := safeEntryInfo(absolute)
		if err != nil {
			return Selection{}, err
		}
		if versionFor(info) != item.Version {
			return Selection{}, ErrSourceChanged
		}
		itemDirectory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
		if itemDirectory == "." {
			itemDirectory = ""
		}
		if len(selection.Items) == 0 {
			directory = itemDirectory
		} else if directory != itemDirectory {
			return Selection{}, operationError("invalid_selection")
		}
		selection.Items = append(selection.Items, SelectedItem{Name: filepath.Base(rel), Relative: rel, Absolute: absolute, Info: info})
	}
	selection.Directory = directory
	return revalidateSelection(selection)
}
