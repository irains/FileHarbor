package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/irains/fileharbor/utils"
)

type JobManager struct {
	mu            sync.Mutex
	state         *RuntimeState
	directory     string
	jobs          map[string]*FileJob
	wake          chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	worker        sync.WaitGroup
	runningCancel context.CancelFunc
	root          os.FileInfo
	rootIdentity  string
}

func newJobManager(state *RuntimeState) (*JobManager, error) {
	directory := filepath.Join(state.Dir, "jobs")
	if err := ensurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	jobs, err := loadJobs(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.Stat(state.ManagedRoot)
	if err != nil {
		return nil, err
	}
	identity, err := jobRootIdentity(state.ManagedRoot)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &JobManager{state: state, directory: directory, jobs: jobs, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, root: root, rootIdentity: identity}
	manager.worker.Add(1)
	go manager.run()
	return manager, nil
}
func (manager *JobManager) scope(owner string) string {
	sum := sha256.Sum256([]byte(owner + "\x00" + manager.state.ManagedRoot + "\x00" + manager.rootIdentity))
	return hex.EncodeToString(sum[:])
}
func (manager *JobManager) persist(job *FileJob) error {
	job.Updated = time.Now().UTC()
	if err := saveJob(manager.directory, job); err != nil {
		manager.state.SetReady(false)
		job.State = "interrupted"
		job.Code = "job_unavailable"
		if len(job.Published) > 0 || job.Intent != "" {
			job.State = "partial"
		}
		return err
	}
	return nil
}
func (manager *JobManager) submit(job *FileJob) (*FileJob, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.ctx.Err() != nil || !manager.state.Ready() {
		return nil, errors.New("job_unavailable")
	}
	active, owned := 0, 0
	for id, existing := range manager.jobs {
		if existing.Scope == job.Scope && existing.Key == job.Key {
			if existing.Kind != job.Kind || existing.Destination != job.Destination || existing.Name != job.Name || existing.Target != job.Target || existing.Previous != job.Previous || !reflect.DeepEqual(existing.Sources, job.Sources) {
				return nil, errors.New("job_key_conflict")
			}
			return existing, nil
		}
		if !jobTerminal(existing.State) {
			active++
			if existing.Scope == job.Scope {
				owned++
			}
		}
		if jobTerminal(existing.State) && existing.Intent == "" && len(existing.Stages) == 0 && time.Since(existing.Updated) > 7*24*time.Hour {
			recordPath := filepath.Join(manager.directory, id+".json")
			info, err := os.Lstat(recordPath)
			if err != nil || !info.Mode().IsRegular() {
				manager.state.SetReady(false)
				return nil, errors.New("invalid job record")
			}
			if err := os.Remove(recordPath); err != nil {
				return nil, err
			}
			if err := syncRuntimeDirectory(manager.directory); err != nil {
				manager.state.SetReady(false)
				return nil, err
			}
			delete(manager.jobs, id)
		}
	}
	if active >= 64 || owned >= 16 || len(manager.jobs) >= 1000 {
		return nil, errors.New("job_limit")
	}
	id, err := newTrashRecordID()
	if err != nil {
		return nil, err
	}
	job.ID = id
	job.Schema = 1
	job.Created = time.Now().UTC()
	job.State = "queued"
	job.Phase = "queued"
	job.Published = []string{}
	if err := manager.persist(job); err != nil {
		return nil, err
	}
	manager.jobs[id] = job
	select {
	case manager.wake <- struct{}{}:
	default:
	}
	return job, nil
}
func (manager *JobManager) stop() {
	manager.cancel()
	manager.mu.Lock()
	if manager.runningCancel != nil {
		manager.runningCancel()
	}
	manager.mu.Unlock()
	manager.worker.Wait()
}
func (manager *JobManager) run() {
	defer manager.worker.Done()
	for {
		select {
		case <-manager.ctx.Done():
			manager.mu.Lock()
			for _, job := range manager.jobs {
				if !jobTerminal(job.State) {
					job.State = "interrupted"
					job.Code = "job_interrupted"
					_ = manager.persist(job)
				}
			}
			manager.mu.Unlock()
			return
		case <-manager.wake:
		}
		for {
			manager.mu.Lock()
			var job *FileJob
			for _, candidate := range manager.jobs {
				if candidate.State == "queued" && (job == nil || candidate.Created.Before(job.Created)) {
					job = candidate
				}
			}
			if job == nil || manager.ctx.Err() != nil {
				manager.mu.Unlock()
				break
			}
			ctx, cancel := context.WithCancel(manager.ctx)
			manager.runningCancel = cancel
			job.State = "running"
			job.Phase = "scanning"
			err := manager.persist(job)
			manager.mu.Unlock()
			if err == nil {
				err = manager.execute(ctx, job)
			}
			executionCancelled := ctx.Err() != nil
			cancel()
			manager.mu.Lock()
			manager.runningCancel = nil
			if err == nil {
				job.State = "succeeded"
				job.Phase = "finished"
				// Staging evidence is cleared only by confirmed cleanup events.
			} else {
				job.Code = utils.ErrorCode(err)
				job.State = "failed"
				if executionCancelled && job.CancelRequested {
					job.State = "cancelled"
					job.Code = "request_cancelled"
				}
				if manager.ctx.Err() != nil {
					job.State = "interrupted"
					job.Code = "job_interrupted"
				}
				if len(job.Published) > 0 || job.Intent != "" {
					job.State = "partial"
					job.Code = "execution_partial"
				}
			}
			outcome := "success"
			if job.State != "succeeded" {
				outcome = job.State
			}
			if auditErr := manager.state.Record(AuditEvent{Event: "job." + job.Kind, Outcome: outcome, Principal: job.Owner, AuthMethod: "session", Affected: len(job.Published), Code: job.Code}); auditErr != nil {
				job.State = "partial"
				job.Code = "audit_unavailable"
			}
			_ = manager.persist(job)
			manager.mu.Unlock()
		}
	}
}
func (manager *JobManager) execute(ctx context.Context, job *FileJob) error {
	root, err := os.Stat(manager.state.ManagedRoot)
	if err != nil || !os.SameFile(root, manager.root) || !manager.state.Ready() {
		return errors.New("job_unavailable")
	}
	if err := manager.state.Record(AuditEvent{Event: "job." + job.Kind, Outcome: "attempted", Principal: job.Owner, AuthMethod: "session"}); err != nil {
		return err
	}
	lastCheckpoint := time.Now()
	ctx = utils.WithOperationObserver(ctx, func(event utils.OperationEvent) error {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		if !manager.state.Ready() {
			return errors.New("job_unavailable")
		}
		if event.Phase == "publish_intent" {
			identity, err := jobRootIdentity(manager.state.ManagedRoot)
			if err != nil || identity != manager.rootIdentity {
				return utils.ErrSourceChanged
			}
		}
		if event.Phase != "stage_cleaned" {
			job.Phase = event.Phase
		}
		job.Bytes += event.Bytes
		job.Items += event.Items
		critical := event.Phase == "stage_cleaned" || event.Phase == "staging" || event.Phase == "publish_intent" || event.Phase == "published"
		switch event.Phase {
		case "stage_cleaned":
			for i, stage := range job.Stages {
				if stage == event.Path {
					job.Stages = append(job.Stages[:i], job.Stages[i+1:]...)
					break
				}
			}
		case "staging":
			job.Stages = append(job.Stages, event.Path)
		case "publish_intent":
			job.Intent = event.Path
		case "published":
			job.Published = append(job.Published, event.Path)
			job.Intent = ""
		}
		if critical || time.Since(lastCheckpoint) >= 2*time.Second {
			lastCheckpoint = time.Now()
			return manager.persist(job)
		}
		return nil
	})
	if job.Kind == "extract" {
		source := job.Sources[0]
		_, _, info, err := utils.ResolveExisting(source.Path, false)
		if err != nil {
			return err
		}
		if utils.EntryVersion(info) != source.Version {
			return utils.ErrSourceChanged
		}
		_, err = utils.ExtractArchiveVersionContext(ctx, source.Path, job.Target, source.Version)
		return err
	}
	return utils.ExecuteFileJob(ctx, job.Kind, job.Sources, job.Destination, job.Name)
}
func (manager *JobManager) list(scope string) []FileJob {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := []FileJob{}
	for _, job := range manager.jobs {
		if job.Scope == scope {
			copy := *job
			copy.Sources = append([]utils.FileJobSource(nil), job.Sources...)
			copy.Published = append([]string{}, job.Published...)
			copy.Stages = nil
			result = append(result, copy)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Created.After(result[j].Created) })
	return result
}
