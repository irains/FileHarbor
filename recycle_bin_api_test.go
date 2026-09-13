package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irains/fileharbor/conf"
	"github.com/irains/fileharbor/utils"
)

func TestRecycleBinAPIUsesSessionCSRFAndAudit(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "file"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			previousRoot, previousReader, previousUploader, previousBasePath := conf.FileHarbor, reader, uploader, basePath
			conf.FileHarbor, reader, uploader, basePath = t.TempDir(), false, false, ""
			t.Cleanup(func() {
				conf.FileHarbor, reader, uploader, basePath = previousRoot, previousReader, previousUploader, previousBasePath
			})
			item := "notes.txt"
			if directory {
				item = "folder"
				if err := os.Mkdir(filepath.Join(conf.FileHarbor, item), 0700); err != nil {
					t.Fatal(err)
				}
			}
			contentPath := filepath.Join(conf.FileHarbor, item)
			if directory {
				contentPath = filepath.Join(contentPath, "notes.txt")
			}
			if err := os.WriteFile(contentPath, []byte("contents"), 0644); err != nil {
				t.Fatal(err)
			}
			manager := testManager(t)
			state := newTestState(t)
			router := newRouter(manager, state)
			cookie := loginCookie(t, router)
			csrf := sessionCSRF(t, manager, cookie)

			for _, auth := range []string{"anonymous", "csrf", "reader"} {
				denied := httptest.NewRequest(http.MethodPost, "/do/rm", strings.NewReader(url.Values{"path": {item}}.Encode()))
				denied.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if auth != "anonymous" {
					denied.AddCookie(cookie)
				}
				if auth != "csrf" {
					denied.Header.Set("X-CSRF-Token", csrf)
				}
				reader = auth == "reader"
				result := httptest.NewRecorder()
				if auth == "reader" {
					newRouter(manager, state).ServeHTTP(result, denied)
				} else {
					router.ServeHTTP(result, denied)
				}
				reader = false
				expected := http.StatusForbidden
				if auth == "reader" {
					expected = http.StatusNotFound
				}
				if auth == "anonymous" {
					expected = http.StatusUnauthorized
				}
				if result.Code != expected {
					t.Fatalf("%s status=%d", auth, result.Code)
				}
				if _, err := os.Stat(contentPath); err != nil {
					t.Fatal("rejected request changed source")
				}
			}
			beforeAudit := len(readAuditEvents(t, state))
			request := httptest.NewRequest(http.MethodPost, "/do/rm", strings.NewReader(url.Values{"path": {item}}.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("X-CSRF-Token", csrf)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("trash move = %d: %s", response.Code, response.Body.String())
			}
			var moved struct {
				Entry TrashEntry `json:"entry"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &moved); err != nil || moved.Entry.ID == "" || moved.Entry.OriginalPath != item {
				t.Fatalf("trash move response = %s, %v", response.Body.String(), err)
			}
			if _, err := os.Lstat(filepath.Join(conf.FileHarbor, item)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source was not moved to recycle bin: %v", err)
			}
			events := readAuditEvents(t, state)
			if len(events) != beforeAudit+2 || events[len(events)-2].Event != "file.trash" || events[len(events)-2].Outcome != "attempted" || events[len(events)-1].Event != "file.trash" || events[len(events)-1].Outcome != "success" || events[len(events)-1].Path != item {
				t.Fatalf("trash audit = %#v", events[beforeAudit:])
			}

			request = httptest.NewRequest(http.MethodGet, "/api/trash", nil)
			request.AddCookie(cookie)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"original_path":"`+item+`"`) {
				t.Fatalf("trash listing = %d: %s", response.Code, response.Body.String())
			}

			request = httptest.NewRequest(http.MethodPost, "/api/trash/"+moved.Entry.ID+"/purge", strings.NewReader(`{"confirmation":"DELETE"}`))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(cookie)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "csrf_invalid") {
				t.Fatalf("purge missing csrf = %d: %s", response.Code, response.Body.String())
			}

			request = httptest.NewRequest(http.MethodPost, "/api/trash/"+moved.Entry.ID+"/restore", nil)
			request.Header.Set("X-CSRF-Token", csrf)
			request.AddCookie(cookie)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("restore = %d: %s", response.Code, response.Body.String())
			}
			if data, err := os.ReadFile(contentPath); err != nil || string(data) != "contents" {
				t.Fatalf("restored file = %q, %v", data, err)
			}

			bearer := httptest.NewRequest(http.MethodGet, "/api/trash", nil)
			bearer.Header.Set("Authorization", "Bearer abcdef0123456789abcdef0123456789")
			response = httptest.NewRecorder()
			router.ServeHTTP(response, bearer)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("trash bearer access = %d: %s", response.Code, response.Body.String())
			}

		})
	}
}

func TestRecycleBinPermanentActionsRequireExactConfirmation(t *testing.T) {
	previousRoot, previousReader, previousUploader, previousBasePath := conf.FileHarbor, reader, uploader, basePath
	conf.FileHarbor, reader, uploader, basePath = t.TempDir(), false, false, ""
	t.Cleanup(func() {
		conf.FileHarbor, reader, uploader, basePath = previousRoot, previousReader, previousUploader, previousBasePath
	})
	if err := os.WriteFile(filepath.Join(conf.FileHarbor, "notes.txt"), []byte("contents"), 0644); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t)
	state := newTestState(t)
	router := newRouter(manager, state)
	cookie := loginCookie(t, router)
	csrf := sessionCSRF(t, manager, cookie)

	move := httptest.NewRequest(http.MethodPost, "/do/rm", strings.NewReader(url.Values{"path": {"notes.txt"}}.Encode()))
	move.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	move.Header.Set("X-CSRF-Token", csrf)
	move.AddCookie(cookie)
	movedResponse := httptest.NewRecorder()
	router.ServeHTTP(movedResponse, move)
	if movedResponse.Code != http.StatusOK {
		t.Fatalf("trash move = %d: %s", movedResponse.Code, movedResponse.Body.String())
	}
	var moved struct {
		Entry TrashEntry `json:"entry"`
	}
	if err := json.Unmarshal(movedResponse.Body.Bytes(), &moved); err != nil {
		t.Fatal(err)
	}

	purge := httptest.NewRequest(http.MethodPost, "/api/trash/"+moved.Entry.ID+"/purge", strings.NewReader(`{"confirmation":"delete"}`))
	purge.Header.Set("Content-Type", "application/json")
	purge.Header.Set("X-CSRF-Token", csrf)
	purge.AddCookie(cookie)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, purge)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "confirmation_required") {
		t.Fatalf("non-exact purge confirmation = %d: %s", response.Code, response.Body.String())
	}

	empty := httptest.NewRequest(http.MethodPost, "/api/trash/empty", strings.NewReader(`{"confirmation":"DELETE","extra":true}`))
	empty.Header.Set("Content-Type", "application/json")
	empty.Header.Set("X-CSRF-Token", csrf)
	empty.AddCookie(cookie)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, empty)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_path") {
		t.Fatalf("unknown confirmation field = %d: %s", response.Code, response.Body.String())
	}

	purge = httptest.NewRequest(http.MethodPost, "/api/trash/"+moved.Entry.ID+"/purge", strings.NewReader(`{"confirmation":"DELETE"}`))
	purge.Header.Set("Content-Type", "application/json")
	purge.Header.Set("X-CSRF-Token", csrf)
	purge.AddCookie(cookie)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, purge)
	if response.Code != http.StatusOK {
		t.Fatalf("exact purge confirmation = %d: %s", response.Code, response.Body.String())
	}
}

func TestRecycleDirectoryBatchAPI(t *testing.T) {
	previousRoot, previousReader, previousUploader, previousBasePath := conf.FileHarbor, reader, uploader, basePath
	conf.FileHarbor, reader, uploader, basePath = t.TempDir(), false, false, ""
	t.Cleanup(func() {
		conf.FileHarbor, reader, uploader, basePath = previousRoot, previousReader, previousUploader, previousBasePath
	})
	seedTrashDirectory(t, filepath.Join(conf.FileHarbor, "folder"))
	if err := os.Mkdir(filepath.Join(conf.FileHarbor, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf.FileHarbor, "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(conf.FileHarbor, "stale"), 0700); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t)
	state := newTestState(t)
	router := newRouter(manager, state)
	cookie := loginCookie(t, router)
	csrf := sessionCSRF(t, manager, cookie)
	response := requestDirectoryBrowser(t, router, cookie, "/api/listing")
	var listing struct {
		Directory struct {
			ListingToken string              `json:"listing_token"`
			Entries      []utils.ItemRequest `json:"entries"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || listing.Directory.ListingToken == "" {
		t.Fatal("listing failed")
	}

	post := func(token string) *httptest.ResponseRecorder {
		body, err := json.Marshal(batchRequest{ListingToken: token, Entries: listing.Directory.Entries})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/do/batch/delete", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(cookie)
		result := httptest.NewRecorder()
		router.ServeHTTP(result, request)
		return result
	}
	if result := post("invalid"); result.Code != http.StatusConflict {
		t.Fatalf("invalid token status=%d", result.Code)
	}
	before := len(readAuditEvents(t, state))
	result := post(listing.Directory.ListingToken)
	var body struct {
		Code  string             `json:"code"`
		Items []utils.ItemResult `json:"items"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil || result.Code != http.StatusOK || body.Code != "" {
		t.Fatalf("batch=%d %s", result.Code, result.Body.String())
	}
	codes := map[string]string{}
	for _, item := range body.Items {
		codes[item.Name] = item.Code
	}
	if codes["folder"] != "trashed" || codes["empty"] != "trashed" || codes["file"] != "trashed" || codes["stale"] != "trashed" {
		t.Fatalf("codes=%v", codes)
	}
	events := readAuditEvents(t, state)[before:]
	if len(events) != 2 || events[0].Outcome != "attempted" || events[1].Outcome != "success" || events[1].Event != "batch.trash" {
		t.Fatalf("audit=%#v", events)
	}
	bin, err := newRecycleBin(state)
	if err != nil {
		t.Fatal(err)
	}
	page, err := bin.List("")
	if err != nil || len(page.Entries) != 4 {
		t.Fatal("batch records missing")
	}
	for _, entry := range page.Entries {
		if _, err := bin.Restore(entry.ID); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(conf.FileHarbor, "folder", "nested", "data")); err != nil || string(data) != "contents" {
		t.Fatal("batch restored data differs")
	}
}
