package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irains/fileharbor/conf"
)

func TestSearchAPIAuthenticationAndFilters(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	if err := os.Mkdir(filepath.Join(conf.FileHarbor, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf.FileHarbor, "nested", "report.txt"), []byte("data"), 0600); err != nil {
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
		{"?recursive=true", false, 401, ""},
		{"?recursive=true&name=REPORT&extension=txt&min_size=4", true, 200, `"path":"nested/report.txt"`},
		{"?path=../outside", true, 400, ""},
		{"?min_size=-1", true, 400, ""},
		{"?recursive=invalid", true, 400, ""},
		{"?recursive=", true, 400, ""},
		{"?min_size=", true, 400, ""},
		{"?name=a&name=b", true, 400, ""},
		{"?unexpected=true", true, 400, ""},
		{"?modified_after=invalid", true, 400, ""},
		{"?min_size=5&max_size=4", true, 400, ""},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/search"+test.query, nil)
		if test.authenticated {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != test.status || test.contains != "" && !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("search status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestSearchAPITraversesBeyondListingLimit(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	for i := 0; i < 140; i++ {
		if err := os.WriteFile(filepath.Join(conf.FileHarbor, fmt.Sprintf("item-%03d.txt", i)), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(conf.FileHarbor, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf.FileHarbor, "nested", "needle.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	router := newTestRouter(t, testManager(t))
	cookie := loginCookie(t, router)
	response := requestDirectoryBrowser(t, router, cookie, "/api/search?recursive=true&name=needle")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"path":"nested/needle.txt"`) {
		t.Fatalf("search=%d %s", response.Code, response.Body.String())
	}
	response = requestDirectoryBrowser(t, router, cookie, "/api/search?recursive=false&name=needle")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"entries":[]`) {
		t.Fatalf("shallow=%d %s", response.Code, response.Body.String())
	}
}
