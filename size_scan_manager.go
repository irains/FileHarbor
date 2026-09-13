package main

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/auth"
	"github.com/irains/fileharbor/utils"
)

type sizeScan struct {
	Kind       string `json:"kind"`
	generation uint64
	completed  time.Time
	ID         string               `json:"id"`
	Path       string               `json:"path"`
	State      string               `json:"state"`
	Result     *utils.DirectorySize `json:"result,omitempty"`
	Code       string               `json:"code,omitempty"`
	owner      string
	created    time.Time
	cancel     context.CancelFunc
}
type sizeScanManager struct {
	mu    sync.Mutex
	scans map[string]*sizeScan
}

func registerSizeRoutes(group *gin.RouterGroup, authManager *auth.Manager, state *RuntimeState) {
	manager := &sizeScanManager{scans: map[string]*sizeScan{}}
	group.POST("/api/size-scans", csrfRequired(authManager), func(c *gin.Context) {
		setPrivateResponse(c)
		var request struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		}
		if err := decodeStrictJSON(c, &request, 8192); err != nil {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		relative := request.Path
		var err error
		if request.Kind == "" {
			request.Kind = "directory"
		}
		if request.Kind == "directory" {
			_, relative, _, err = utils.ResolveDirectory(request.Path, true)
		} else if request.Kind != "trash" || request.Path != "" {
			err = utils.ErrInvalidPath
		}
		if err != nil {
			jsonError(c, operationStatus(err), err)
			return
		}

		owner := authInfo(c).Username
		manager.mu.Lock()
		defer manager.mu.Unlock()
		for id, scan := range manager.scans {
			if time.Since(scan.created) > 5*time.Minute && scan.State != "running" {
				delete(manager.scans, id)
				continue
			}
			if scan.owner == owner && scan.Path == relative && scan.Kind == request.Kind && (scan.State == "running" || scan.State == "succeeded" && time.Since(scan.completed) < 30*time.Second && scan.generation == state.scanGeneration.Load()) {
				c.JSON(200, gin.H{"ok": true, "scan": scan})
				return
			}
		}
		if len(manager.scans) >= 1000 {
			c.JSON(429, gin.H{"ok": false, "code": "scan_busy"})
			return
		}
		release, ok := beginReadScan(c)
		if !ok {
			return
		}
		id, err := newTrashRecordID()
		if err != nil {
			release()
			c.JSON(503, gin.H{"ok": false, "code": "io_error"})
			return
		}
		ctx, cancel := context.WithTimeout(state.scanContext, 10*time.Second)
		if !state.startScan() {
			cancel()
			release()
			c.JSON(503, gin.H{"ok": false, "code": "scan_busy"})
			return
		}
		scan := &sizeScan{Kind: request.Kind, ID: id, Path: relative, State: "running", owner: owner, created: time.Now(), generation: state.scanGeneration.Load(), cancel: cancel}
		manager.scans[id] = scan
		go func() {
			defer state.scanWorkers.Done()
			defer cancel()
			defer release()
			var result utils.DirectorySize
			var err error
			if scan.Kind == "trash" {
				bin := &RecycleBin{directory: state.TrashDir}
				result, err = bin.ScanSize(ctx)
			} else {
				result, err = utils.ScanDirectorySize(ctx, relative)
			}
			manager.mu.Lock()
			defer manager.mu.Unlock()
			scan.completed = time.Now()
			scan.Result = &result
			if err != nil {
				scan.State = "failed"
				scan.Code = utils.ErrorCode(err)
				if ctx.Err() != nil {
					scan.State = "cancelled"
					scan.Code = "request_cancelled"
				}
			} else {
				scan.State = "succeeded"
			}
		}()
		c.JSON(202, gin.H{"ok": true, "scan": scan})
	})
	get := func(c *gin.Context) {
		setPrivateResponse(c)
		manager.mu.Lock()
		defer manager.mu.Unlock()
		scan := manager.scans[c.Param("id")]
		if scan == nil || scan.owner != authInfo(c).Username || time.Since(scan.created) > 5*time.Minute {
			c.JSON(404, gin.H{"ok": false, "code": "not_found"})
			return
		}
		if c.Request.Method == "DELETE" && scan.State == "running" {
			scan.cancel()
		}
		c.JSON(200, gin.H{"ok": true, "scan": scan})
	}
	group.GET("/api/size-scans/:id", get)
	group.DELETE("/api/size-scans/:id", csrfRequired(authManager), get)
}
