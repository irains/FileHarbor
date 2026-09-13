package main

import (
	"encoding/json"
	"github.com/irains/fileharbor/conf"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFavoritesSessionCSRFAuditAndSharedViews(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	manager := testManager(t)
	state := newTestState(t)
	router := newRouter(manager, state)
	cookie := loginCookie(t, router)
	second := loginCookie(t, router)
	csrf := sessionCSRF(t, manager, cookie)
	post := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/api/favorites", strings.NewReader(`{"path":"","label":"Root favorite"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", token)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := post(""); response.Code != 403 {
		t.Fatalf("csrf=%d", response.Code)
	}
	before := len(readAuditEvents(t, state))
	response := post(csrf)
	if response.Code != 200 {
		t.Fatalf("create=%d %s", response.Code, response.Body.String())
	}
	var created struct {
		Entry Favorite `json:"entry"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/favorites", nil)
	request.AddCookie(second)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), created.Entry.ID) {
		t.Fatal("second session cannot see favorite")
	}
	events := readAuditEvents(t, state)[before:]
	if len(events) != 2 || events[0].Outcome != "attempted" || events[1].Outcome != "success" {
		t.Fatal("favorite audit missing")
	}
	request = httptest.NewRequest("GET", "/api/favorites", nil)
	request.Header.Set("Authorization", "Bearer abcdef0123456789abcdef0123456789")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("bearer can list favorites")
	}
}
