package utils

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWalkManagedBeyondListingLimit(t *testing.T) {
	root := useTestRoot(t)
	for i := 0; i < 140; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.txt", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "last.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, InternalTrashRestorePrefix+"hidden"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	summary, err := WalkManaged(context.Background(), "", true, ScanLimits{1000, 10, time.Second}, func(entry ScanEntry) bool { seen[entry.Path] = true; return true })
	if err != nil || len(seen) != 142 || !seen["nested/last.txt"] || summary.Skipped != 1 {
		t.Fatalf("scan=%#v count=%d err=%v", summary, len(seen), err)
	}
}

func TestWalkManagedBudgetsAndCancellation(t *testing.T) {
	root := useTestRoot(t)
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := WalkManaged(context.Background(), "", true, ScanLimits{2, 10, time.Second}, func(ScanEntry) bool { return true })
	if err != nil || summary.Visited != 2 || !summary.Incomplete || summary.Reason != "entry_limit" {
		t.Fatalf("scan=%#v %v", summary, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = WalkManaged(ctx, "", true, ScanLimits{10, 10, time.Second}, func(ScanEntry) bool { t.Error("cancelled scan visited entry"); return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
