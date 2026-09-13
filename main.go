package main

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/auth"
	"github.com/irains/fileharbor/conf"
	"github.com/irains/fileharbor/utils"
)

var (
	reader   bool
	uploader bool
	basePath string
	uploads  *UploadStore
)

var (
	maxUploadBodyBytes   = int64(256 << 20)
	maxChunkBodyBytes    = int64(64 << 20)
	maxChunkStateBytes   = int64(512 << 20)
	maxChunkStorageBytes = int64(2 << 30)
	maxEditorBytes       = int64(8 << 20)

	errForcedShutdown = errors.New("forced shutdown")
)

const (
	maxChunkCount   = 4096
	contextAuthInfo = "authInfo"
)

func authInfo(c *gin.Context) auth.Info {
	if value, ok := c.Get(contextAuthInfo); ok {
		return value.(auth.Info)
	}
	return auth.Info{}
}

func recordAction(state *RuntimeState, c *gin.Context, event, outcome, path, code string, affected int) error {
	info := authInfo(c)
	principal := info.Username
	authMethod := "session"
	if info.Bearer {
		principal = "api-token"
		authMethod = "bearer"
	}
	return state.Record(AuditEvent{
		Event:      event,
		Outcome:    outcome,
		Principal:  principal,
		AuthMethod: authMethod,
		ClientIP:   c.ClientIP(),
		Path:       path,
		Affected:   affected,
		Code:       code,
	})
}

func logAction(state *RuntimeState, c *gin.Context, event, path string) error {
	return recordAction(state, c, event, "success", path, "", 0)
}

// finishMutation never reports success after its durable success record fails.
// The filesystem effect may already be committed, so callers must not attempt a
// generic rollback. RuntimeState disables readiness to block further mutations.
func finishMutation(c *gin.Context, state *RuntimeState, event, path string, affected int) bool {
	if err := recordAction(state, c, event, "success", path, "", affected); err == nil {
		return true
	}
	setPrivateResponse(c)
	c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "code": "audit_unavailable"})
	return false
}

func requireAudit(c *gin.Context, state *RuntimeState, event, path string, affected int) bool {
	info := authInfo(c)
	principal := info.Username
	authMethod := "session"
	if info.Bearer {
		principal = "api-token"
		authMethod = "bearer"
	}
	if err := state.Record(AuditEvent{
		Event:      event,
		Outcome:    "attempted",
		Principal:  principal,
		AuthMethod: authMethod,
		ClientIP:   c.ClientIP(),
		Path:       path,
		Affected:   affected,
	}); err != nil {
		setPrivateResponse(c)
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "code": "audit_unavailable"})
		return false
	}
	return true
}

func mutationAuditMiddleware(state *RuntimeState) gin.HandlerFunc {
	return func(c *gin.Context) {
		if state.Ready() {
			c.Next()
			return
		}
		setPrivateResponse(c)
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "code": "audit_unavailable"})
		c.Abort()
	}
}

func chunkDir(state *RuntimeState, fileID string) string {
	if state == nil || !validChunkID(fileID) {
		return ""
	}
	base := state.ChunksDir
	dir := filepath.Clean(filepath.Join(base, fileID))
	if !strings.HasPrefix(dir, base+string(filepath.Separator)) {
		return ""
	}
	return dir
}

func validChunkID(fileID string) bool {
	if fileID == "" || len(fileID) > 128 {
		return false
	}
	for _, r := range fileID {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func chunkDirSize(dir string) (int64, error) {
	var size int64
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !isChunkFileName(entry.Name()) {
			return 0, errors.New("invalid chunk state")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return 0, errors.New("invalid chunk state")
		}
		if info.Size() < 0 || size > maxChunkStateBytes-info.Size() {
			return maxChunkStateBytes + 1, nil
		}
		size += info.Size()
	}
	return size, nil
}

func chunkStorageSize(chunksDir string) (int64, error) {
	var size int64
	entries, err := os.ReadDir(chunksDir)
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		if !validChunkID(entry.Name()) {
			return 0, errors.New("invalid chunk state")
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return 0, errors.New("invalid chunk state")
		}
		entrySize, err := chunkDirSize(filepath.Join(chunksDir, entry.Name()))
		if err != nil || entrySize < 0 || size > maxChunkStorageBytes-entrySize {
			if err != nil {
				return 0, err
			}
			return maxChunkStorageBytes + 1, nil
		}
		size += entrySize
	}
	return size, nil
}

func isChunkFileName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func wantsJSON(c *gin.Context) bool {
	path := c.Request.URL.Path
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/do/") || c.GetHeader("Accept") == "application/json" || strings.Contains(c.GetHeader("Content-Type"), "application/json")
}

func setPrivateResponse(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
	c.Header("Vary", "Cookie")
}

func appPath(internalPath string) string {
	if internalPath == "" || internalPath == "/" {
		if basePath == "" {
			return "/"
		}
		return basePath + "/"
	}
	return basePath + internalPath
}

func isSafeAppPath(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "\\") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "." || segment == ".." || segment == "" && path != "/" {
			return false
		}
	}
	return true
}

func safeNextPath(raw string) string {
	decoded, err := url.PathUnescape(raw)
	if err != nil || decoded == "" || !strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, "//") || strings.Contains(decoded, "\\") || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return appPath("/")
	}
	parsed, err := url.ParseRequestURI(decoded)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Scheme != "" || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, "\\") {
		return appPath("/")
	}
	internalPath := parsed.Path
	if basePath != "" {
		switch {
		case internalPath == basePath:
			internalPath = "/"
		case strings.HasPrefix(internalPath, basePath+"/"):
			internalPath = strings.TrimPrefix(internalPath, basePath)
		}
	}
	if !isSafeAppPath(internalPath) {
		return appPath("/")
	}
	if parsed.RawQuery == "" {
		return appPath(internalPath)
	}
	return appPath(internalPath) + "?" + parsed.RawQuery
}

func withBasePath(next http.Handler) http.Handler {
	if basePath == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != basePath && !strings.HasPrefix(r.URL.Path, basePath+"/") {
			next.ServeHTTP(w, r)
			return
		}
		request := new(http.Request)
		*request = *r
		request.URL = new(url.URL)
		*request.URL = *r.URL
		request.URL.Path = strings.TrimPrefix(r.URL.Path, basePath)
		if request.URL.Path == "" {
			request.URL.Path = "/"
		}
		if r.URL.RawPath != "" {
			request.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, basePath)
			if request.URL.RawPath == "" {
				request.URL.RawPath = "/"
			}
		}
		request.RequestURI = request.URL.RequestURI()
		next.ServeHTTP(w, request)
	})
}

func hiddenNotFound(c *gin.Context) {
	setPrivateResponse(c)
	c.Status(http.StatusNotFound)
}

func authRequired(manager *auth.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if (c.Request.URL.Path == "/api/upload" || c.Request.URL.Path == "/api/uploads" || strings.HasPrefix(c.Request.URL.Path, "/api/uploads/")) && manager.IsBearerToken(c.GetHeader("Authorization")) {
			c.Set(contextAuthInfo, auth.Info{Bearer: true})
			c.Next()
			return
		}
		if info, ok := manager.SessionFromRequest(c.Request); ok {
			c.Set(contextAuthInfo, info)
			c.Next()
			return
		}
		setPrivateResponse(c)
		if wantsJSON(c) {
			c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "code": "unauthenticated"})
		} else {
			c.Redirect(http.StatusFound, appPath("/login")+"?next="+url.QueryEscape(safeNextPath(c.Request.URL.RequestURI())))
		}
		c.Abort()
	}
}

func csrfRequired(manager *auth.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		info := authInfo(c)
		if !manager.CheckCSRF(info, c.GetHeader("X-CSRF-Token")) {
			c.JSON(http.StatusForbidden, gin.H{"ok": false, "code": "csrf_invalid"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func jsonError(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"ok": false, "code": utils.ErrorCode(err)})
}

func operationStatus(err error) int {
	switch utils.ErrorCode(err) {
	case "destination_exists", "source_changed", "self_descendant", "destination_same_directory", "cross_device_move":
		return http.StatusConflict
	case "confirmation_required":
		return http.StatusBadRequest
	case "not_found", "not_directory":
		return http.StatusNotFound
	case "batch_limit_exceeded", "archive_limit_exceeded":
		return http.StatusRequestEntityTooLarge
	case "io_error", "execution_partial":
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

func pathFromParam(raw string) string {
	return strings.ReplaceAll(strings.TrimPrefix(raw, "/"), "\\", "/")
}

func setSafeFilePreviewHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; sandbox")
}

func isInlinePreviewable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".log", ".md", ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg", ".csv", ".go", ".py", ".java", ".c", ".h", ".cpp", ".hpp", ".sh", ".ps1":
		return true
	default:
		return false
	}
}

func renderDirectory(c *gin.Context, state *RuntimeState, rawPath string) {
	cleanPath, err := utils.CleanRelative(rawPath, true)
	if err != nil {
		hiddenNotFound(c)
		return
	}
	if _, err := utils.ListDirectory(cleanPath); err != nil {
		hiddenNotFound(c)
		return
	}
	_ = logAction(state, c, "directory.list", cleanPath)
	bundle, err := loadWebAssets()
	if err != nil {
		setPrivateResponse(c)
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
		return
	}
	serveShell(c, bundle)
}

func fileForRead(raw string) (string, string, os.FileInfo, error) {
	absolute, rel, info, err := utils.ResolveExisting(pathFromParam(raw), false)
	if err != nil {
		return "", "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", nil, utils.ErrUnsupportedType
	}
	return absolute, rel, info, nil
}

func writeUploadedFile(destination string, uploaded io.Reader) error {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return utils.ErrDestinationExists
		}
		return errors.New("io")
	}
	_, copyErr := io.Copy(file, uploaded)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return errors.New("io")
	}
	return nil
}

func boundedMultipartBody(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadBodyBytes)
}

func boundedChunkBody(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChunkBodyBytes)
}

func boundedFormBody(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		defer cleanupMultipartFiles(c)
		contentType := c.GetHeader("Content-Type")
		var err error
		switch {
		case strings.HasPrefix(contentType, "multipart/form-data"):
			err = c.Request.ParseMultipartForm(1 << 20)
		case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
			err = c.Request.ParseForm()
		}
		if err != nil {
			multipartError(c, err, utils.ErrInvalidPath)
			c.Abort()
			return
		}
		c.Next()
	}
}

func multipartError(c *gin.Context, err error, fallback error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		jsonError(c, http.StatusRequestEntityTooLarge, utils.ErrBatchLimitExceeded)
		return
	}
	jsonError(c, http.StatusBadRequest, fallback)
}

func cleanupMultipartFiles(c *gin.Context) {
	if form := c.Request.MultipartForm; form != nil {
		_ = form.RemoveAll()
	}
}

func uploadDestination(parent, name string) (string, string, error) {
	if err := utils.ValidateLeafName(name); err != nil {
		return "", "", err
	}
	dir, rel, _, err := utils.ResolveDirectory(parent, true)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, name), filepath.ToSlash(filepath.Join(rel, name)), nil
}

type batchRequest struct {
	ListingToken string              `json:"listing_token"`
	Entries      []utils.ItemRequest `json:"entries"`
	Destination  string              `json:"destination"`
}

func decodeBatch(c *gin.Context, manager *auth.Manager) (utils.Selection, batchRequest, error) {
	var request batchRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if err := json.NewDecoder(c.Request.Body).Decode(&request); err != nil {
		return utils.Selection{}, request, utils.ErrInvalidPath
	}
	session := authInfo(c)
	if session.SessionID == "" {
		return utils.Selection{}, request, utils.ErrInvalidPath
	}
	directory, allowed, ok := manager.ReadListing(session.SessionID, request.ListingToken)
	if !ok {
		return utils.Selection{}, request, utils.ErrSourceChanged
	}
	selection, err := utils.ValidateSelection(directory, allowed, request.Entries)
	return selection, request, err
}

func decodeTrashConfirmation(c *gin.Context) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var request struct {
		Confirmation string `json:"confirmation"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return utils.ErrInvalidPath
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return utils.ErrInvalidPath
	}
	if request.Confirmation != "DELETE" {
		return utils.ErrInvalidConfirmation
	}
	return nil
}

func trashAuditPath(bin *RecycleBin, id string) string {
	record, err := bin.loadRecord(id)
	if err != nil {
		return ""
	}
	return record.metadata.OriginalPath
}

func extractArchiveHandler(state *RuntimeState) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawPath := c.PostForm("path")
		cleanPath, pathErr := utils.CleanRelative(rawPath, false)
		auditPath := cleanPath
		if pathErr != nil {
			auditPath = ""
		}
		if !requireAudit(c, state, "archive.extract", auditPath, 1) {
			return
		}
		if pathErr != nil {
			jsonError(c, operationStatus(pathErr), pathErr)
			return
		}
		rel, err := utils.ExtractArchiveContext(c.Request.Context(), cleanPath)
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		if !finishMutation(c, state, "archive.extract", rel, 1) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
	}
}

func newRouter(manager *auth.Manager, state *RuntimeState) *gin.Engine {
	if state == nil {
		panic("runtime state is required")
	}
	trash, err := newRecycleBin(state)
	if err != nil {
		panic(err)
	}
	bundle, err := loadWebAssets()
	if err != nil {
		panic(err)
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(gin.Recovery())
	router.GET("/healthz", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.GET("/readyz", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if !state.Ready() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "code": "shutting_down"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.GET("/login", func(c *gin.Context) {
		if _, ok := manager.SessionFromRequest(c.Request); ok {
			c.Redirect(http.StatusFound, appPath("/"))
			return
		}
		serveShell(c, bundle)
	})
	router.GET("/assets/*asset", func(c *gin.Context) {
		serveWebAsset(c, bundle)
	})
	router.POST("/login", boundedFormBody(64<<10), func(c *gin.Context) {
		setPrivateResponse(c)
		username := c.PostForm("username")
		password := c.PostForm("password")
		remember := c.PostForm("remember") == "true" || c.PostForm("remember") == "on"
		info, signedID, expiry, err := manager.LoginWithRemember(c.ClientIP(), username, password, remember)
		if err != nil {
			outcome := "failure"
			if errors.Is(err, auth.ErrRateLimited) {
				outcome = "rate_limited"
			}
			_ = state.Record(AuditEvent{Event: "auth.login", Outcome: outcome, AuthMethod: "session", ClientIP: c.ClientIP()})
			if !errors.Is(err, auth.ErrInvalidCredentials) && !errors.Is(err, auth.ErrRateLimited) {
				if wantsJSON(c) {
					c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				} else {
					c.Status(http.StatusInternalServerError)
				}
				return
			}
			status := http.StatusUnauthorized
			code := "invalid_credentials"
			if errors.Is(err, auth.ErrRateLimited) {
				status = http.StatusTooManyRequests
				code = "rate_limited"
			}
			if wantsJSON(c) {
				c.JSON(status, gin.H{"ok": false, "code": code})
			} else {
				c.Status(status)
			}
			return
		}
		_ = state.Record(AuditEvent{Event: "auth.login", Outcome: "success", Principal: info.Username, AuthMethod: "session", ClientIP: c.ClientIP()})
		http.SetCookie(c.Writer, manager.LoginCookie(signedID, expiry, remember))
		http.SetCookie(c.Writer, manager.ExpiredLegacyCookie())
		c.Redirect(http.StatusFound, safeNextPath(c.PostForm("next")))
	})

	router.POST("/api/session/login", sessionLoginHandler(manager, state))

	apiSession := router.Group("/api/session")
	apiSession.Use(authRequired(manager))
	apiSession.GET("", sessionGetHandler())
	apiSession.POST("/logout", csrfRequired(manager), sessionLogoutHandler(manager, state))

	protected := router.Group("/")
	protected.Use(authRequired(manager))
	registerFavoriteRoutes(protected, manager, state)
	registerSizeRoutes(protected, manager, state)
	registerJobRoutes(protected, manager, state)
	protected.GET("/", func(c *gin.Context) { renderDirectory(c, state, "") })
	protected.GET("/edit/*path", func(c *gin.Context) {
		file, err := utils.ReadTextFile(pathFromParam(c.Param("path")), maxEditorBytes)
		if err != nil {
			if utils.ErrorCode(err) == "batch_limit_exceeded" {
				setPrivateResponse(c)
				jsonError(c, http.StatusRequestEntityTooLarge, err)
				return
			}
			hiddenNotFound(c)
			return
		}
		_ = logAction(state, c, "file.edit", file.Relative)
		serveShell(c, bundle)
	})
	protected.GET("/d/*path", func(c *gin.Context) {
		raw := pathFromParam(c.Param("path"))
		if raw == "" {
			c.Redirect(http.StatusMovedPermanently, appPath("/"))
			return
		}
		renderDirectory(c, state, raw)
	})
	protected.GET("/api/listing", listingHandler(manager, state))
	protected.GET("/api/editor/content", editorContentHandler(state))
	if !reader {
		protected.PUT("/api/editor/content", csrfRequired(manager), mutationAuditMiddleware(state), editorSaveHandler(state))
	}
	protected.GET("/view/*path", func(c *gin.Context) {
		absolute, rel, _, err := fileForRead(c.Param("path"))
		if err != nil {
			hiddenNotFound(c)
			return
		}
		setPrivateResponse(c)
		c.Header("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'; sandbox")
		c.Header("X-Content-Type-Options", "nosniff")
		if !isInlinePreviewable(rel) {
			c.Header("Content-Type", "application/octet-stream")
			_ = logAction(state, c, "file.view", rel)
			c.FileAttachment(absolute, filepath.Base(rel))
			return
		}
		setSafeFilePreviewHeaders(c)
		_ = logAction(state, c, "file.view", rel)
		c.File(absolute)
	})
	protected.GET("/batch-download/:ticket", func(c *gin.Context) {
		session := authInfo(c)
		if session.SessionID == "" {
			hiddenNotFound(c)
			return
		}
		items, ok := manager.ConsumeArchiveTicket(session.SessionID, c.Param("ticket"))
		if !ok {
			hiddenNotFound(c)
			return
		}
		selection, err := utils.SelectionFromArchiveItems(items)
		if err != nil {
			if code := utils.ErrorCode(err); code == "not_found" || code == "unsupported_file_type" || code == "invalid_path" {
				hiddenNotFound(c)
				return
			}
			c.Status(operationStatus(err))
			return
		}
		archive, cleanup, err := utils.PrepareSelectionZip(selection, state.TempDir)
		if err != nil {
			c.Status(operationStatus(err))
			return
		}
		defer cleanup()
		setPrivateResponse(c)
		c.Header("Content-Type", "application/zip")
		c.Header("Content-Disposition", "attachment; filename=fileharbor-selection.zip")
		c.Header("X-Content-Type-Options", "nosniff")
		if _, err := io.Copy(c.Writer, archive); err != nil {
			_ = state.Record(AuditEvent{Event: "batch.download", Outcome: "failure", Principal: session.Username, AuthMethod: "session", ClientIP: c.ClientIP(), Affected: len(selection.Items), Code: "io_error"})
			return
		}
		_ = state.Record(AuditEvent{Event: "batch.download", Outcome: "success", Principal: session.Username, AuthMethod: "session", ClientIP: c.ClientIP(), Affected: len(selection.Items)})
	})
	protected.GET("/download/*path", func(c *gin.Context) {
		absolute, rel, _, err := fileForRead(c.Param("path"))
		if err != nil {
			hiddenNotFound(c)
			return
		}
		setPrivateResponse(c)
		c.Header("X-Content-Type-Options", "nosniff")
		_ = logAction(state, c, "file.download", rel)
		c.FileAttachment(absolute, filepath.Base(rel))
	})
	protected.GET("/api/directories", func(c *gin.Context) {
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
		directories := info.Dirs
		if directories == nil {
			directories = []conf.Dir{}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "path": cleanPath, "dirs": directories})
	})
	protected.GET("/api/search", searchHandler)
	protected.GET("/api/archive/preview", archivePreviewHandler)
	protected.GET("/api/properties", func(c *gin.Context) {
		if authInfo(c).Bearer {
			setPrivateResponse(c)
			c.JSON(http.StatusForbidden, gin.H{"ok": false, "code": "browser_session_required"})
			return
		}
		setPrivateResponse(c)
		release, ok := beginReadScan(c)
		if !ok {
			return
		}
		defer release()
		var properties utils.Properties
		var err error
		if c.Query("basic") == "true" {
			properties, err = utils.GetBasicProperties(c.Query("path"))
		} else {
			properties, err = utils.GetPropertiesContext(c.Request.Context(), c.Query("path"))
		}
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "properties": properties})
	})
	protected.GET("/api/trash", func(c *gin.Context) {
		if authInfo(c).Bearer {
			setPrivateResponse(c)
			c.JSON(http.StatusForbidden, gin.H{"ok": false, "code": "browser_session_required"})
			return
		}
		setPrivateResponse(c)
		page, err := trash.List(c.Query("cursor"))
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "entries": page.Entries, "next_cursor": page.NextCursor})
	})
	if uploads == nil || uploads.state != state {
		store, err := NewUploadStore(state, defaultUploadConfig())
		if err != nil {
			panic(err)
		}
		uploads = store
	}
	registerUploadRoutes(protected, manager, state, uploads)

	if !reader {
		trashMutations := protected.Group("/")
		trashMutations.Use(csrfRequired(manager), mutationAuditMiddleware(state))
		trashMutations.POST("/api/trash/:id/restore", func(c *gin.Context) {
			id := c.Param("id")
			auditPath := trashAuditPath(trash, id)
			if !requireAudit(c, state, "trash.restore", auditPath, 1) {
				return
			}
			path, err := trash.Restore(id)
			if err != nil {
				if utils.ErrorCode(err) == "execution_partial" {
					if auditErr := recordAction(state, c, "trash.restore", "partial_failure", path, utils.ErrorCode(err), 1); auditErr != nil {
						setPrivateResponse(c)
						c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "code": "audit_unavailable"})
						return
					}
				}
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "trash.restore", path, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": path})
		})
		trashMutations.POST("/api/trash/:id/purge", func(c *gin.Context) {
			id := c.Param("id")
			auditPath := trashAuditPath(trash, id)
			if !requireAudit(c, state, "trash.purge", auditPath, 1) {
				return
			}
			if err := decodeTrashConfirmation(c); err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if err := trash.Purge(id); err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "trash.purge", auditPath, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		trashMutations.POST("/api/trash/empty", func(c *gin.Context) {
			if !requireAudit(c, state, "trash.empty", "", 0) {
				return
			}
			if err := decodeTrashConfirmation(c); err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			affected, results, err := trash.Empty()
			if err != nil {
				if err := recordAction(state, c, "trash.empty", "partial_failure", "", utils.ErrorCode(err), affected); err != nil {
					setPrivateResponse(c)
					c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "code": "audit_unavailable"})
					return
				}
				c.JSON(operationStatus(err), gin.H{"ok": false, "code": utils.ErrorCode(err), "items": results})
				return
			}
			if !finishMutation(c, state, "trash.empty", "", affected) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "affected": affected, "items": results})
		})
	}

	if !reader || uploader {
		uploadGroup := protected.Group("/")
		uploadGroup.Use(csrfRequired(manager), mutationAuditMiddleware(state))
		uploadGroup.POST("/do/upload/*path", func(c *gin.Context) {
			boundedMultipartBody(c)
			defer cleanupMultipartFiles(c)
			upload, err := c.FormFile("file")
			if err != nil {
				multipartError(c, err, utils.ErrInvalidPath)
				return
			}
			destination, rel, err := uploadDestination(pathFromParam(c.Param("path")), upload.Filename)
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !requireAudit(c, state, "file.upload", rel, 1) {
				return
			}
			input, err := upload.Open()
			if err != nil {
				jsonError(c, http.StatusInternalServerError, errors.New("io"))
				return
			}
			err = writeUploadedFile(destination, input)
			closeErr := input.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "file.upload", rel, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
		uploadGroup.POST("/do/chunk/check", func(c *gin.Context) {
			boundedChunkBody(c)
			defer cleanupMultipartFiles(c)
			if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
				multipartError(c, err, utils.ErrInvalidPath)
				return
			}
			fileID := c.PostForm("fileId")
			dir := chunkDir(state, fileID)
			total, err := strconv.Atoi(c.PostForm("totalChunks"))
			if dir == "" || err != nil || total <= 0 || total > maxChunkCount {
				c.JSON(http.StatusBadRequest, gin.H{"ok": false, "code": "invalid_upload"})
				return
			}
			state.chunkMu.Lock()
			defer state.chunkMu.Unlock()
			if _, err := chunkDirSize(dir); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				return
			}
			uploaded := make([]int, 0)
			for i := 0; i < total; i++ {
				if utils.Exist(filepath.Join(dir, strconv.Itoa(i))) {
					uploaded = append(uploaded, i)
				}
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "uploaded": uploaded})
		})
		uploadGroup.POST("/do/chunk/upload", func(c *gin.Context) {
			boundedChunkBody(c)
			defer cleanupMultipartFiles(c)
			if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
				multipartError(c, err, utils.ErrInvalidPath)
				return
			}
			fileID := c.PostForm("fileId")
			dir := chunkDir(state, fileID)
			index, err := strconv.Atoi(c.PostForm("chunkIndex"))
			total, totalErr := strconv.Atoi(c.PostForm("totalChunks"))
			if dir == "" || err != nil || totalErr != nil || index < 0 || total <= 0 || total > maxChunkCount || index >= total {
				c.JSON(http.StatusBadRequest, gin.H{"ok": false, "code": "invalid_upload"})
				return
			}
			upload, err := c.FormFile("file")
			if err != nil {
				multipartError(c, err, utils.ErrInvalidPath)
				return
			}
			state.chunkMu.Lock()
			defer state.chunkMu.Unlock()
			chunkPath := fileID + "/" + strconv.Itoa(index)
			if !requireAudit(c, state, "file.chunk_upload", chunkPath, 1) {
				return
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				return
			}
			size, err := chunkDirSize(dir)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				return
			}
			chunkFile := filepath.Join(dir, strconv.Itoa(index))
			if _, err := os.Lstat(chunkFile); err == nil {
				c.JSON(http.StatusConflict, gin.H{"ok": false, "code": "chunk_exists"})
				return
			} else if !errors.Is(err, fs.ErrNotExist) {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				return
			}
			if size > maxChunkStateBytes || upload.Size > maxChunkStateBytes-size {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"ok": false, "code": "upload_too_large"})
				return
			}
			storageSize, err := chunkStorageSize(state.ChunksDir)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				return
			}
			if storageSize > maxChunkStorageBytes || upload.Size > maxChunkStorageBytes-storageSize {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"ok": false, "code": "upload_too_large"})
				return
			}
			input, err := upload.Open()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				return
			}
			err = writeUploadedFile(chunkFile, input)
			closeErr := input.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "file.chunk_upload", chunkPath, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		uploadGroup.POST("/do/chunk/merge", func(c *gin.Context) {
			boundedChunkBody(c)
			defer cleanupMultipartFiles(c)
			if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
				multipartError(c, err, utils.ErrInvalidPath)
				return
			}
			fileID := c.PostForm("fileId")
			dir := chunkDir(state, fileID)
			total, err := strconv.Atoi(c.PostForm("totalChunks"))
			if dir == "" || err != nil || total <= 0 || total > maxChunkCount {
				c.JSON(http.StatusBadRequest, gin.H{"ok": false, "code": "invalid_upload"})
				return
			}
			destination, rel, err := uploadDestination(c.PostForm("path"), c.PostForm("fileName"))
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			state.chunkMu.Lock()
			defer state.chunkMu.Unlock()
			size, err := chunkDirSize(dir)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				return
			}
			if size > maxChunkStateBytes {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"ok": false, "code": "upload_too_large"})
				return
			}
			if !requireAudit(c, state, "file.upload", rel, 1) {
				return
			}
			output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				if errors.Is(err, fs.ErrExist) {
					jsonError(c, http.StatusConflict, utils.ErrDestinationExists)
				} else {
					jsonError(c, http.StatusInternalServerError, errors.New("io"))
				}
				return
			}
			success := true
			for i := 0; i < total; i++ {
				input, openErr := os.Open(filepath.Join(dir, strconv.Itoa(i)))
				if openErr != nil {
					success = false
					break
				}
				_, copyErr := io.Copy(output, input)
				inputCloseErr := input.Close()
				if copyErr != nil || inputCloseErr != nil {
					success = false
					break
				}
			}
			closeErr := output.Close()
			if !success || closeErr != nil {
				_ = os.Remove(destination)
				jsonError(c, http.StatusInternalServerError, errors.New("io"))
				return
			}
			if err := os.RemoveAll(dir); err != nil {
				if !finishMutation(c, state, "file.upload", rel, 1) {
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "code": "io_error"})
				return
			}
			if !finishMutation(c, state, "file.upload", rel, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
		uploadGroup.POST("/api/upload", func(c *gin.Context) {
			boundedMultipartBody(c)
			defer cleanupMultipartFiles(c)
			upload, err := c.FormFile("file")
			if err != nil {
				multipartError(c, err, utils.ErrInvalidPath)
				return
			}
			destination, rel, err := uploadDestination(c.PostForm("path"), upload.Filename)
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !requireAudit(c, state, "file.upload", rel, 1) {
				return
			}
			input, err := upload.Open()
			if err != nil {
				jsonError(c, http.StatusInternalServerError, errors.New("io"))
				return
			}
			err = writeUploadedFile(destination, input)
			closeErr := input.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "file.upload", rel, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
	}

	if !reader {
		mutations := protected.Group("/")
		mutations.Use(csrfRequired(manager), mutationAuditMiddleware(state))
		mutations.Use(boundedFormBody(maxEditorBytes + 64<<10))
		mutations.POST("/do/newdir", func(c *gin.Context) {
			if !requireAudit(c, state, "directory.create", "", 1) {
				return
			}
			rel, err := utils.MakeDirectory(c.PostForm("path"), c.PostForm("dirname"))
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "directory.create", rel+"/", 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
		mutations.POST("/do/newfile", func(c *gin.Context) {
			if !requireAudit(c, state, "file.create", "", 1) {
				return
			}
			rel, err := utils.MakeFile(c.PostForm("path"), c.PostForm("filename"))
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "file.create", rel, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
		mutations.POST("/do/rename", func(c *gin.Context) {
			if !requireAudit(c, state, "file.rename", "", 1) {
				return
			}
			rel, err := utils.RenameItem(c.PostForm("path"), c.PostForm("name"))
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "file.rename", rel, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
		mutations.POST("/do/move", func(c *gin.Context) {
			if !requireAudit(c, state, "file.move", "", 1) {
				return
			}
			rel, err := utils.MoveItem(c.PostForm("path"), c.PostForm("destination"), c.PostForm("name"))
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "file.move", rel, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
		mutations.POST("/do/copy", func(c *gin.Context) {
			if !requireAudit(c, state, "file.copy", "", 1) {
				return
			}
			rel, err := utils.CopyItem(c.PostForm("path"), c.PostForm("destination"), c.PostForm("name"))
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "file.copy", rel, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
		mutations.POST("/do/rm", func(c *gin.Context) {
			if !requireAudit(c, state, "file.trash", "", 1) {
				return
			}
			entry, err := trash.Move(c.PostForm("path"))
			if err != nil {
				if utils.ErrorCode(err) == "execution_partial" {
					if auditErr := recordAction(state, c, "file.trash", "partial_failure", entry.OriginalPath, utils.ErrorCode(err), 1); auditErr != nil {
						setPrivateResponse(c)
						c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "code": "audit_unavailable"})
						return
					}
				}
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "file.trash", entry.OriginalPath, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "entry": entry})
		})
		mutations.POST("/do/zip", func(c *gin.Context) {
			if !requireAudit(c, state, "archive.create", "", 1) {
				return
			}
			rel, err := utils.CreateDirectoryZip(c.PostForm("path"))
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "archive.create", rel, 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "path": rel})
		})
		extractHandler := extractArchiveHandler(state)
		mutations.POST("/do/extract", extractHandler)
		mutations.POST("/do/unzip", extractHandler)
		mutations.POST("/do/md5", func(c *gin.Context) {
			if !requireAudit(c, state, "file.checksum", "", 1) {
				return
			}
			absolute, rel, _, err := fileForRead(c.PostForm("path"))
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			input, err := os.Open(absolute)
			if err != nil {
				jsonError(c, http.StatusInternalServerError, errors.New("io"))
				return
			}
			hash := md5.New()
			_, copyErr := io.Copy(hash, input)
			inputCloseErr := input.Close()
			if copyErr != nil || inputCloseErr != nil {
				jsonError(c, http.StatusInternalServerError, errors.New("io"))
				return
			}
			output := absolute + ".md5"
			value := fmt.Sprintf("%x", hash.Sum(nil))
			checksum, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if errors.Is(err, os.ErrExist) {
				jsonError(c, http.StatusConflict, utils.ErrDestinationExists)
				return
			}
			if err != nil {
				jsonError(c, http.StatusInternalServerError, errors.New("io"))
				return
			}
			_, writeErr := io.WriteString(checksum, value+"  "+filepath.Base(rel)+"\n")
			closeErr := checksum.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(output)
				jsonError(c, http.StatusInternalServerError, errors.New("io"))
				return
			}
			if !finishMutation(c, state, "file.checksum", rel+".md5", 1) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "hash": value})
		})
		mutations.POST("/do/batch/move", func(c *gin.Context) {
			selection, request, err := decodeBatch(c, manager)
			if err == nil {
				_, _, _, err = utils.ResolveDirectory(request.Destination, true)
			}
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !requireAudit(c, state, "batch.move", "", len(selection.Items)) {
				return
			}
			paths, err := utils.BatchMove(selection, request.Destination)
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "batch.move", "", len(paths)) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "affected": len(paths), "paths": paths})
		})
		mutations.POST("/do/batch/copy", func(c *gin.Context) {
			selection, request, err := decodeBatch(c, manager)
			if err == nil {
				_, _, _, err = utils.ResolveDirectory(request.Destination, true)
			}
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !requireAudit(c, state, "batch.copy", "", len(selection.Items)) {
				return
			}
			paths, err := utils.BatchCopy(selection, request.Destination)
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !finishMutation(c, state, "batch.copy", "", len(paths)) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "affected": len(paths), "paths": paths})
		})
		mutations.POST("/do/batch/delete", func(c *gin.Context) {
			selection, _, err := decodeBatch(c, manager)
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if !requireAudit(c, state, "batch.trash", "", len(selection.Items)) {
				return
			}
			results := trash.BatchMove(selection)
			allTrashed := true
			partial := false
			trashed := 0
			for _, result := range results {
				if result.Code == "trashed" {
					trashed++
					continue
				}
				allTrashed = false
				if result.Code == "execution_partial" {
					partial = true
					trashed++
				}
			}
			if !allTrashed {
				code := "trash_partial"
				if partial {
					code = "execution_partial"
				}
				if err := recordAction(state, c, "batch.trash", "partial_failure", "", code, trashed); err != nil {
					setPrivateResponse(c)
					c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "code": "audit_unavailable"})
					return
				}
				responseStatus := http.StatusInternalServerError
				if partial {
					responseStatus = operationStatus(utils.ErrExecutionPartial)
				}
				c.JSON(responseStatus, gin.H{"ok": false, "code": code, "items": results})
				return
			}
			if !finishMutation(c, state, "batch.trash", "", trashed) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "affected": trashed, "items": results})
		})
	}

	batchArchive := protected.Group("/")
	batchArchive.Use(csrfRequired(manager))
	batchArchive.POST("/do/batch/download-zip", func(c *gin.Context) {
		selection, _, err := decodeBatch(c, manager)
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		if err := utils.PreflightSelectionZip(selection); err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}
		items := make([]auth.ArchiveItem, 0, len(selection.Items))
		for _, item := range selection.Items {
			items = append(items, auth.ArchiveItem{Path: item.Relative, Version: utils.EntryVersion(item.Info)})
		}
		ticket, err := manager.IssueArchiveTicket(authInfo(c).SessionID, items)
		if err != nil {
			jsonError(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "download_url": appPath("/batch-download/" + ticket)})
	})
	logout := protected.Group("/")
	logout.Use(csrfRequired(manager))
	logout.POST("/logout", func(c *gin.Context) {
		info := authInfo(c)
		manager.Logout(info)
		_ = state.Record(AuditEvent{Event: "auth.logout", Outcome: "success", Principal: info.Username, AuthMethod: "session", ClientIP: c.ClientIP()})
		http.SetCookie(c.Writer, manager.ExpiredCookie())
		http.SetCookie(c.Writer, manager.ExpiredLegacyCookie())
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	router.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || strings.HasPrefix(c.Request.URL.Path, "/do/") {
			setPrivateResponse(c)
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "code": "not_found"})
			return
		}
		hiddenNotFound(c)
	})

	return router
}

func web(ctx context.Context, manager *auth.Manager, state *RuntimeState) error {
	address := net.JoinHostPort(conf.Host, conf.FileHarborPort)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()

	fmt.Println(strings.Repeat("-", 44))
	fmt.Println("Directory : " + conf.FileHarbor)
	fmt.Println("Listen    : http://" + address + appPath("/"))
	if !isLoopbackHost(conf.Host) {
		for _, ip := range localIPs() {
			fmt.Println("Access    : http://" + net.JoinHostPort(ip, conf.FileHarborPort) + appPath("/"))
		}
	}
	fmt.Println(strings.Repeat("-", 44))

	gate := newRequestGate()
	server := &http.Server{
		Handler:           gate.Wrap(withBasePath(newRouter(manager, state))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	if state.Jobs == nil {
		return errors.New("file task storage unavailable")
	}
	state.SetReady(true)
	if uploads != nil && uploads.state == state {
		uploads.StartReaper(ctx.Done())
	}
	if err := state.Record(AuditEvent{Event: "server.start", Outcome: "success"}); err != nil {
		state.SetReady(false)
		return err
	}

	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()

	select {
	case err := <-serveErrors:
		state.SetReady(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		state.SetReady(false)
		gate.StopAdmission()
		if state.Jobs != nil {
			state.Jobs.cancel()
		}
		_ = state.Record(AuditEvent{Event: "server.shutdown_requested", Outcome: "success"})
		deadline := time.Now().Add(shutdownTimeout())
		shutdownCtx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		if !gate.WaitForDrain(deadline) {
			// Admitted handlers can still be using private runtime files. Exit without
			// releasing the lock or deleting state under their feet; process exit
			// closes descriptors after this function returns.
			return errForcedShutdown
		}
		_ = state.Record(AuditEvent{Event: "server.shutdown_complete", Outcome: "success"})
		return nil
	}
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseConfig() (*auth.Manager, *RuntimeState, error) {
	startup, err := parseStartupConfig(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		return nil, nil, err
	}
	absolute, err := filepath.Abs(startup.Path)
	if err != nil {
		return nil, nil, err
	}
	conf.FileHarbor = filepath.Clean(absolute)
	conf.FileHarborPort = startup.Port
	conf.Host = startup.Host
	reader = startup.ReadOnly
	uploader = startup.UploadReadOnly
	basePath = startup.BasePath
	root, err := utils.Root()
	if err != nil {
		return nil, nil, fmt.Errorf("invalid -path: %w", err)
	}
	manager, err := auth.NewManager(startup.Auth)
	if err != nil {
		return nil, nil, err
	}
	state, err := OpenRuntimeState(startup.StateDir, root)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid runtime state: %w", err)
	}
	store, err := NewUploadStore(state, startup.Upload)
	if err != nil {
		_ = state.Close()
		return nil, nil, fmt.Errorf("invalid upload state: %w", err)
	}
	uploads = store
	return manager, state, nil
}

func localIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				ips = append(ips, ip.String())
			}
		}
	}
	return ips
}

func run() int {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := runHashPasswordCommand(os.Args[2:], terminalPasswordPrompter{file: os.Stdin}, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}

	manager, state, err := parseConfig()
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	closeState := true
	defer func() {
		if !closeState {
			return
		}
		if err := state.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	go func() {
		<-ctx.Done()
		stop()
	}()
	defer stop()
	if err := web(ctx, manager, state); err != nil {
		if errors.Is(err, errForcedShutdown) {
			closeState = false
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run())
}
