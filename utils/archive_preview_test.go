package utils

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestArchivePreviewReadsWithoutExtracting(t *testing.T) {
	root := withArchiveRoot(t)
	writeZIPFixture(t, filepath.Join(root, "sample.zip"), []zipFixtureEntry{{name: "nested/data.txt", data: []byte("contents"), mode: 0600}})
	result, err := PreviewArchive(context.Background(), "sample.zip", "")
	if err != nil || !result.Complete || len(result.Entries) != 1 || result.Entries[0].Name != "nested/data.txt" || *result.Entries[0].Size != 8 {
		t.Fatalf("preview=%#v %v", result, err)
	}
	assertArchiveRootEntries(t, root, "sample.zip")
	if _, err := PreviewArchive(context.Background(), "sample.zip", "stale"); err != ErrSourceChanged {
		t.Fatal("stale version accepted")
	}
}

func TestArchivePreviewTARAliasAndUnsafeZIP(t *testing.T) {
	root := withArchiveRoot(t)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "./data", Mode: 0600, Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.tar.gz"), buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := PreviewArchive(context.Background(), "sample.tar.gz", "")
	if err != nil || !result.Complete || len(result.Entries) != 1 || result.Entries[0].Name != "data" {
		t.Fatalf("TAR preview=%#v %v", result, err)
	}
	writeZIPFixture(t, filepath.Join(root, "unsafe.zip"), []zipFixtureEntry{{name: "../escape", data: []byte("data"), mode: 0600}})
	result, err = PreviewArchive(context.Background(), "unsafe.zip", "")
	if err != nil || result.Complete || result.Reason != "archive_unsafe_entry" {
		t.Fatalf("unsafe=%#v %v", result, err)
	}
	assertArchiveRootEntries(t, root, "sample.tar.gz", "unsafe.zip")
}

func TestArchivePreviewCentralDirectoryBounds(t *testing.T) {
	root := withArchiveRoot(t)
	filename := filepath.Join(root, "sample.zip")
	writeZIPFixture(t, filename, []zipFixtureEntry{{name: "data", data: []byte("data"), mode: 0600}})
	original, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	end := len(original) - 22
	for _, test := range []struct {
		name   string
		mutate func([]byte)
		reason string
	}{
		{"underreported", func(data []byte) {
			binary.LittleEndian.PutUint16(data[end+8:], 0)
			binary.LittleEndian.PutUint16(data[end+10:], 0)
		}, "corrupt_archive"},
		{"count-limit", func(data []byte) {
			binary.LittleEndian.PutUint16(data[end+8:], 10001)
			binary.LittleEndian.PutUint16(data[end+10:], 10001)
		}, "archive_limit_exceeded"},
		{"central-limit", func(data []byte) { binary.LittleEndian.PutUint32(data[end+12:], 16<<20+1) }, "archive_limit_exceeded"},
		{"offset-outside", func(data []byte) { binary.LittleEndian.PutUint32(data[end+16:], uint32(len(data))) }, "corrupt_archive"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append([]byte(nil), original...)
			test.mutate(data)
			if err := os.WriteFile(filename, data, 0600); err != nil {
				t.Fatal(err)
			}
			result, err := PreviewArchive(context.Background(), "sample.zip", "")
			if err != nil || result.Complete || result.Reason != test.reason {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestArchivePreviewCancellationAndOutputLimit(t *testing.T) {
	root := withArchiveRoot(t)
	entries := make([]zipFixtureEntry, 1001)
	for i := range entries {
		entries[i] = zipFixtureEntry{name: fmt.Sprintf("file-%04d", i), mode: 0600}
	}
	writeZIPFixture(t, filepath.Join(root, "sample.zip"), entries)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PreviewArchive(ctx, "sample.zip", ""); ErrorCode(err) != "request_cancelled" {
		t.Fatalf("cancel=%v", err)
	}
	result, err := PreviewArchive(context.Background(), "sample.zip", "")
	if err != nil || result.Complete || !result.Truncated || len(result.Entries) != 1000 || result.Reason != "archive_limit_exceeded" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	assertArchiveRootEntries(t, root, "sample.zip")
}
