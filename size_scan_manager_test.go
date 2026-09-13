package main

import (
	"encoding/json"
	"github.com/irains/fileharbor/conf"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSizeScanAPIAndMutationInvalidation(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	if err := os.Mkdir(filepath.Join(conf.FileHarbor, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf.FileHarbor, "folder", "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t)
	state := newTestState(t)
	router := newRouter(manager, state)
	cookie := loginCookie(t, router)
	csrf := sessionCSRF(t, manager, cookie)
	request := httptest.NewRequest("POST", "/api/size-scans", strings.NewReader(`{"path":"folder"}`))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 202 {
		t.Fatalf("start=%d %s", response.Code, response.Body.String())
	}
	var body struct {
		Scan sizeScan `json:"scan"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	id := body.Scan.ID
	for deadline := time.Now().Add(time.Second); ; {
		response = requestDirectoryBrowser(t, router, cookie, "/api/size-scans/"+id)
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Scan.State != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not terminate")
		}
		time.Sleep(time.Millisecond)
	}
	if body.Scan.State != "succeeded" || body.Scan.Result.Bytes != 4 || body.Scan.Result.Files != 1 {
		t.Fatalf("scan=%#v", body.Scan)
	}

	startAgain := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/api/size-scans", strings.NewReader(`{"path":"folder"}`))
		request.AddCookie(cookie)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	cached := startAgain()
	if cached.Code != 200 || !strings.Contains(cached.Body.String(), id) {
		t.Fatal("completed scan was not cached")
	}
	if err := state.Record(AuditEvent{Event: "file.create", Outcome: "success", Path: "folder/new"}); err != nil {
		t.Fatal(err)
	}
	fresh := startAgain()
	if fresh.Code != 202 || strings.Contains(fresh.Body.String(), id) {
		t.Fatal("mutation reused stale scan")
	}
	state.scanWorkers.Wait()
	response = requestDirectoryBrowser(t, router, cookie, "/api/properties?basic=true&path=folder")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"size":0`) {
		t.Fatal("basic properties performed scan")
	}
}

func TestTrashSizeScanAPI(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	manager := testManager(t)
	state := newTestState(t)
	bin, err := newRecycleBin(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf.FileHarbor, "file"), []byte("trash"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := bin.Move("file"); err != nil {
		t.Fatal(err)
	}
	router := newRouter(manager, state)
	cookie := loginCookie(t, router)
	request := httptest.NewRequest("POST", "/api/size-scans", strings.NewReader(`{"kind":"trash","path":""}`))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", sessionCSRF(t, manager, cookie))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 202 {
		t.Fatalf("start=%d", response.Code)
	}
	var body struct {
		Scan sizeScan `json:"scan"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	state.scanWorkers.Wait()
	response = requestDirectoryBrowser(t, router, cookie, "/api/size-scans/"+body.Scan.ID)
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Scan.Kind != "trash" || body.Scan.State != "succeeded" || body.Scan.Result == nil || body.Scan.Result.Bytes != 5 {
		t.Fatalf("scan=%+v", body.Scan)
	}
}
