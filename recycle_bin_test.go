package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/irains/fileharbor/conf"
	"github.com/irains/fileharbor/utils"
)

func useRecycleBin(t *testing.T) (*RecycleBin, string, *RuntimeState) {
	t.Helper()
	previousRoot := conf.FileHarbor
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Match the canonical paths used by Root and OpenRuntimeState. Windows
	// temporary paths may contain an 8.3 alias (for example RUNNER~1).
	conf.FileHarbor = root
	t.Cleanup(func() { conf.FileHarbor = previousRoot })
	state, err := OpenRuntimeState(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	bin, err := newRecycleBin(state)
	if err != nil {
		t.Fatal(err)
	}
	return bin, root, state
}

func TestRecycleBinMovesListsRestoresAndPersists(t *testing.T) {
	bin, root, state := useRecycleBin(t)
	if err := os.Mkdir(filepath.Join(root, "reports"), 0755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "reports", "notes.txt")
	if err := os.WriteFile(original, []byte("contents"), 0644); err != nil {
		t.Fatal(err)
	}
	entry, err := bin.Move("reports/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID == "" || entry.OriginalPath != "reports/notes.txt" || entry.Name != "notes.txt" || entry.Kind != "file" {
		t.Fatalf("trash entry = %#v", entry)
	}
	if _, err := os.Lstat(original); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remained after trash move: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(state.TrashDir, entry.ID, trashPayloadName)); err != nil {
		t.Fatalf("private payload missing: %v", err)
	}
	page, err := bin.List("")
	if err != nil || len(page.Entries) != 1 || page.Entries[0].ID != entry.ID {
		t.Fatalf("recycle-bin list = %#v, %v", page, err)
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
	if page, err = bin.List(""); err != nil || len(page.Entries) != 1 || page.Entries[0].ID != entry.ID {
		t.Fatalf("reopened recycle-bin list = %#v, %v", page, err)
	}
	restored, err := bin.Restore(entry.ID)
	if err != nil || restored != "reports/notes.txt" {
		t.Fatalf("restore = %q, %v", restored, err)
	}
	contents, err := os.ReadFile(original)
	if err != nil || string(contents) != "contents" {
		t.Fatalf("restored content = %q, %v", contents, err)
	}
	if page, err = bin.List(""); err != nil || len(page.Entries) != 0 {
		t.Fatalf("recycle-bin retained restored entry = %#v, %v", page, err)
	}
}

func TestRecycleBinRestoreNeverOverwritesDestination(t *testing.T) {
	bin, root, _ := useRecycleBin(t)
	original := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(original, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	entry, err := bin.Move("notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := bin.Restore(entry.ID); utils.ErrorCode(err) != "destination_exists" {
		t.Fatalf("restore conflict = %v", err)
	}
	contents, err := os.ReadFile(original)
	if err != nil || string(contents) != "new" {
		t.Fatalf("restore overwrote destination = %q, %v", contents, err)
	}
	if page, err := bin.List(""); err != nil || len(page.Entries) != 1 {
		t.Fatalf("conflicted record was removed = %#v, %v", page, err)
	}
}

func TestRecycleBinRejectsUnsafeRecordsAndDoesNotCleanThem(t *testing.T) {
	bin, _, state := useRecycleBin(t)
	unsafe := filepath.Join(state.TrashDir, "0123456789abcdef0123456789abcdef")
	if err := os.Mkdir(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsafe, "unrecognized"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if page, err := bin.List(""); err != nil || len(page.Entries) != 0 {
		t.Fatalf("unsafe record leaked into listing = %#v, %v", page, err)
	}
	if _, err := os.Lstat(filepath.Join(unsafe, "unrecognized")); err != nil {
		t.Fatalf("unsafe record was removed: %v", err)
	}
	if err := bin.Purge("0123456789abcdef0123456789abcdef"); utils.ErrorCode(err) != "trash_record_invalid" {
		t.Fatalf("unsafe purge = %v", err)
	}
}

func TestRecycleBinBatchRetainsIndependentSafeEntries(t *testing.T) {
	bin, root, _ := useRecycleBin(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0644); err != nil {
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

func TestRecycleBinCrossDeviceMoveReportsPartialAfterPayloadPublication(t *testing.T) {
	bin, root, _ := useRecycleBin(t)
	original := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(original, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	crossDevice := errors.New("cross-device test transfer")
	renameNoReplace := bin.renameNoReplace
	calls := 0
	bin.renameNoReplace = func(source, destination string) error {
		calls++
		if calls == 1 {
			return crossDevice
		}
		return renameNoReplace(source, destination)
	}
	bin.isCrossDeviceError = func(err error) bool { return errors.Is(err, crossDevice) }
	bin.afterCrossDevicePayloadPublished = func() {
		if err := os.WriteFile(original, []byte("changed after payload publication"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	entry, err := bin.Move("notes.txt")
	if utils.ErrorCode(err) != "execution_partial" {
		t.Fatalf("cross-device source change = entry %#v, error %v", entry, err)
	}
	if entry.ID == "" {
		t.Fatalf("partial result did not identify the durable recycle record: %#v", entry)
	}
	if contents, readErr := os.ReadFile(original); readErr != nil || string(contents) != "changed after payload publication" {
		t.Fatalf("changed source was removed or altered: %q, %v", contents, readErr)
	}
	page, listErr := bin.List("")
	if listErr != nil || len(page.Entries) != 1 || page.Entries[0].ID != entry.ID {
		t.Fatalf("published recycle record was not recoverable: %#v, %v", page, listErr)
	}
}
