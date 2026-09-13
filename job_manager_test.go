package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irains/fileharbor/conf"
	"github.com/irains/fileharbor/utils"
)

func TestJobAPICompletionIdempotencyAndPersistence(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	os.Mkdir(filepath.Join(conf.FileHarbor, "out"), 0700)
	os.WriteFile(filepath.Join(conf.FileHarbor, "source"), []byte("data"), 0600)
	auth := testManager(t)
	state := newTestState(t)
	router := newRouter(auth, state)
	cookie := loginCookie(t, router)
	_, _, info, _ := utils.ResolveExisting("source", false)
	body, _ := json.Marshal(map[string]any{"kind": "copy", "key": strings.Repeat("a", 32), "path": "source", "version": utils.EntryVersion(info), "destination": "out"})
	post := func(csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/jobs", strings.NewReader(string(body)))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set("X-CSRF-Token", sessionCSRF(t, auth, cookie))
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	if response := post(false); response.Code != 403 {
		t.Fatalf("csrf=%d", response.Code)
	}
	response := post(true)
	if response.Code != 202 {
		t.Fatalf("submit=%d %s", response.Code, response.Body.String())
	}
	var result struct {
		Job FileJob `json:"job"`
	}
	json.Unmarshal(response.Body.Bytes(), &result)
	id := result.Job.ID
	for deadline := time.Now().Add(3 * time.Second); ; {
		state.Jobs.mu.Lock()
		done := jobTerminal(state.Jobs.jobs[id].State)
		state.Jobs.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(conf.FileHarbor, "out", "source"))
	if err != nil || string(data) != "data" {
		t.Fatal("output mismatch")
	}
	response = post(true)
	json.Unmarshal(response.Body.Bytes(), &result)
	if result.Job.ID != id {
		t.Fatal("duplicate submission created a new job")
	}
	saved, err := loadJobs(filepath.Join(state.Dir, "jobs"))
	if err != nil || saved[id].State != "succeeded" {
		t.Fatalf("persistence=%v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(state.Dir, "jobs", id+".json"))
	if strings.Contains(string(raw), "SessionID") || strings.Contains(string(raw), sessionCSRF(t, auth, cookie)) {
		t.Fatal("secret persisted")
	}
}
func TestQueuedJobRecoveryNeverExecutes(t *testing.T) {
	directory := t.TempDir()
	id := strings.Repeat("b", 32)
	job := &FileJob{Schema: 1, ID: id, Scope: strings.Repeat("c", 64), Key: strings.Repeat("d", 32), Kind: "copy", State: "running", Sources: []utils.FileJobSource{{Path: "source", Version: "v"}}, Intent: "out/source", Created: time.Now(), Updated: time.Now()}
	if err := saveJob(directory, job); err != nil {
		t.Fatal(err)
	}
	recovered, err := loadJobs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if recovered[id].State != "interrupted" || recovered[id].Intent != "out/source" {
		t.Fatal("recovery lost uncertainty")
	}
}

func TestJobShutdownAndReopenInterruptsQueuedWork(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	os.Mkdir(filepath.Join(conf.FileHarbor, "out"), 0700)
	os.WriteFile(filepath.Join(conf.FileHarbor, "source"), []byte("data"), 0600)
	state := newTestState(t)
	manager, err := newJobManager(state)
	if err != nil {
		t.Fatal(err)
	}
	state.Jobs = manager
	_, _, info, _ := utils.ResolveExisting("source", false)
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		utils.WithOperationLock(func() error { close(locked); <-release; return nil })
	}()
	<-locked
	job, err := manager.submit(&FileJob{Owner: "owner", Scope: manager.scope("owner"), Key: strings.Repeat("e", 32), Kind: "copy", Sources: []utils.FileJobSource{{Path: "source", Version: utils.EntryVersion(info)}}, Destination: "out"})
	if err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	manager.stop()
	close(release)
	<-done
	loaded, err := loadJobs(manager.directory)
	if err != nil {
		t.Fatal(err)
	}
	if loaded[job.ID].State != "interrupted" {
		t.Fatalf("shutdown state=%s", loaded[job.ID].State)
	}
	restarted, err := newJobManager(state)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.stop()
	if len(restarted.list(restarted.scope("other"))) != 0 {
		t.Fatal("cross-owner record leak")
	}
	entries, _ := os.ReadDir(filepath.Join(conf.FileHarbor, "out"))
	if len(entries) != 0 {
		t.Fatal("restart automatically wrote output")
	}
}

func TestJobPersistenceFailureDisablesReadiness(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	state := newTestState(t)
	manager, err := newJobManager(state)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stop()
	manager.directory = filepath.Join(state.Dir, "missing")
	err = manager.persist(&FileJob{ID: strings.Repeat("f", 32)})
	if err == nil || state.Ready() {
		t.Fatal("metadata failure left service writable")
	}
}

func TestJobTerminalPersistenceFailureNeverReportsSuccess(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	state := newTestState(t)
	manager, err := newJobManager(state)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stop()
	manager.directory = filepath.Join(state.Dir, "missing")
	job := &FileJob{ID: strings.Repeat("f", 32), State: "succeeded", Published: []string{"out/file"}}
	if manager.persist(job) == nil || job.State != "partial" || state.Ready() {
		t.Fatalf("unsafe terminal state: %s", job.State)
	}
}

func TestJobScopeIncludesRootIdentity(t *testing.T) {
	manager := &JobManager{state: &RuntimeState{ManagedRoot: "same-path"}, rootIdentity: "first"}
	first := manager.scope("owner")
	manager.rootIdentity = "replacement"
	if first == manager.scope("owner") {
		t.Fatal("root replacement inherited old task scope")
	}
}

func TestJobRetryEndpointCreatesNewAttemptWithoutOverwrite(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	os.Mkdir(filepath.Join(conf.FileHarbor, "out"), 0700)
	os.WriteFile(filepath.Join(conf.FileHarbor, "source"), []byte("data"), 0600)
	auth := testManager(t)
	state := newTestState(t)
	router := newRouter(auth, state)
	cookie := loginCookie(t, router)
	_, _, info, _ := utils.ResolveExisting("source", false)
	body := map[string]any{"kind": "copy", "key": strings.Repeat("a", 32), "path": "source", "version": utils.EntryVersion(info), "destination": "out"}
	post := func(url string) FileJob {
		data, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", url, strings.NewReader(string(data)))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", sessionCSRF(t, auth, cookie))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 202 {
			t.Fatalf("submit status=%d", w.Code)
		}
		var result struct {
			Job FileJob `json:"job"`
		}
		json.Unmarshal(w.Body.Bytes(), &result)
		if strings.Contains(w.Body.String(), `"scope"`) || strings.Contains(w.Body.String(), `"key"`) {
			t.Fatal("private metadata exposed")
		}
		return result.Job
	}
	wait := func(id string) string {
		for deadline := time.Now().Add(5 * time.Second); ; {
			state.Jobs.mu.Lock()
			status := state.Jobs.jobs[id].State
			state.Jobs.mu.Unlock()
			if jobTerminal(status) {
				return status
			}
			if time.Now().After(deadline) {
				t.Fatal("task timeout")
			}
			time.Sleep(time.Millisecond)
		}
	}
	first := post("/api/jobs")
	if wait(first.ID) != "succeeded" {
		t.Fatal("first failed")
	}
	body["key"] = strings.Repeat("b", 32)
	second := post("/api/jobs/" + first.ID + "/retry")
	if second.ID == first.ID || second.Previous != first.ID {
		t.Fatal("missing attempt lineage")
	}
	if wait(second.ID) != "failed" {
		t.Fatal("retry did not reject existing output")
	}
	data, _ := os.ReadFile(filepath.Join(conf.FileHarbor, "out", "source"))
	if string(data) != "data" {
		t.Fatal("retry changed existing output")
	}
}

func TestJobAPISessionAndListingBoundaries(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	auth := testManager(t)
	state := newTestState(t)
	router := newRouter(auth, state)
	for _, url := range []string{"/api/jobs", "/api/jobs/" + strings.Repeat("a", 32)} {
		req := httptest.NewRequest("GET", url, nil)
		req.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("anonymous task read=%d", w.Code)
		}
	}
	cookie := loginCookie(t, router)
	body := `{"kind":"copy","key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","listing_token":"invalid","entries":[{"name":"source","version":"v"}],"destination":""}`
	req := httptest.NewRequest("POST", "/api/jobs", strings.NewReader(body))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", sessionCSRF(t, auth, cookie))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatalf("invalid listing=%d", w.Code)
	}
	if len(state.Jobs.jobs) != 0 {
		t.Fatal("invalid authorization created a job")
	}
}
