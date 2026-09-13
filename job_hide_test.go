package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irains/fileharbor/conf"
	"github.com/irains/fileharbor/utils"
)

func jobHideFixture(t *testing.T) (*JobManager, *FileJob) {
	t.Helper()
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	state := newTestState(t)
	directory := filepath.Join(state.Dir, "jobs")
	if err := ensurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	identity, err := jobRootIdentity(state.ManagedRoot)
	if err != nil {
		t.Fatal(err)
	}
	manager := &JobManager{state: state, directory: directory, jobs: map[string]*FileJob{}, ctx: context.Background(), wake: make(chan struct{}, 1), rootIdentity: identity}
	job := &FileJob{Schema: 1, ID: strings.Repeat("a", 32), Key: strings.Repeat("b", 32), Scope: manager.scope("admin"), Kind: "copy", State: "partial", Phase: "published", Sources: []utils.FileJobSource{{Path: "source", Version: "v"}}, Destination: "out", Published: []string{"out/source"}, Intent: "out/uncertain", Stages: []string{utils.InternalArchiveExtractPrefix + "evidence"}, Previous: strings.Repeat("c", 32), Created: time.Now().UTC().Add(-10 * 24 * time.Hour), Updated: time.Now().UTC().Add(-8 * 24 * time.Hour)}
	manager.jobs[job.ID] = job
	if err := saveJob(directory, job); err != nil {
		t.Fatal(err)
	}
	return manager, job
}

func noJobAudit(string, string) error { return nil }

func TestJobHidePreservesEvidenceRestartAndQuota(t *testing.T) {
	manager, job := jobHideFixture(t)
	before := *job
	for _, p := range []string{"source", "out/source", "out/uncertain", job.Stages[0]} {
		full := filepath.Join(conf.FileHarbor, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("evidence"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.hide(job.Scope, job.ID, noJobAudit); err != nil {
		t.Fatal(err)
	}
	if err := manager.hide(job.Scope, job.ID, noJobAudit); err != nil {
		t.Fatal(err)
	}
	if len(manager.list(job.Scope)) != 0 || len(manager.jobs) != 1 {
		t.Fatal("hidden record lost or visible")
	}
	loaded, err := loadJobs(manager.directory)
	if err != nil {
		t.Fatal(err)
	}
	expected := before
	expected.Hidden = true
	if !reflect.DeepEqual(*loaded[job.ID], expected) {
		t.Fatalf("evidence changed: %#v", loaded[job.ID])
	}
	for _, p := range []string{"source", "out/source", "out/uncertain", job.Stages[0]} {
		data, err := os.ReadFile(filepath.Join(conf.FileHarbor, filepath.FromSlash(p)))
		if err != nil || string(data) != "evidence" {
			t.Fatal("filesystem evidence changed")
		}
	}
	restarted, err := newJobManager(manager.state)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.stop()
	if len(restarted.list(job.Scope)) != 0 {
		t.Fatal("restart revealed hidden record")
	}
	request := before
	if _, err := manager.submit(&request); err == nil || err.Error() != "job_hidden" {
		t.Fatalf("hidden key replay: %v", err)
	}
	for i := len(manager.jobs); i < 1000; i++ {
		key := strings.Repeat("0", 28) + fmtJobSuffix(i)
		clone := expected
		clone.ID = key
		clone.Key = key
		manager.jobs[key] = &clone
	}
	request.Key = strings.Repeat("d", 32)
	request.Previous = ""
	if _, err := manager.submit(&request); err == nil || err.Error() != "job_limit" {
		t.Fatalf("hidden quota bypass: %v", err)
	}
	if manager.jobs[job.ID].Intent != before.Intent {
		t.Fatal("GC discarded unresolved evidence")
	}
}

func fmtJobSuffix(i int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[(i>>12)&15], digits[(i>>8)&15], digits[(i>>4)&15], digits[i&15]})
}

func TestJobHideSaveFailures(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_rename", true: "directory_sync"}[afterRename], func(t *testing.T) {
			manager, job := jobHideFixture(t)
			before := *job
			manager.saveHidden = func(directory string, copy *FileJob) error {
				if !afterRename {
					return errors.New("injected save failure")
				}
				return saveJobWithSync(directory, copy, func(string) error { return errors.New("injected directory sync failure") })
			}
			if err := manager.hide(job.Scope, job.ID, noJobAudit); err == nil || err.Error() != "job_unavailable" {
				t.Fatalf("hide error: %v", err)
			}
			if manager.state.Ready() || len(manager.list(job.Scope)) != 1 || !reflect.DeepEqual(*manager.jobs[job.ID], before) {
				t.Fatal("failure changed memory or stayed writable")
			}
			loaded, err := loadJobs(manager.directory)
			if err != nil {
				t.Fatal(err)
			}
			if loaded[job.ID].Hidden != afterRename {
				t.Fatal("unexpected disk commit state")
			}
		})
	}
}

func TestJobHideRetryAtomicBoundary(t *testing.T) {
	manager, first := jobHideFixture(t)
	retry := *first
	retry.ID = ""
	retry.Key = strings.Repeat("d", 32)
	retry.Previous = first.ID
	retry.Sources = []utils.FileJobSource{{Path: "source", Version: "new"}}
	accepted, err := manager.submit(&retry)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.hide(first.Scope, first.ID, noJobAudit); err != nil {
		t.Fatal(err)
	}
	replay, err := manager.submit(&retry)
	if err != nil || replay.ID != accepted.ID {
		t.Fatalf("accepted retry blocked by hidden previous: %v", err)
	}
	next := retry
	next.Key = strings.Repeat("e", 32)
	if _, err := manager.submit(&next); err == nil || err.Error() != "not_found" {
		t.Fatalf("new retry accepted hidden previous: %v", err)
	}
	if err := manager.hide(retry.Scope, retry.ID, noJobAudit); err == nil || err.Error() != "job_not_terminal" {
		t.Fatalf("queued hide: %v", err)
	}
	if err := manager.hide(manager.scope("other"), first.ID, noJobAudit); err == nil || err.Error() != "not_found" {
		t.Fatalf("scope leak: %v", err)
	}
}

func TestJobHideAPISecurityAndAudit(t *testing.T) {
	manager, job := jobHideFixture(t)
	// Recovered jobs have no Owner. Auditing must identify the requester.
	manager.state.Jobs = manager
	manager.cancel = func() {}
	auth := testManager(t)
	router := newRouter(auth, manager.state)
	cookie := loginCookie(t, router)
	send := func(method string, session, csrf, bearer bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/jobs/"+job.ID, nil)
		req.Header.Set("Accept", "application/json")
		if session {
			req.AddCookie(cookie)
		}
		if csrf {
			req.Header.Set("X-CSRF-Token", sessionCSRF(t, auth, cookie))
		}
		if bearer {
			req.Header.Set("Authorization", "Bearer abcdef0123456789abcdef0123456789")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		session, csrf, bearer bool
		want                  int
	}{{false, false, false, 401}, {false, false, true, 401}, {true, false, false, 403}} {
		if w := send("DELETE", tc.session, tc.csrf, tc.bearer); w.Code != tc.want {
			t.Fatalf("security: %d %s", w.Code, w.Body.String())
		}
	}
	originalScope := job.Scope
	job.Scope = manager.scope("other")
	if w := send("DELETE", true, true, false); w.Code != 404 {
		t.Fatalf("scope=%d", w.Code)
	}
	job.Scope = originalScope
	identity := manager.rootIdentity
	manager.rootIdentity = "replaced"
	if w := send("DELETE", true, true, false); w.Code != 404 {
		t.Fatalf("root=%d", w.Code)
	}
	manager.rootIdentity = identity
	job.State = "running"
	if w := send("DELETE", true, true, false); w.Code != 409 || !strings.Contains(w.Body.String(), "job_not_terminal") {
		t.Fatalf("active=%d %s", w.Code, w.Body.String())
	}
	job.State = "partial"
	for i := 0; i < 2; i++ {
		if w := send("DELETE", true, true, false); w.Code != 204 || w.Body.Len() != 0 {
			t.Fatalf("hide=%d %s", w.Code, w.Body.String())
		}
	}
	if w := send("GET", true, false, false); w.Code != 404 {
		t.Fatalf("hidden get=%d", w.Code)
	}
	data, err := os.ReadFile(manager.state.Audit.path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "job.hide" && event.Outcome == "success" {
			found = true
			if event.Principal != "admin" || event.JobID != job.ID || event.Path != "" || event.AuthMethod != "session" {
				t.Fatalf("wrong audit: %#v", event)
			}
		}
	}
	if !found {
		t.Fatal("missing hide audit")
	}
	manager.state.SetReady(false)
	if w := send("DELETE", true, true, false); w.Code != 503 {
		t.Fatalf("readiness=%d", w.Code)
	}
}

func TestJobHideLegacyAndTerminalStates(t *testing.T) {
	for _, terminal := range []string{"succeeded", "failed", "partial", "cancelled", "interrupted"} {
		t.Run(terminal, func(t *testing.T) {
			manager, job := jobHideFixture(t)
			job.State = terminal
			if err := saveJob(manager.directory, job); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(manager.directory, job.ID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), `"hidden"`) {
				t.Fatal("legacy fixture includes hidden")
			}
			loaded, err := loadJobs(manager.directory)
			if err != nil || loaded[job.ID].Hidden {
				t.Fatalf("legacy decode: %v", err)
			}
			if err := manager.hide(job.Scope, job.ID, noJobAudit); err != nil {
				t.Fatal(err)
			}
			if manager.jobs[job.ID].State != terminal {
				t.Fatal("hide changed terminal state")
			}
		})
	}
}

func TestJobHideAuditFailure(t *testing.T) {
	for _, failure := range []string{"attempted", "success"} {
		t.Run(failure, func(t *testing.T) {
			manager, job := jobHideFixture(t)
			err := manager.hide(job.Scope, job.ID, func(outcome, code string) error {
				if outcome == failure {
					manager.state.Audit.mu.Lock()
					manager.state.Audit.healthy = false
					manager.state.Audit.mu.Unlock()
				}
				return manager.state.Record(AuditEvent{Event: "job.hide", Outcome: outcome, JobID: job.ID, Principal: "admin", Code: code})
			})
			if err == nil || err.Error() != "audit_unavailable" || manager.state.Ready() {
				t.Fatalf("audit failure: %v", err)
			}
			if manager.jobs[job.ID].Hidden != (failure == "success") {
				t.Fatal("incorrect persistence boundary after audit failure")
			}
		})
	}
}

func TestJobHideReadOnlyAndUploadOnly(t *testing.T) {
	for _, mode := range []string{"reader", "uploader"} {
		t.Run(mode, func(t *testing.T) {
			manager, job := jobHideFixture(t)
			manager.state.Jobs = manager
			manager.cancel = func() {}
			oldReader, oldUploader := reader, uploader
			reader = mode == "reader"
			uploader = mode == "uploader"
			t.Cleanup(func() { reader = oldReader; uploader = oldUploader })
			auth := testManager(t)
			router := newRouter(auth, manager.state)
			cookie := loginCookie(t, router)
			req := httptest.NewRequest("DELETE", "/api/jobs/"+job.ID, nil)
			req.AddCookie(cookie)
			req.Header.Set("Accept", "application/json")
			req.Header.Set("X-CSRF-Token", sessionCSRF(t, auth, cookie))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code < 400 || manager.jobs[job.ID].Hidden {
				t.Fatalf("restricted delete=%d", w.Code)
			}
		})
	}
}

func TestJobHideHTTPKeyReplayWithMissingSource(t *testing.T) {
	manager, job := jobHideFixture(t)
	manager.state.Jobs = manager
	manager.cancel = func() {}
	auth := testManager(t)
	router := newRouter(auth, manager.state)
	cookie := loginCookie(t, router)
	if err := manager.hide(job.Scope, job.ID, noJobAudit); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/jobs", strings.NewReader(`{"kind":"copy","key":"`+job.Key+`","path":"missing","version":"v","destination":"out"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", sessionCSRF(t, auth, cookie))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "job_hidden") || len(manager.jobs) != 1 {
		t.Fatalf("hidden key replay=%d %s", w.Code, w.Body.String())
	}
}
