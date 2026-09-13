package utils

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestObservedExtractionKeepsPublishedOutputAfterJournalFailure(t *testing.T) {
	root := withArchiveRoot(t)
	writeZIPFixture(t, filepath.Join(root, "sample.zip"), []zipFixtureEntry{
		{name: "a.txt", data: []byte("first"), mode: 0600},
		{name: "b.txt", data: []byte("second"), mode: 0600},
	})
	var bytes int64
	var published []string
	ctx := WithOperationObserver(context.Background(), func(event OperationEvent) error {
		bytes += event.Bytes
		if event.Phase == "publish_intent" && event.Path == "b.txt" {
			return errors.New("journal unavailable")
		}
		if event.Phase == "published" {
			published = append(published, event.Path)
		}
		return nil
	})
	if _, err := ExtractArchiveTargetContext(ctx, "sample.zip", ExtractionTarget{Mode: "current"}); err == nil {
		t.Fatal("journal failure was ignored")
	}
	if bytes != 11 || len(published) != 1 || published[0] != "a.txt" {
		t.Fatalf("bytes=%d published=%v", bytes, published)
	}
	if data, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(data) != "first" {
		t.Fatal("published output was rolled back")
	}
	if _, err := os.Lstat(filepath.Join(root, "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unrecorded intent published output")
	}
}

func TestObservedExtractionRejectsPublicationBeforeRename(t *testing.T) {
	root := withArchiveRoot(t)
	writeZIPFixture(t, filepath.Join(root, "sample.zip"), []zipFixtureEntry{{name: "file", data: []byte("data"), mode: 0600}})
	ctx := WithOperationObserver(context.Background(), func(event OperationEvent) error {
		if event.Phase == "publish_intent" {
			return errors.New("journal unavailable")
		}
		return nil
	})
	if _, err := ExtractArchiveTargetContext(ctx, "sample.zip", ExtractionTarget{Mode: "new_folder"}); err == nil {
		t.Fatal("publication proceeded")
	}
	if _, err := os.Lstat(filepath.Join(root, "sample")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("destination published")
	}
}
