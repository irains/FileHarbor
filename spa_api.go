package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/auth"
	"github.com/irains/fileharbor/conf"
	"github.com/irains/fileharbor/utils"
)

const maxAPIJSONBytes = int64(64 << 10)

type sessionLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

type sessionPayload struct {
	Username  string `json:"username"`
	CSRFToken string `json:"csrf_token"`
	ExpiresAt string `json:"expires_at"`
}

type capabilitiesPayload struct {
	Browse     bool `json:"browse"`
	Upload     bool `json:"upload"`
	Mutate     bool `json:"mutate"`
	EditorSave bool `json:"editor_save"`
}

type sessionResponse struct {
	OK           bool                `json:"ok"`
	Session      sessionPayload      `json:"session"`
	BasePath     string              `json:"base_path"`
	Locale       string              `json:"locale"`
	Capabilities capabilitiesPayload `json:"capabilities"`
}

type listingResponse struct {
	OK        bool             `json:"ok"`
	Directory listingDirectory `json:"directory"`
	Disk      *diskResponse    `json:"disk,omitempty"`
}

type listingDirectory struct {
	Path         string         `json:"path"`
	ParentPath   *string        `json:"parent_path"`
	ListingToken string         `json:"listing_token"`
	Entries      []listingEntry `json:"entries"`
	Truncated    bool           `json:"truncated"`
}

type listingEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	SizeBytes   int64  `json:"size_bytes"`
	ModifiedAt  string `json:"modified_at"`
	Mode        string `json:"mode"`
	Extension   string `json:"extension,omitempty"`
	IsArchive   bool   `json:"is_archive"`
	Previewable bool   `json:"previewable"`
	Editable    bool   `json:"editable"`
	Version     string `json:"version"`
}

type diskResponse struct {
	TotalBytes  uint64 `json:"total_bytes"`
	FreeBytes   uint64 `json:"free_bytes"`
	UsedPercent uint64 `json:"used_percent"`
}

type editorPayload struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Content    string `json:"content,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	ModifiedAt string `json:"modified_at"`
	Extension  string `json:"extension"`
	Version    string `json:"version"`
}

type editorContentResponse struct {
	OK     bool          `json:"ok"`
	Editor editorPayload `json:"editor"`
}

type editorSaveRequest struct {
	Path            string `json:"path"`
	Content         string `json:"content"`
	ExpectedVersion string `json:"expected_version"`
}

func decodeStrictJSON(c *gin.Context, destination any, limit int64) error {
	if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		return errors.New("content type must be application/json")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("unexpected JSON value")
	}
	return nil
}

func jsonRequestError(c *gin.Context, err error) {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		jsonError(c, http.StatusRequestEntityTooLarge, utils.ErrBatchLimitExceeded)
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"ok": false, "code": "invalid_request"})
}

func localeForRequest(c *gin.Context) string {
	language := strings.ToLower(c.GetHeader("Accept-Language"))
	if strings.Contains(language, "en") && !strings.Contains(language, "zh") {
		return "en"
	}
	return "zh-CN"
}

func sessionCapabilities() capabilitiesPayload {
	return capabilitiesPayload{
		Browse:     true,
		Upload:     !reader || uploader,
		Mutate:     !reader,
		EditorSave: !reader,
	}
}

func sessionPayloadFor(c *gin.Context, info auth.Info, expiresAt string) sessionResponse {
	return sessionResponse{
		OK: true,
		Session: sessionPayload{
			Username:  info.Username,
			CSRFToken: info.CSRF,
			ExpiresAt: expiresAt,
		},
		BasePath:     basePath,
		Locale:       localeForRequest(c),
		Capabilities: sessionCapabilities(),
	}
}

func sessionLoginHandler(manager *auth.Manager, state *RuntimeState) gin.HandlerFunc {
	return func(c *gin.Context) {
		setPrivateResponse(c)
		var request sessionLoginRequest
		if err := decodeStrictJSON(c, &request, maxAPIJSONBytes); err != nil {
			jsonRequestError(c, err)
			return
		}
		info, signedID, expiry, err := manager.LoginWithRemember(c.ClientIP(), request.Username, request.Password, request.Remember)
		if err != nil {
			outcome := "failure"
			if errors.Is(err, auth.ErrRateLimited) {
				outcome = "rate_limited"
			}
			_ = state.Record(AuditEvent{Event: "auth.login", Outcome: outcome, AuthMethod: "session", ClientIP: c.ClientIP()})
			switch {
			case errors.Is(err, auth.ErrRateLimited):
				c.JSON(http.StatusTooManyRequests, gin.H{"ok": false, "code": "rate_limited"})
			case errors.Is(err, auth.ErrInvalidCredentials):
				c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "code": "invalid_credentials"})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
			}
			return
		}
		_ = state.Record(AuditEvent{Event: "auth.login", Outcome: "success", Principal: info.Username, AuthMethod: "session", ClientIP: c.ClientIP()})
		http.SetCookie(c.Writer, manager.LoginCookie(signedID, expiry, request.Remember))
		http.SetCookie(c.Writer, manager.ExpiredLegacyCookie())
		c.JSON(http.StatusOK, sessionPayloadFor(c, info, expiry.UTC().Format(timeFormat)))
	}
}

const timeFormat = "2006-01-02T15:04:05Z"

func sessionGetHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		setPrivateResponse(c)
		info := authInfo(c)
		c.JSON(http.StatusOK, sessionPayloadFor(c, info, info.Expires.UTC().Format(timeFormat)))
	}
}

func sessionLogoutHandler(manager *auth.Manager, state *RuntimeState) gin.HandlerFunc {
	return func(c *gin.Context) {
		setPrivateResponse(c)
		info := authInfo(c)
		manager.Logout(info)
		_ = state.Record(AuditEvent{Event: "auth.logout", Outcome: "success", Principal: info.Username, AuthMethod: "session", ClientIP: c.ClientIP()})
		http.SetCookie(c.Writer, manager.ExpiredCookie())
		http.SetCookie(c.Writer, manager.ExpiredLegacyCookie())
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func listingHandler(manager *auth.Manager, state *RuntimeState) gin.HandlerFunc {
	return func(c *gin.Context) {
		setPrivateResponse(c)
		cleanPath, err := utils.CleanRelative(c.Query("path"), true)
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		info, err := utils.ListDirectory(cleanPath)
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		entries := make([]listingEntry, 0, len(info.Entries))
		listingItems := make([]auth.ListingItem, 0, len(info.Entries))
		for _, entry := range info.Entries {
			editable := entry.Kind == "file" && entry.Size <= maxEditorBytes && utils.IsTextFile(entry.Path, maxEditorBytes)
			entries = append(entries, listingEntry{
				Name:        entry.Name,
				Path:        entry.Path,
				Kind:        entry.Kind,
				SizeBytes:   entry.Size,
				ModifiedAt:  entry.Modified.UTC().Format(timeFormat),
				Mode:        entry.Mode,
				Extension:   entry.Extension,
				IsArchive:   entry.IsArchive,
				Previewable: entry.Kind == "file" && isInlinePreviewable(entry.Name),
				Editable:    editable,
				Version:     entry.Version,
			})
			listingItems = append(listingItems, auth.ListingItem{Name: entry.Name, Version: entry.Version})
		}
		session := authInfo(c)
		listingToken, err := manager.IssueListing(session.SessionID, cleanPath, listingItems)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
			return
		}
		var parent *string
		if cleanPath != "" {
			value := path.Dir(cleanPath)
			if value == "." {
				value = ""
			}
			parent = &value
		}
		var disk *diskResponse
		if total, free := utils.DiskUsage(conf.FileHarbor); total > 0 {
			used := total - free
			disk = &diskResponse{TotalBytes: total, FreeBytes: free, UsedPercent: used * 100 / total}
		}
		_ = logAction(state, c, "directory.list", cleanPath)
		c.JSON(http.StatusOK, listingResponse{
			OK:        true,
			Directory: listingDirectory{Path: cleanPath, ParentPath: parent, ListingToken: listingToken, Entries: entries, Truncated: info.Truncated},
			Disk:      disk,
		})
	}
}

func editorContentHandler(state *RuntimeState) gin.HandlerFunc {
	return func(c *gin.Context) {
		setPrivateResponse(c)
		file, err := utils.ReadTextFile(pathFromParam(c.Query("path")), maxEditorBytes)
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		_ = logAction(state, c, "file.edit", file.Relative)
		c.JSON(http.StatusOK, editorContentResponse{OK: true, Editor: editorPayload{
			Path: file.Relative, Name: filepath.Base(file.Relative), Content: string(file.Data), SizeBytes: file.Info.Size(), ModifiedAt: file.Info.ModTime().UTC().Format(timeFormat), Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Relative)), "."), Version: utils.EntryVersion(file.Info),
		}})
	}
}

func editorSaveHandler(state *RuntimeState) gin.HandlerFunc {
	return func(c *gin.Context) {
		setPrivateResponse(c)
		var request editorSaveRequest
		if err := decodeStrictJSON(c, &request, maxEditorBytes+maxAPIJSONBytes); err != nil {
			jsonRequestError(c, err)
			return
		}
		if int64(len(request.Content)) > maxEditorBytes {
			jsonError(c, http.StatusRequestEntityTooLarge, utils.ErrBatchLimitExceeded)
			return
		}
		if request.ExpectedVersion == "" || !utils.IsTextContent([]byte(request.Content)) {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "code": "invalid_request"})
			return
		}
		_, rel, _, err := fileForRead(request.Path)
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		if !requireAudit(c, state, "file.save", rel, 1) {
			return
		}
		updated, err := utils.WriteVersionedTextFile(pathFromParam(request.Path), request.ExpectedVersion, []byte(request.Content), maxEditorBytes)
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		if !finishMutation(c, state, "file.save", rel, 1) {
			return
		}
		c.JSON(http.StatusOK, editorContentResponse{OK: true, Editor: editorPayload{
			Path: rel, Name: filepath.Base(rel), SizeBytes: updated.Size(), ModifiedAt: updated.ModTime().UTC().Format(timeFormat), Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), "."), Version: utils.EntryVersion(updated),
		}})
	}
}
