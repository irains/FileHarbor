package main

import (
	"context"
	"github.com/irains/fileharbor/conf"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadDirectoriesValidateBeforeCreation(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	for _, names := range [][]string{{"valid", "../escape"}, {"Case/a", "case/b"}, {"valid", ".fileharbor-extract-reserved"}} {
		created, err := prepareUploadDirectories(context.Background(), "", names)
		if err == nil || len(created) != 0 {
			t.Fatalf("accepted invalid manifest: %v", names)
		}
	}
	created, err := prepareUploadDirectories(context.Background(), "", []string{"folder/nested/empty", "folder/other"})
	if err != nil || len(created) != 4 {
		t.Fatalf("created=%v err=%v", created, err)
	}
	created, err = prepareUploadDirectories(context.Background(), "", []string{"folder/nested/empty"})
	if err != nil || len(created) != 0 {
		t.Fatal("existing directory not reused")
	}
	if err := os.WriteFile(filepath.Join(conf.FileHarbor, "file"), []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	created, err = prepareUploadDirectories(context.Background(), "", []string{"new", "file/child"})
	if err == nil || len(created) != 0 {
		t.Fatal("file conflict changed workspace")
	}
	if data, _ := os.ReadFile(filepath.Join(conf.FileHarbor, "file")); string(data) != "sentinel" {
		t.Fatal("file overwritten")
	}
}

func TestUploadDirectoryAPISessionOnly(t *testing.T) {
	previousRoot, previousReader, previousUploader := conf.FileHarbor, reader, uploader
	conf.FileHarbor, reader, uploader = t.TempDir(), true, true
	t.Cleanup(func() { conf.FileHarbor, reader, uploader = previousRoot, previousReader, previousUploader })
	manager := testManager(t)
	router := newTestRouter(t, manager)
	cookie := loginCookie(t, router)
	csrf := sessionCSRF(t, manager, cookie)
	for _, mode := range []string{"bearer", "csrf", "session"} {
		request := httptest.NewRequest("POST", "/api/uploads/directories", strings.NewReader(`{"path":"","directories":["folder/empty"]}`))
		request.Header.Set("Content-Type", "application/json")
		if mode == "bearer" {
			request.Header.Set("Authorization", "Bearer abcdef0123456789abcdef0123456789")
		} else {
			request.AddCookie(cookie)
		}
		if mode != "csrf" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		expected := 403
		if mode == "session" {
			expected = 200
		}
		if response.Code != expected {
			t.Fatalf("%s status=%d body=%s", mode, response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(conf.FileHarbor, "folder", "empty")); err != nil {
		t.Fatal(err)
	}
}
