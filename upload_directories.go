package main

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/utils"
)

func prepareUploadDirectories(ctx context.Context, destination string, names []string) ([]string, error) {
	created := []string{}
	if len(names) > 10000 {
		return created, utils.ErrInvalidPath
	}
	err := utils.WithOperationContext(ctx, func() error {
		_, root, _, err := utils.ResolveDirectory(destination, true)
		if err != nil {
			return err
		}
		unique := map[string]string{}
		for _, name := range names {
			clean, err := utils.CleanRelative(name, false)
			if err != nil || clean != name || strings.Count(name, "/") >= 64 {
				return utils.ErrInvalidPath
			}
			parts := strings.Split(name, "/")
			for i, part := range parts {
				if utils.ValidateLeafName(part) != nil {
					return utils.ErrInvalidPath
				}
				relative := strings.Join(parts[:i+1], "/")
				key := strings.ToLower(relative)
				if previous, exists := unique[key]; exists && previous != relative {
					return utils.ErrDestinationExists
				}
				unique[key] = relative
				if len(unique) > 10000 {
					return utils.ErrInvalidPath
				}
			}
		}
		ordered := make([]string, 0, len(unique))
		for _, relative := range unique {
			ordered = append(ordered, relative)
		}
		sort.Strings(ordered)
		// Validate all existing components before creating any directory.
		for _, relative := range ordered {
			_, _, info, err := utils.ResolveExisting(path.Join(root, relative), false)
			if err == nil && !info.IsDir() {
				return utils.ErrDestinationExists
			}
			if err != nil && utils.ErrorCode(err) != "not_found" {
				return err
			}
		}
		for _, relative := range ordered {
			if err := ctx.Err(); err != nil {
				return err
			}
			target := path.Join(root, relative)
			if _, _, _, err := utils.ResolveDirectory(target, false); err == nil {
				continue
			} else if utils.ErrorCode(err) != "not_found" {
				return err
			}
			parent := path.Dir(target)
			if parent == "." {
				parent = ""
			}
			absolute, _, expected, err := utils.ResolveDirectory(parent, true)
			if err != nil {
				return err
			}
			if current, err := os.Stat(absolute); err != nil || !os.SameFile(expected, current) {
				return utils.ErrSourceChanged
			}
			err = os.Mkdir(filepath.Join(absolute, path.Base(target)), 0755)
			if err != nil {
				if errors.Is(err, os.ErrExist) {
					return utils.ErrDestinationExists
				}
				return err
			}
			created = append(created, target)
		}
		return nil
	})
	return created, err
}

func uploadDirectoriesHandler(state *RuntimeState) gin.HandlerFunc {
	return func(c *gin.Context) {
		setPrivateResponse(c)
		if authInfo(c).Bearer {
			c.JSON(403, gin.H{"ok": false, "code": "browser_session_required"})
			return
		}
		var request struct {
			Path        string   `json:"path"`
			Directories []string `json:"directories"`
		}
		if err := decodeStrictJSON(c, &request, 1<<20); err != nil {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		if !requireAudit(c, state, "upload.directories", "", len(request.Directories)) {
			return
		}
		created, err := prepareUploadDirectories(c.Request.Context(), request.Path, request.Directories)
		if err != nil {
			outcome := "failed"
			if len(created) > 0 {
				outcome = "partial"
			}
			code := utils.ErrorCode(err)
			if recordAction(state, c, "upload.directories", outcome, "", code, len(created)) != nil {
				c.JSON(503, gin.H{"ok": false, "code": "audit_unavailable", "created": created})
				return
			}
			c.JSON(operationStatus(err), gin.H{"ok": false, "code": code, "created": created})
			return
		}
		if !finishMutation(c, state, "upload.directories", request.Path, len(created)) {
			return
		}
		c.JSON(200, gin.H{"ok": true, "created": created})
	}
}
