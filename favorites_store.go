package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/irains/fileharbor/utils"
)

type Favorite struct {
	ID           string    `json:"id"`
	Path         string    `json:"path"`
	Label        string    `json:"label"`
	Revision     uint64    `json:"revision"`
	CreatedAt    time.Time `json:"created_at"`
	Availability string    `json:"availability,omitempty"`
}
type favoriteRecord struct {
	Schema  int        `json:"schema"`
	Scope   string     `json:"scope"`
	Entries []Favorite `json:"entries"`
}
type favoriteStore struct {
	state     *RuntimeState
	directory string
	root      string
}

var favoritesMu sync.Mutex
var errFavoriteConflict = errors.New("favorite_conflict")
var errFavoriteLimit = errors.New("favorite_limit")

func newFavoriteStore(state *RuntimeState) (*favoriteStore, error) {
	directory := filepath.Join(state.Dir, "favorites")
	if err := ensurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	return &favoriteStore{state: state, directory: directory, root: state.ManagedRoot}, nil
}
func (store *favoriteStore) scope(owner string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + store.root))
	return hex.EncodeToString(sum[:])
}
func validFavoriteLabel(label string) bool {
	return utf8.ValidString(label) && strings.TrimSpace(label) != "" && utf8.RuneCountInString(label) <= 128 && !strings.ContainsAny(label, "\x00\r\n")
}
func (store *favoriteStore) load(owner string) (favoriteRecord, error) {
	scope := store.scope(owner)
	record := favoriteRecord{Schema: 1, Scope: scope, Entries: []Favorite{}}
	filename := filepath.Join(store.directory, scope+".json")
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return record, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return record, errors.New("invalid favorite state")
	}
	file, err := os.Open(filename)
	if err != nil {
		return record, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (4<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	if decoder.Decode(new(any)) != io.EOF || record.Schema != 1 || record.Scope != scope || len(record.Entries) > 500 {
		return record, errors.New("invalid favorite state")
	}
	ids := map[string]bool{}
	paths := map[string]bool{}
	for _, entry := range record.Entries {
		clean, err := utils.CleanRelative(entry.Path, true)
		if err != nil || clean != entry.Path || !isTrashRecordID(entry.ID) || !validFavoriteLabel(entry.Label) || entry.Revision == 0 || entry.Availability != "" || ids[entry.ID] || paths[entry.Path] {
			return record, errors.New("invalid favorite state")
		}
		ids[entry.ID] = true
		paths[entry.Path] = true
	}
	return record, nil
}
func (store *favoriteStore) save(record favoriteRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(store.directory, ".favorites-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := protectPrivateFile(file.Name()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(store.directory, record.Scope+".json")); err != nil {
		return err
	}
	return syncRuntimeDirectory(store.directory)
}
func (store *favoriteStore) list(owner string) ([]Favorite, error) {
	favoritesMu.Lock()
	defer favoritesMu.Unlock()
	record, err := store.load(owner)
	if err != nil {
		return nil, err
	}
	for i := range record.Entries {
		entry := &record.Entries[i]
		entry.Availability = "available"
		if _, _, _, err := utils.ResolveDirectory(entry.Path, true); err != nil {
			switch utils.ErrorCode(err) {
			case "not_found":
				entry.Availability = "missing"
			case "not_directory":
				entry.Availability = "not_directory"
			default:
				entry.Availability = "unavailable"
			}
		}
	}
	return record.Entries, nil
}
func (store *favoriteStore) change(owner, method, id, path, label string, revision uint64) (Favorite, error) {
	favoritesMu.Lock()
	defer favoritesMu.Unlock()
	record, err := store.load(owner)
	if err != nil {
		return Favorite{}, err
	}
	var changed Favorite
	if method == "POST" {
		if !validFavoriteLabel(label) {
			return changed, utils.ErrInvalidPath
		}
		_, relative, _, err := utils.ResolveDirectory(path, true)
		if err != nil {
			return changed, err
		}
		for _, entry := range record.Entries {
			if entry.Path == relative {
				return entry, nil
			}
		}
		if len(record.Entries) >= 500 {
			return changed, errFavoriteLimit
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return changed, err
		}
		changed = Favorite{ID: hex.EncodeToString(random[:]), Path: relative, Label: label, Revision: 1, CreatedAt: time.Now().UTC()}
		record.Entries = append(record.Entries, changed)
	} else {
		index := -1
		for i, entry := range record.Entries {
			if entry.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return changed, os.ErrNotExist
		}
		changed = record.Entries[index]
		if revision != changed.Revision || revision == ^uint64(0) {
			return changed, errFavoriteConflict
		}
		if method == "PATCH" {
			if !validFavoriteLabel(label) {
				return changed, utils.ErrInvalidPath
			}
			changed.Label = label
			changed.Revision++
			record.Entries[index] = changed
		} else {
			record.Entries = append(record.Entries[:index], record.Entries[index+1:]...)
		}
	}
	if err := store.save(record); err != nil {
		store.state.SetReady(false)
		return Favorite{}, err
	}
	return changed, nil
}
