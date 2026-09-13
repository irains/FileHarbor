package utils

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileJobsStageAndNeverOverwrite(t *testing.T) {
	for _, kind := range []string{"copy", "compress"} {
		t.Run(kind, func(t *testing.T) {
			root := withArchiveRoot(t)
			os.Mkdir(filepath.Join(root, "source"), 0700)
			os.Mkdir(filepath.Join(root, "out"), 0700)
			os.WriteFile(filepath.Join(root, "source", "file"), []byte("contents"), 0600)
			_, _, info, _ := ResolveExisting("source", false)
			var bytes int64
			published := []string{}
			ctx := WithOperationObserver(context.Background(), func(event OperationEvent) error {
				bytes += event.Bytes
				if event.Phase == "published" {
					published = append(published, event.Path)
				}
				return nil
			})
			sources := []FileJobSource{{Path: "source", Version: EntryVersion(info)}}
			if err := ExecuteFileJob(ctx, kind, sources, "out", ""); err != nil {
				t.Fatal(err)
			}
			if bytes != 8 || len(published) != 1 {
				t.Fatalf("bytes=%d published=%v", bytes, published)
			}
			if kind == "copy" {
				data, err := os.ReadFile(filepath.Join(root, "out", "source", "file"))
				if err != nil || string(data) != "contents" {
					t.Fatal("copy mismatch")
				}
			} else {
				archive, err := zip.OpenReader(filepath.Join(root, "out", "source.zip"))
				if err != nil {
					t.Fatal(err)
				}
				archive.Close()
			}
			if err := ExecuteFileJob(ctx, kind, sources, "out", ""); ErrorCode(err) != "destination_exists" {
				t.Fatalf("overwrite=%v", err)
			}
		})
	}
}
func TestFileJobCancellationBeforePublication(t *testing.T) {
	root := withArchiveRoot(t)
	os.Mkdir(filepath.Join(root, "out"), 0700)
	os.WriteFile(filepath.Join(root, "source"), []byte("data"), 0600)
	_, _, info, _ := ResolveExisting("source", false)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithOperationObserver(ctx, func(event OperationEvent) error {
		if event.Phase == "writing" {
			cancel()
		}
		return nil
	})
	if err := ExecuteFileJob(ctx, "copy", []FileJobSource{{Path: "source", Version: EntryVersion(info)}}, "out", ""); ErrorCode(err) != "request_cancelled" {
		t.Fatalf("cancel=%v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "out"))
	if len(entries) != 0 {
		t.Fatal("cancel left visible output")
	}
}

func TestFileJobKeepsEarlierPublishedOutputOnConflict(t *testing.T) {
	root := withArchiveRoot(t)
	os.Mkdir(filepath.Join(root, "out"), 0700)
	sources := []FileJobSource{}
	for _, name := range []string{"a", "b"} {
		os.WriteFile(filepath.Join(root, name), []byte(name), 0600)
		_, _, info, _ := ResolveExisting(name, false)
		sources = append(sources, FileJobSource{Path: name, Version: EntryVersion(info)})
	}
	os.WriteFile(filepath.Join(root, "out", "b"), []byte("existing"), 0600)
	if err := ExecuteFileJob(context.Background(), "copy", sources, "out", ""); ErrorCode(err) != "destination_exists" {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a": "a", "b": "existing"} {
		data, err := os.ReadFile(filepath.Join(root, "out", name))
		if err != nil || string(data) != want {
			t.Fatalf("changed output %s", name)
		}
	}
}
