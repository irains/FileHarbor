package main

import (
	"archive/zip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irains/fileharbor/conf"
)

func TestArchivePreviewAPI(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	file, err := os.Create(filepath.Join(conf.FileHarbor, "sample.zip"))
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	member, err := writer.Create("nested/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(t, testManager(t))
	cookie := loginCookie(t, router)
	for _, test := range []struct {
		query         string
		authenticated bool
		status        int
		contains      string
	}{
		{"?path=sample.zip", false, 401, ""},
		{"?path=sample.zip", true, 200, `"verification":"metadata_only"`},
		{"?path=sample.zip&version=stale", true, 409, "source_changed"},
		{"?path=../outside.zip", true, 400, ""},
		{"?path=sample.zip&path=other.zip", true, 400, ""},
		{"?path=sample.zip&extra=true", true, 400, ""},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/archive/preview"+test.query, nil)
		if test.authenticated {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status || test.contains != "" && !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
		}
		if test.authenticated && !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("preview is cacheable")
		}
	}
	entries, err := os.ReadDir(conf.FileHarbor)
	if err != nil || len(entries) != 1 {
		t.Fatal("preview changed workspace")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/archive/preview?path=sample.zip", nil)
	request.Header.Set("Authorization", "Bearer abcdef0123456789abcdef0123456789")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("preview accepted bearer")
	}
}

func TestReadScanLimiter(t *testing.T) {
	limiter := scanLimiter{owners: make(map[string]bool)}
	first, ok := limiter.acquire("one")
	if !ok {
		t.Fatal("first rejected")
	}
	if _, ok := limiter.acquire("one"); ok {
		t.Fatal("owner limit bypassed")
	}
	second, ok := limiter.acquire("two")
	if !ok {
		t.Fatal("second rejected")
	}
	if _, ok := limiter.acquire("three"); ok {
		t.Fatal("global limit bypassed")
	}
	first()
	first()
	third, ok := limiter.acquire("three")
	if !ok {
		t.Fatal("release leaked slot")
	}
	second()
	third()
	if limiter.active != 0 || len(limiter.owners) != 0 {
		t.Fatal("limiter leaked owner")
	}
}
