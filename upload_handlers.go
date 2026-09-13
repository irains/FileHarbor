package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/auth"
	"github.com/irains/fileharbor/utils"
)

func registerUploadRoutes(protected *gin.RouterGroup, manager *auth.Manager, state *RuntimeState, store *UploadStore) {
	if protected == nil || manager == nil || state == nil || store == nil || reader && !uploader {
		return
	}
	routes := protected.Group("/api/uploads")
	routes.GET("/:id", func(c *gin.Context) {
		setPrivateResponse(c)
		status, err := store.Status(authInfo(c), c.Param("id"), c.GetHeader(uploadTokenHeader))
		if err != nil {
			writeUploadError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "upload": status})
	})

	mutations := routes.Group("")
	mutations.Use(mutationAuditMiddleware(state), csrfRequired(manager))
	mutations.POST("/directories", uploadDirectoriesHandler(state))
	mutations.POST("", func(c *gin.Context) {
		setPrivateResponse(c)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var request UploadCreateRequest
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			if errors.As(err, new(*http.MaxBytesError)) {
				writeUploadError(c, errUploadSizeMismatch)
				return
			}
			writeUploadError(c, errUploadInvalid)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if errors.As(err, new(*http.MaxBytesError)) {
				writeUploadError(c, errUploadSizeMismatch)
				return
			}
			writeUploadError(c, errUploadInvalid)
			return
		}
		auditPath := uploadAuditPath(request.Path, request.Name)
		if !requireAudit(c, state, "upload.create", auditPath, 1) {
			return
		}
		status, created, err := store.Create(authInfo(c), c.GetHeader(uploadIDHeader), c.GetHeader(uploadTokenHeader), request)
		if err != nil {
			writeUploadError(c, err)
			return
		}
		if !finishMutation(c, state, "upload.create", auditPath, 1) {
			return
		}
		code := http.StatusOK
		if created {
			code = http.StatusCreated
		}
		c.JSON(code, gin.H{"ok": true, "upload": status})
	})
	mutations.PUT("/:id/parts/:index", func(c *gin.Context) {
		setPrivateResponse(c)
		index, err := parsePartIndex(c.Param("index"))
		if err != nil {
			writeUploadError(c, err)
			return
		}
		current, err := store.Status(authInfo(c), c.Param("id"), c.GetHeader(uploadTokenHeader))
		if err != nil {
			writeUploadError(c, err)
			return
		}
		if index >= current.PartCount {
			writeUploadError(c, errUploadInvalidPart)
			return
		}
		limit := expectedStatusPartSize(current, index)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit+1)
		status, repeated, err := store.WritePart(authInfo(c), c.Param("id"), c.GetHeader(uploadTokenHeader), index, c.GetHeader(uploadPartDigestHeader), c.Request.Body)
		if err != nil {
			if errors.As(err, new(*http.MaxBytesError)) {
				writeUploadError(c, errUploadSizeMismatch)
				return
			}
			writeUploadError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "repeated": repeated, "upload": status})
	})
	mutations.POST("/:id/complete", func(c *gin.Context) {
		setPrivateResponse(c)
		id := c.Param("id")
		current, err := store.Status(authInfo(c), id, c.GetHeader(uploadTokenHeader))
		if err != nil {
			writeUploadError(c, err)
			return
		}
		auditPath := uploadAuditPath(current.Path, current.Name)
		if !requireAudit(c, state, "upload.complete", auditPath, 1) {
			return
		}
		status, repeated, err := store.Complete(authInfo(c), id, c.GetHeader(uploadTokenHeader))
		if err != nil {
			writeUploadError(c, err)
			return
		}
		if !finishMutation(c, state, "upload.complete", status.FinalPath, 1) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "repeated": repeated, "upload": status})
	})
	mutations.DELETE("/:id", func(c *gin.Context) {
		setPrivateResponse(c)
		id := c.Param("id")
		current, err := store.Status(authInfo(c), id, c.GetHeader(uploadTokenHeader))
		if err != nil {
			writeUploadError(c, err)
			return
		}
		auditPath := uploadAuditPath(current.Path, current.Name)
		if !requireAudit(c, state, "upload.cancel", auditPath, 1) {
			return
		}
		if err := store.Cancel(authInfo(c), id, c.GetHeader(uploadTokenHeader)); err != nil {
			writeUploadError(c, err)
			return
		}
		if !finishMutation(c, state, "upload.cancel", auditPath, 1) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}

func expectedStatusPartSize(status *UploadStatus, index int) int64 {
	if status == nil || index < 0 || index >= status.PartCount {
		return 0
	}
	if status.Size == 0 {
		return 0
	}
	if index < status.PartCount-1 {
		return status.ChunkBytes
	}
	remaining := status.Size - int64(index)*status.ChunkBytes
	if remaining == 0 {
		return status.ChunkBytes
	}
	return remaining
}

func uploadAuditPath(path, name string) string {
	cleaned, err := utils.CleanRelative(path, true)
	if err != nil || utils.ValidateLeafName(name) != nil {
		return ""
	}
	return filepath.ToSlash(filepath.Join(cleaned, name))
}

func writeUploadError(c *gin.Context, err error) {
	setPrivateResponse(c)
	code := uploadErrorCode(err)
	status := http.StatusInternalServerError
	switch code {
	case "upload_not_found":
		status = http.StatusNotFound
	case "upload_expired", "upload_cancelled":
		status = http.StatusGone
	case "upload_too_large", "size_mismatch":
		status = http.StatusRequestEntityTooLarge
	case "upload_busy":
		status = http.StatusTooManyRequests
	case "upload_conflict", "part_conflict", "upload_incomplete", "destination_exists":
		status = http.StatusConflict
	case "invalid_upload", "invalid_part", "invalid_digest":
		status = http.StatusBadRequest
	case "insufficient_storage":
		status = http.StatusInsufficientStorage
	}
	c.JSON(status, gin.H{"ok": false, "code": code})
}
