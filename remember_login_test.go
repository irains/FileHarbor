package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/irains/fileharbor/auth"
	"github.com/irains/fileharbor/conf"
)

func TestLoginRememberCookieContracts(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	router := newTestRouter(t, testManager(t))
	for _, test := range []struct {
		route, body, contentType string
		remember                 bool
		status                   int
	}{
		{"/api/session/login", `{"username":"admin","password":"a durable password"}`, "application/json", false, 200},
		{"/api/session/login", `{"username":"admin","password":"a durable password","remember":true}`, "application/json", true, 200},
		{"/login", "username=admin&password=a+durable+password&remember=on", "application/x-www-form-urlencoded", true, 302},
		{"/login", "username=admin&password=a+durable+password", "application/x-www-form-urlencoded", false, 302},
	} {
		request := httptest.NewRequest(http.MethodPost, test.route, strings.NewReader(test.body))
		request.Header.Set("Content-Type", test.contentType)
		result := httptest.NewRecorder()
		router.ServeHTTP(result, request)
		if result.Code != test.status {
			t.Fatalf("login status=%d", result.Code)
		}
		cookie := sessionCookieFromResponse(t, result)
		if test.remember {
			if cookie.MaxAge != int(auth.RememberedSessionDuration.Seconds()) || cookie.Expires.IsZero() {
				t.Fatal("remembered cookie not persistent")
			}
		} else if cookie.MaxAge != 0 || !cookie.Expires.IsZero() {
			t.Fatal("default login persisted cookie")
		}
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatal("cookie protections missing")
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/session/login", strings.NewReader(`{"username":"admin","password":"a durable password","remember":"true"}`))
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest {
		t.Fatal("invalid remember type accepted")
	}
}
