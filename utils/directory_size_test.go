package utils

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectorySizeCountsMetadataBeyondArchiveLimit(t *testing.T) {
	root := withArchiveRoot(t)
	directory := filepath.Join(root, "folder")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(directory, "large"))
	if err != nil {
		t.Fatal(err)
	}
	// Truncate creates synthetic logical content without writing a multi-GiB payload.
	if err := file.Truncate(3 << 30); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "sibling"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ScanDirectorySize(context.Background(), "folder")
	if err != nil || result.Incomplete || result.Bytes != (3<<30)+4 || result.Files != 2 {
		t.Fatalf("size=%#v err=%v", result, err)
	}
	properties, err := GetProperties("folder")
	if err != nil || properties.Incomplete || properties.Size != result.Bytes || properties.EntryCount != 2 {
		t.Fatalf("properties=%#v err=%v", properties, err)
	}
}

func TestDirectorySizeSkipsReservedEntryWithoutSkippingSiblings(t *testing.T) {
	root := withArchiveRoot(t)
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{InternalArchiveExtractPrefix + "private", "safe"} {
		if err := os.WriteFile(filepath.Join(root, "folder", name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := ScanDirectorySize(context.Background(), "folder")
	if err != nil || !result.Incomplete || result.Skipped != 1 || result.Bytes != 4 || result.Files != 1 {
		t.Fatalf("size=%#v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanDirectorySize(ctx, "folder"); err == nil {
		t.Fatal("cancel ignored")
	}
}
