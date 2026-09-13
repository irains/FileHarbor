package utils

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractionTargets(t *testing.T) {
	for _, mode := range []string{"current", "chosen", "new_folder"} {
		t.Run(mode, func(t *testing.T) {
			root := withArchiveRoot(t)
			if err := os.Mkdir(filepath.Join(root, "destination"), 0700); err != nil {
				t.Fatal(err)
			}
			writeZIPFixture(t, filepath.Join(root, "sample.zip"), []zipFixtureEntry{{name: "inside.txt", data: []byte("contents"), mode: 0600}})
			target := ExtractionTarget{Mode: mode}
			out := root
			if mode == "chosen" {
				target.Directory = "destination"
				out = filepath.Join(root, "destination")
			}
			if mode == "new_folder" {
				out = filepath.Join(root, "sample")
			}
			if _, err := ExtractArchiveTargetContext(context.Background(), "sample.zip", target); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(out, "inside.txt")); err != nil || string(data) != "contents" {
				t.Fatal("output differs")
			}
			if _, err := ExtractArchiveTargetContext(context.Background(), "sample.zip", target); ErrorCode(err) != "destination_exists" {
				t.Fatalf("repeat=%v", err)
			}
		})
	}
}

func TestExtractionNewFolderPublicationRace(t *testing.T) {
	root := withArchiveRoot(t)
	writeZIPFixture(t, filepath.Join(root, "sample.zip"), []zipFixtureEntry{{name: "inside.txt", data: []byte("contents"), mode: 0600}})
	archiveBeforePublishHook = func() {
		if err := os.Mkdir(filepath.Join(root, "sample"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ExtractArchiveTargetContext(context.Background(), "sample.zip", ExtractionTarget{Mode: "new_folder"}); ErrorCode(err) != "destination_exists" {
		t.Fatalf("race=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "sample"))
	if err != nil || len(entries) != 0 {
		t.Fatal("existing destination modified")
	}
	assertArchiveRootEntries(t, root, "sample.zip", "sample")
}
