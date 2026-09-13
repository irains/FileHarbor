package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchManagedFiltersAndScope(t *testing.T) {
	root := useTestRoot(t)
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "Report.TXT"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	min, max := int64(4), int64(4)
	before := time.Now().Add(-time.Hour)
	after := time.Now().Add(time.Hour)
	options := SearchOptions{Recursive: true, Name: "report", Extension: ".txt", MinSize: &min, MaxSize: &max, ModifiedAfter: &before, ModifiedBefore: &after}
	result, err := SearchManaged(context.Background(), options)
	if err != nil || len(result.Entries) != 1 || result.Entries[0].Parent != "nested" || result.Incomplete {
		t.Fatalf("result=%#v %v", result, err)
	}
	options.Recursive = false
	result, err = SearchManaged(context.Background(), options)
	if err != nil || len(result.Entries) != 0 {
		t.Fatalf("shallow=%#v %v", result, err)
	}
	options.MinSize = &max
	negative := int64(-1)
	options.MaxSize = &negative
	if _, err := SearchManaged(context.Background(), options); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestSearchManagedInclusiveDatesAndResultLimit(t *testing.T) {
	root := useTestRoot(t)
	stamp := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 501; i++ {
		filename := filepath.Join(root, fmt.Sprintf("entry-%03d.txt", i))
		if err := os.WriteFile(filename, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filename, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	result, err := SearchManaged(context.Background(), SearchOptions{ModifiedAfter: &stamp, ModifiedBefore: &stamp})
	if err != nil || len(result.Entries) != 500 || !result.Incomplete || result.Reason != "result_limit" {
		t.Fatalf("count=%d summary=%#v err=%v", len(result.Entries), result.ScanSummary, err)
	}
	later := stamp.Add(time.Second)
	result, err = SearchManaged(context.Background(), SearchOptions{ModifiedAfter: &later})
	if err != nil || len(result.Entries) != 0 || result.Incomplete {
		t.Fatalf("count=%d err=%v", len(result.Entries), err)
	}
}
