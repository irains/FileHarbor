package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/irains/fileharbor/utils"
)

func seedTrashDirectory(t *testing.T, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(source, "nested", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"zero": "", "nested/data": "contents"} {
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecycleDirectoryLifecycle(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "native"
		if fallback {
			name = "injected-cross-device"
		}
		t.Run(name, func(t *testing.T) {
			bin, root, state := useRecycleBin(t)
			source := filepath.Join(root, "folder")
			seedTrashDirectory(t, source)
			inject := func(bin *RecycleBin) {
				native := bin.renameNoReplace
				cross := errors.New("test cross-device")
				bin.isCrossDeviceError = func(err error) bool { return errors.Is(err, cross) }
				bin.renameNoReplace = func(from, to string) error {
					if from == source || to == source && filepath.Base(from) == trashPayloadName && filepath.Dir(filepath.Dir(from)) == bin.directory {
						return cross
					}
					return native(from, to)
				}
			}
			if fallback {
				inject(bin)
			}
			entry, err := bin.Move("folder")
			if err != nil {
				t.Fatal(err)
			}
			if entry.Kind != "directory" || entry.SizeBytes != 8 || entry.OriginalPath != "folder" {
				t.Fatalf("entry=%#v", entry)
			}
			if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("source remains")
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenRuntimeState(state.Dir, root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			bin, err = newRecycleBin(reopened)
			if err != nil {
				t.Fatal(err)
			}
			if fallback {
				inject(bin)
			}
			page, err := bin.List("")
			if err != nil || len(page.Entries) != 1 || page.Entries[0].ID != entry.ID {
				t.Fatal("record did not persist")
			}
			if _, err := bin.Restore(entry.ID); err != nil {
				t.Fatal(err)
			}
			for name, expected := range map[string]string{"zero": "", "nested/data": "contents"} {
				data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
				if err != nil || string(data) != expected {
					t.Fatal("restored content differs")
				}
			}
			if info, err := os.Stat(filepath.Join(source, "nested", "empty")); err != nil || !info.IsDir() {
				t.Fatal("empty directory missing")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatal("restore staging remains")
			}
			entries, err = os.ReadDir(bin.directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("record remains")
			}
		})
	}
}

func TestRecycleDirectoryTransferFailures(t *testing.T) {
	for _, scenario := range []string{"access-denied", "publication", "source-changed", "restore-conflict", "restore-race"} {
		t.Run(scenario, func(t *testing.T) {
			bin, root, _ := useRecycleBin(t)
			source := filepath.Join(root, "folder")
			seedTrashDirectory(t, source)
			native := bin.renameNoReplace
			cross := errors.New("test cross-device")
			bin.isCrossDeviceError = func(err error) bool { return errors.Is(err, cross) }
			bin.renameNoReplace = func(from, to string) error {
				if from == source {
					if scenario == "access-denied" {
						return os.ErrPermission
					}
					return cross
				}
				if scenario == "publication" && filepath.Base(from) == trashPayloadStageName {
					return os.ErrPermission
				}
				return native(from, to)
			}
			if scenario == "source-changed" {
				bin.afterCrossDevicePayloadPublished = func() {
					stamp := time.Now().Add(time.Hour)
					if err := os.Chtimes(source, stamp, stamp); err != nil {
						t.Fatal(err)
					}
				}
			}
			entry, err := bin.Move("folder")
			switch scenario {
			case "access-denied", "publication":
				if utils.ErrorCode(err) != "io_error" {
					t.Fatalf("error=%v", err)
				}
				if data, err := os.ReadFile(filepath.Join(source, "nested", "data")); err != nil || string(data) != "contents" {
					t.Fatal("source changed")
				}
				records, err := os.ReadDir(bin.directory)
				if err != nil || len(records) != 0 {
					t.Fatal("unpublished record remains")
				}
			case "source-changed":
				if utils.ErrorCode(err) != "execution_partial" || entry.ID == "" {
					t.Fatalf("result=%#v %v", entry, err)
				}
				page, err := bin.List("")
				if err != nil || len(page.Entries) != 1 {
					t.Fatal("recoverable payload missing")
				}
				if _, err := os.Stat(source); err != nil {
					t.Fatal("source missing")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				createTarget := func() {
					if err := os.Mkdir(source, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(source, "sentinel"), []byte("new"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "restore-conflict" {
					createTarget()
				} else {
					bin.renameNoReplace = func(from, to string) error {
						if to == source {
							if filepath.Dir(filepath.Dir(from)) == bin.directory {
								return cross
							}
							createTarget()
						}
						return native(from, to)
					}
				}
				if _, err := bin.Restore(entry.ID); utils.ErrorCode(err) != "destination_exists" {
					t.Fatalf("restore error=%v", err)
				}
				if data, err := os.ReadFile(filepath.Join(source, "sentinel")); err != nil || string(data) != "new" {
					t.Fatal("destination overwritten")
				}
				page, err := bin.List("")
				if err != nil || len(page.Entries) != 1 {
					t.Fatal("conflicted payload lost")
				}
			}
		})
	}
}

func TestRecycleDirectoriesBatchRetainsIndependentSafeEntries(t *testing.T) {
	bin, root, _ := useRecycleBin(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listing, err := utils.ListDirectory("")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]string{}
	for _, entry := range listing.Entries {
		allowed[entry.Name] = entry.Version
	}
	selection, err := utils.ValidateSelection("", allowed, []utils.ItemRequest{{Name: "a.txt", Version: allowed["a.txt"]}, {Name: "b.txt", Version: allowed["b.txt"]}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	results := bin.BatchMove(selection)
	codes := map[string]string{}
	for _, result := range results {
		codes[result.Name] = result.Code
	}
	if codes["a.txt"] != "not_found" || codes["b.txt"] != "trashed" {
		t.Fatalf("batch recycle results = %#v", results)
	}
	if _, err := os.Lstat(filepath.Join(root, "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("safe batch item remained in workspace: %v", err)
	}
}

func TestRecycleDirectoryRejectsUnsafeChild(t *testing.T) {
	bin, root, _ := useRecycleBin(t)
	source := filepath.Join(root, "folder")
	seedTrashDirectory(t, source)
	if err := os.WriteFile(filepath.Join(source, utils.InternalTrashRestorePrefix+"reserved"), []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := bin.Move("folder"); !errors.Is(err, utils.ErrUnsupportedType) {
		t.Fatalf("unsafe child error=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(source, "nested", "data")); err != nil || string(data) != "contents" {
		t.Fatal("unsafe source changed")
	}
	records, err := os.ReadDir(bin.directory)
	if err != nil || len(records) != 0 {
		t.Fatal("unsafe record created")
	}
}
