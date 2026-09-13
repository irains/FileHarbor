package main

import (
	"errors"
	"github.com/irains/fileharbor/conf"
	"os"
	"path/filepath"
	"testing"
)

func TestFavoritesPersistIsolationAndRevision(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	state := newTestState(t)
	store, err := newFavoriteStore(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(conf.FileHarbor, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	entry, err := store.change("owner", "POST", "", "folder", "First", 0)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newFavoriteStore(state)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := reopened.list("owner")
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID || entries[0].Availability != "available" {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if entries, err := reopened.list("another"); err != nil || len(entries) != 0 {
		t.Fatal("owner isolation failed")
	}
	otherRoot := *reopened
	otherRoot.root = store.root + "-other"
	if entries, err := otherRoot.list("owner"); err != nil || len(entries) != 0 {
		t.Fatal("root isolation failed")
	}
	renamed, err := reopened.change("owner", "PATCH", entry.ID, "", "Display only", entry.Revision)
	if err != nil || renamed.Revision != 2 {
		t.Fatal("rename failed")
	}
	if _, err := os.Stat(filepath.Join(conf.FileHarbor, "folder")); err != nil {
		t.Fatal("display edit renamed directory")
	}
	if _, err := reopened.change("owner", "DELETE", entry.ID, "", "", entry.Revision); !errors.Is(err, errFavoriteConflict) {
		t.Fatal("stale revision accepted")
	}
	if err := os.Remove(filepath.Join(conf.FileHarbor, "folder")); err != nil {
		t.Fatal(err)
	}
	entries, err = reopened.list("owner")
	if err != nil || len(entries) != 1 || entries[0].Availability != "missing" {
		t.Fatal("missing favorite lost")
	}
	if _, err := reopened.change("owner", "DELETE", entry.ID, "", "", renamed.Revision); err != nil {
		t.Fatal(err)
	}
	entries, err = reopened.list("owner")
	if err != nil || len(entries) != 0 {
		t.Fatal("remove failed")
	}
}
