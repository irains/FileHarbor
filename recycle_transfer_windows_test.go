//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/irains/fileharbor/conf"
	"github.com/irains/fileharbor/utils"
)

func TestWindowsRecycleAcrossVolumes(t *testing.T) {
	parent := os.Getenv("FILEHARBOR_TEST_OTHER_VOLUME")
	if parent == "" {
		t.Skip("requires an explicitly configured temporary parent on another volume")
	}
	for _, kind := range []string{"empty", "nested", "file"} {
		t.Run(kind, func(t *testing.T) {
			stateParent := t.TempDir()
			other, err := os.MkdirTemp(parent, "fileharbor-volume-test-")
			if err != nil {
				t.Fatal("create other-volume fixture failed")
			}
			t.Cleanup(func() { _ = os.RemoveAll(other) })
			root := other
			fromVolume, fromOK := trashDirectoryVolume(root)
			toVolume, toOK := trashDirectoryVolume(stateParent)
			if !fromOK || !toOK || fromVolume == toVolume {
				t.Fatal("fixture must use two confirmed distinct volumes")
			}
			previous := conf.FileHarbor
			conf.FileHarbor = root
			t.Cleanup(func() { conf.FileHarbor = previous })
			state, err := OpenRuntimeState(stateParent, root)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			bin, err := newRecycleBin(state)
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "sample")
			if kind == "file" {
				err = os.WriteFile(source, []byte("contents"), 0600)
			} else {
				err = os.Mkdir(source, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "nested" {
				if err := os.MkdirAll(filepath.Join(source, "child", "empty"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, "child", "data"), []byte("contents"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			native := bin.renameNoReplace
			bin.renameNoReplace = func(from, to string) error {
				err := native(from, to)
				var errno syscall.Errno
				if errors.As(err, &errno) {
					t.Logf("native rename: errno=%d directory=%t", uint32(errno), kind != "file")
				}
				return err
			}
			entry, err := bin.Move("sample")
			if err != nil {
				t.Fatalf("move code=%s", utils.ErrorCode(err))
			}
			if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("source still exists")
			}
			if _, err := bin.Restore(entry.ID); err != nil {
				t.Fatalf("restore code=%s", utils.ErrorCode(err))
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatal("restored item missing")
			}
			if kind == "nested" {
				data, err := os.ReadFile(filepath.Join(source, "child", "data"))
				if err != nil || string(data) != "contents" {
					t.Fatal("restored contents differ")
				}
				if info, err := os.Stat(filepath.Join(source, "child", "empty")); err != nil || !info.IsDir() {
					t.Fatal("empty child missing")
				}
			}
			page, err := bin.List("")
			if err != nil || len(page.Entries) != 0 {
				t.Fatal("restored record remains")
			}
		})
	}
}

func TestWindowsTrashVolumeDetectionIsConservative(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{source, filepath.Join(root, "target")}, {filepath.Join(root, "missing"), filepath.Join(root, "target")}, {source, filepath.Join(root, "missing", "target")}} {
		if confirmedTrashCrossVolume(pair[0], pair[1]) {
			t.Fatal("same or unknown volume classified as different")
		}
	}
	bin, _, _ := useRecycleBin(t)
	nativeCalled := false
	bin.renameNoReplace = func(string, string) error { nativeCalled = true; return syscall.ERROR_ACCESS_DENIED }
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	cross, err := bin.renameTransfer(source, filepath.Join(root, "target"), info)
	if !nativeCalled || cross || !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatal("access denied incorrectly entered fallback")
	}
}
