package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/irains/fileharbor/utils"
)

const (
	stateChunksDirectory  = "chunks"
	stateTempDirectory    = "tmp"
	stateUploadsDirectory = "uploads"
	stateTrashDirectory   = "trash"
	stateAuditFile        = "audit.jsonl"
	stateLockFile         = "state.lock"

	maxAuditLogBytes   = int64(16 << 20)
	auditRetentionLogs = 5
)

// AuditEvent is one durable, JSONL-encoded administrative event. It contains
// only stable, root-relative operational metadata and never request secrets.
type AuditEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Event      string    `json:"event"`
	Outcome    string    `json:"outcome"`
	Principal  string    `json:"principal,omitempty"`
	AuthMethod string    `json:"auth_method,omitempty"`
	ClientIP   string    `json:"client_ip,omitempty"`
	Path       string    `json:"path,omitempty"`
	Affected   int       `json:"affected,omitempty"`
	Code       string    `json:"code,omitempty"`
}

// AuditLog serializes append-and-sync audit writes so concurrent HTTP handlers
// cannot interleave JSON lines.
type AuditLog struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	healthy bool
}

func newAuditLog(path string) (*AuditLog, error) {
	if err := protectAuditRotations(path); err != nil {
		return nil, err
	}
	file, err := openAuditFile(path)
	if err != nil {
		return nil, err
	}
	return &AuditLog{file: file, path: path, healthy: true}, nil
}

func protectAuditRotations(path string) error {
	for index := 1; index <= auditRetentionLogs; index++ {
		rotated := fmt.Sprintf("%s.%d", path, index)
		info, err := os.Lstat(rotated)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("audit rotation must be a regular file")
		}
		if err := protectPrivateFile(rotated); err != nil {
			return errors.New("could not protect audit rotation")
		}
	}
	return nil
}

func validateAuditJSONL(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("could not inspect audit log")
	}
	defer file.Close()
	contents, err := io.ReadAll(file)
	if err != nil {
		return errors.New("could not inspect audit log")
	}
	validEnd := 0
	for validEnd < len(contents) {
		next := bytes.IndexByte(contents[validEnd:], '\n')
		if next < 0 {
			if len(bytes.TrimSpace(contents[validEnd:])) == 0 {
				break
			}
			if err := file.Truncate(int64(validEnd)); err != nil || file.Sync() != nil {
				return errors.New("could not repair audit log")
			}
			return nil
		}
		lineEnd := validEnd + next
		line := bytes.TrimSpace(contents[validEnd:lineEnd])
		if len(line) == 0 {
			return errors.New("audit log contains an empty record")
		}
		var event AuditEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return errors.New("audit log contains an invalid record")
		}
		validEnd = lineEnd + 1
	}
	return nil
}

func openAuditFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("audit log must be a regular file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("could not inspect audit log")
	}
	if err := validateAuditJSONL(path); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("could not open audit log")
	}
	if err := protectPrivateFile(path); err != nil {
		_ = file.Close()
		return nil, errors.New("could not protect audit log")
	}
	if err := syncRuntimeDirectory(filepath.Dir(path)); err != nil {
		_ = file.Close()
		return nil, errors.New("could not sync audit directory")
	}
	return file, nil
}

func (log *AuditLog) Record(event AuditEvent) error {
	if log == nil {
		return errors.New("audit log unavailable")
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if !log.healthy || log.file == nil {
		return errors.New("audit log unavailable")
	}
	event.Timestamp = time.Now().UTC()
	encoded, err := json.Marshal(event)
	if err != nil {
		log.healthy = false
		return errors.New("could not encode audit log")
	}
	if info, err := log.file.Stat(); err != nil || info.Size()+int64(len(encoded)+1) > maxAuditLogBytes {
		if err := log.rotateLocked(); err != nil {
			log.healthy = false
			return err
		}
	}
	if _, err := log.file.Write(append(encoded, '\n')); err != nil {
		log.healthy = false
		return errors.New("could not write audit log")
	}
	if err := log.file.Sync(); err != nil {
		log.healthy = false
		return errors.New("could not sync audit log")
	}
	return nil
}

func (log *AuditLog) rotateLocked() error {
	if log.path == "" || log.file == nil {
		return errors.New("audit log unavailable")
	}
	if err := log.file.Close(); err != nil {
		return errors.New("could not close audit log")
	}
	log.file = nil
	if err := os.Remove(fmt.Sprintf("%s.%d", log.path, auditRetentionLogs)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("could not rotate audit log")
	}
	if err := syncRuntimeDirectory(filepath.Dir(log.path)); err != nil {
		return errors.New("could not sync audit directory")
	}
	for index := auditRetentionLogs - 1; index >= 1; index-- {
		source := fmt.Sprintf("%s.%d", log.path, index)
		destination := fmt.Sprintf("%s.%d", log.path, index+1)
		if err := os.Rename(source, destination); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return errors.New("could not rotate audit log")
		}
		if err := syncRuntimeDirectory(filepath.Dir(log.path)); err != nil {
			return errors.New("could not sync audit directory")
		}
	}
	if err := os.Rename(log.path, log.path+".1"); err != nil {
		return errors.New("could not rotate active audit log")
	}
	if err := syncRuntimeDirectory(filepath.Dir(log.path)); err != nil {
		return errors.New("could not sync audit directory")
	}
	if err := protectAuditRotations(log.path); err != nil {
		return err
	}
	file, err := openAuditFile(log.path)
	if err != nil {
		return err
	}
	log.file = file
	return nil
}

func (log *AuditLog) Healthy() bool {
	if log == nil {
		return false
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.healthy && log.file != nil
}

func (log *AuditLog) Close() error {
	if log == nil {
		return nil
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file == nil {
		return nil
	}
	err := log.file.Close()
	log.file = nil
	if err != nil {
		log.healthy = false
		return errors.New("could not close audit log")
	}
	return nil
}

// RuntimeState owns application-private files that must never be reachable
// through the managed workspace.
type RuntimeState struct {
	jobsMu         sync.Mutex
	Jobs           *JobManager
	scanGeneration atomic.Uint64
	scanContext    context.Context
	scanCancel     context.CancelFunc
	scanWorkers    sync.WaitGroup
	ManagedRoot    string
	Dir            string
	ChunksDir      string
	TempDir        string
	UploadsDir     string
	TrashDir       string
	Audit          *AuditLog
	lock           *stateLock

	mu      sync.RWMutex
	chunkMu sync.Mutex
	ready   bool
}

func OpenRuntimeState(configuredDir, managedRoot string) (*RuntimeState, error) {
	if configuredDir == "" || managedRoot == "" {
		return nil, errors.New("state and managed directories are required")
	}
	root, err := filepath.EvalSymlinks(managedRoot)
	if err != nil {
		return nil, errors.New("could not resolve managed directory")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, errors.New("could not resolve managed directory")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, errors.New("managed directory is unavailable")
	}

	if info, err := os.Lstat(configuredDir); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return nil, errors.New("state directory must be a real directory")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("could not inspect state directory")
	}
	statePath, err := resolveProspectivePath(configuredDir)
	if err != nil {
		return nil, errors.New("could not resolve state directory")
	}
	if pathsOverlap(root, statePath) {
		return nil, errors.New("state directory must not overlap the managed directory")
	}
	if err := ensurePrivateDirectory(statePath); err != nil {
		return nil, err
	}

	statePath, err = filepath.EvalSymlinks(statePath)
	if err != nil {
		return nil, errors.New("could not resolve state directory")
	}
	statePath = filepath.Clean(statePath)
	if pathsOverlap(root, statePath) {
		return nil, errors.New("state directory must not overlap the managed directory")
	}
	lock, err := acquireStateLock(filepath.Join(statePath, stateLockFile))
	if err != nil {
		return nil, err
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = lock.Close()
		}
	}()
	chunksDir := filepath.Join(statePath, stateChunksDirectory)
	if err := ensurePrivateDirectory(chunksDir); err != nil {
		return nil, err
	}
	tempDir := filepath.Join(statePath, stateTempDirectory)
	if err := ensurePrivateDirectory(tempDir); err != nil {
		return nil, err
	}
	uploadsDir := filepath.Join(statePath, stateUploadsDirectory)
	if err := ensurePrivateDirectory(uploadsDir); err != nil {
		return nil, err
	}
	trashDir := filepath.Join(statePath, stateTrashDirectory)
	if err := ensurePrivateDirectory(trashDir); err != nil {
		return nil, err
	}
	if err := clearStateDirectory(chunksDir); err != nil {
		return nil, err
	}
	if err := clearStateDirectory(tempDir); err != nil {
		return nil, err
	}
	audit, err := newAuditLog(filepath.Join(statePath, stateAuditFile))
	if err != nil {
		return nil, err
	}
	state := &RuntimeState{ManagedRoot: root, Dir: statePath, ChunksDir: chunksDir, TempDir: tempDir, UploadsDir: uploadsDir, TrashDir: trashDir, Audit: audit, lock: lock}
	state.scanContext, state.scanCancel = context.WithCancel(context.Background())
	closeLock = false
	return state, nil
}

func resolveProspectivePath(configuredDir string) (string, error) {
	absolute, err := filepath.Abs(configuredDir)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	missing := make([]string, 0)
	ancestor := absolute
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fs.ErrNotExist
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Clean(resolved), nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return errors.New("could not create state directory")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("could not inspect state directory")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("state directory must be a real directory")
	}
	if err := protectPrivateDirectory(path); err != nil {
		return fmt.Errorf("could not protect state directory: %w", err)
	}
	return nil
}

func clearStateDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return errors.New("could not inspect state directory")
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return errors.New("could not clear stale runtime data")
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	if utils.Contained(first, second) || utils.Contained(second, first) {
		return true
	}
	return pathIsOrContains(first, second) || pathIsOrContains(second, first)
}

// pathIsOrContains detects aliases created by case-insensitive filesystems in
// addition to the lexical checks above. It compares every existing candidate
// ancestor by filesystem identity, so state paths such as "FILES/state" cannot
// bypass a managed root named "files" on a default macOS volume.
func pathIsOrContains(ancestor, candidate string) bool {
	ancestorInfo, err := os.Stat(ancestor)
	if err != nil {
		return false
	}
	for current := filepath.Clean(candidate); ; {
		info, err := os.Stat(current)
		if err == nil && os.SameFile(ancestorInfo, info) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

func (state *RuntimeState) SetReady(ready bool) {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.ready = ready
	state.mu.Unlock()
}

func (state *RuntimeState) Ready() bool {
	if state == nil || state.Audit == nil || !state.Audit.Healthy() {
		return false
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.ready
}

func (state *RuntimeState) Record(event AuditEvent) error {
	if state != nil && event.Outcome != "attempted" {
		state.scanGeneration.Add(1)
	}

	if state == nil || state.Audit == nil {
		return errors.New("audit log unavailable")
	}
	if err := state.Audit.Record(event); err != nil {
		state.mu.Lock()
		wasReady := state.ready
		state.ready = false
		state.mu.Unlock()
		if wasReady {
			fmt.Fprintln(os.Stderr, "audit logging unavailable; readiness disabled")
		}
		return err
	}
	return nil
}

func (state *RuntimeState) startScan() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.scanContext == nil || state.scanContext.Err() != nil {
		return false
	}
	state.scanWorkers.Add(1)
	return true
}

func (state *RuntimeState) Close() error {
	if state == nil {
		return nil
	}
	state.SetReady(false)
	state.mu.Lock()
	if state.scanCancel != nil {
		state.scanCancel()
	}
	state.mu.Unlock()
	state.scanWorkers.Wait()
	state.jobsMu.Lock()
	if state.Jobs != nil {
		state.Jobs.stop()
	}
	state.jobsMu.Unlock()
	var auditErr error
	if state.Audit != nil {
		auditErr = state.Audit.Close()
	}
	var lockErr error
	if state.lock != nil {
		lockErr = state.lock.Close()
	}
	if auditErr != nil {
		return auditErr
	}
	return lockErr
}
