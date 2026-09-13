package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/irains/fileharbor/conf"
)

func TestRecycleSizeIncludesAllPagesAndPreservesUnknownRecords(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	state := newTestState(t)
	bin, err := newRecycleBin(state)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		name := fmt.Sprintf("file-%d", i)
		if err := os.WriteFile(filepath.Join(conf.FileHarbor, name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := bin.Move(name); err != nil {
			t.Fatal(err)
		}
	}
	result, err := bin.ScanSize(context.Background())
	if err != nil || result.Incomplete || result.Bytes != 204 || result.Files != 51 {
		t.Fatalf("scan=%+v error=%v", result, err)
	}
	unknown := filepath.Join(state.TrashDir, "unknown")
	if err := os.WriteFile(unknown, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = bin.ScanSize(context.Background())
	if err != nil || !result.Incomplete || result.Bytes != 204 || result.Skipped != 1 {
		t.Fatalf("scan=%+v error=%v", result, err)
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "preserve" {
		t.Fatal("unknown record changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bin.ScanSize(ctx); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
}
