package main

import (
	"net/http"
	"path"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/auth"
	"github.com/irains/fileharbor/utils"
)

func registerJobRoutes(group *gin.RouterGroup, authManager *auth.Manager, state *RuntimeState) {
	state.jobsMu.Lock()
	if state.Jobs == nil {
		var err error
		state.Jobs, err = newJobManager(state)
		if err != nil {
			state.SetReady(false)
		}
	}
	manager := state.Jobs
	state.jobsMu.Unlock()
	available := func(c *gin.Context) bool {
		setPrivateResponse(c)
		if manager == nil {
			c.JSON(503, gin.H{"ok": false, "code": "job_unavailable"})
			return false
		}
		identity, err := jobRootIdentity(state.ManagedRoot)
		if err != nil || identity != manager.rootIdentity {
			c.JSON(404, gin.H{"ok": false, "code": "not_found"})
			return false
		}
		return true
	}
	group.GET("/api/jobs", func(c *gin.Context) {
		if !available(c) {
			return
		}
		c.JSON(200, gin.H{"ok": true, "jobs": publicJobs(manager.list(manager.scope(authInfo(c).Username)))})
	})
	group.GET("/api/jobs/:id", func(c *gin.Context) {
		if !available(c) {
			return
		}
		for _, job := range manager.list(manager.scope(authInfo(c).Username)) {
			if job.ID == c.Param("id") {
				c.JSON(200, gin.H{"ok": true, "job": publicJob(&job)})
				return
			}
		}
		c.JSON(404, gin.H{"ok": false, "code": "not_found"})
	})
	if reader || uploader {
		return
	}
	submit := func(c *gin.Context) {
		if !available(c) {
			return
		}
		var request struct {
			Kind         string                 `json:"kind"`
			Key          string                 `json:"key"`
			Previous     string                 `json:"previous"`
			Path         string                 `json:"path"`
			Version      string                 `json:"version"`
			Destination  string                 `json:"destination"`
			Name         string                 `json:"name"`
			Target       utils.ExtractionTarget `json:"target"`
			ListingToken string                 `json:"listing_token"`
			Entries      []utils.ItemRequest    `json:"entries"`
		}
		if err := decodeStrictJSON(c, &request, 64<<10); err != nil {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		if id := c.Param("id"); id != "" {
			if request.Previous != "" && request.Previous != id {
				jsonError(c, 400, utils.ErrInvalidPath)
				return
			}
			request.Previous = id
		}
		if !isTrashRecordID(request.Key) || (request.Kind != "extract" && request.Kind != "copy" && request.Kind != "compress") {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		// Hidden keys remain reserved even when their original inputs no longer
		// exist. Never turn a replay into a fresh execution or a source error.
		manager.mu.Lock()
		hiddenKey := false
		for _, existing := range manager.jobs {
			if existing.Scope == manager.scope(authInfo(c).Username) && existing.Key == request.Key && existing.Hidden {
				hiddenKey = true
				break
			}
		}
		manager.mu.Unlock()
		if hiddenKey {
			c.JSON(http.StatusConflict, gin.H{"ok": false, "code": "job_hidden"})
			return
		}
		if request.Path != "" && len(request.Entries) > 0 || request.Name != "" && utils.ValidateLeafName(request.Name) != nil {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		if request.Kind == "extract" {
			switch request.Target.Mode {
			case "new_folder":
				if request.Target.Directory != "" || request.Target.Name != "" && utils.ValidateLeafName(request.Target.Name) != nil {
					jsonError(c, 400, utils.ErrInvalidPath)
					return
				}
			case "current":
				if request.Target.Directory != "" || request.Target.Name != "" {
					jsonError(c, 400, utils.ErrInvalidPath)
					return
				}
			case "chosen":
				if request.Target.Name != "" {
					jsonError(c, 400, utils.ErrInvalidPath)
					return
				}
				_, clean, _, err := utils.ResolveDirectory(request.Target.Directory, true)
				if err != nil {
					jsonError(c, operationStatus(err), err)
					return
				}
				request.Target.Directory = clean
			default:
				jsonError(c, 400, utils.ErrInvalidPath)
				return
			}
		} else if request.Target.Mode != "" || request.Target.Directory != "" || request.Target.Name != "" {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		sources := []utils.FileJobSource{}
		if len(request.Entries) > 0 {
			directory, allowed, ok := authManager.ReadListing(authInfo(c).SessionID, request.ListingToken)
			if !ok {
				jsonError(c, 409, utils.ErrSourceChanged)
				return
			}
			selection, err := utils.ValidateSelection(directory, allowed, request.Entries)
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			for _, item := range selection.Items {
				sources = append(sources, utils.FileJobSource{Path: item.Relative, Version: utils.EntryVersion(item.Info)})
			}
		} else {
			_, relative, info, err := utils.ResolveExisting(request.Path, false)
			if err != nil {
				jsonError(c, operationStatus(err), err)
				return
			}
			if request.Version == "" || request.Version != utils.EntryVersion(info) {
				jsonError(c, 409, utils.ErrSourceChanged)
				return
			}
			sources = append(sources, utils.FileJobSource{Path: relative, Version: request.Version})
		}
		if request.Kind == "extract" && (len(sources) != 1 || request.Target.Mode == "") {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		if request.Kind == "compress" {
			request.Destination = path.Dir(sources[0].Path)
			if request.Destination == "." {
				request.Destination = ""
			}
		}
		if request.Kind != "extract" {
			if _, clean, _, err := utils.ResolveDirectory(request.Destination, true); err != nil {
				jsonError(c, operationStatus(err), err)
				return
			} else {
				request.Destination = clean
			}
		}
		scope := manager.scope(authInfo(c).Username)
		if !requireAudit(c, state, "job.submit", sources[0].Path, 1) {
			return
		}
		job, err := manager.submit(&FileJob{Owner: authInfo(c).Username, Scope: scope, Key: request.Key, Previous: request.Previous, Kind: request.Kind, Sources: sources, Destination: request.Destination, Name: request.Name, Target: request.Target})
		if err != nil {
			status, code := jobError(err)
			_ = recordAction(state, c, "job.submit", "failed", sources[0].Path, code, 0)
			c.JSON(status, gin.H{"ok": false, "code": code})
			return
		}
		if !finishMutation(c, state, "job.submit", sources[0].Path, 1) {
			return
		}
		manager.mu.Lock()
		defer manager.mu.Unlock()
		c.JSON(http.StatusAccepted, gin.H{"ok": true, "job": publicJob(job)})
	}
	group.DELETE("/api/jobs/:id", csrfRequired(authManager), mutationAuditMiddleware(state), func(c *gin.Context) {
		if !available(c) {
			return
		}
		info := authInfo(c)
		id := c.Param("id")
		err := manager.hide(manager.scope(info.Username), id, func(outcome, code string) error {
			return state.Record(AuditEvent{Event: "job.hide", Outcome: outcome, Principal: info.Username, AuthMethod: "session", ClientIP: c.ClientIP(), JobID: id, Code: code})
		})
		if err != nil {
			status, code := jobError(err)
			c.JSON(status, gin.H{"ok": false, "code": code})
			return
		}
		c.Status(http.StatusNoContent)
	})
	group.POST("/api/jobs", csrfRequired(authManager), mutationAuditMiddleware(state), submit)
	group.POST("/api/jobs/:id/retry", csrfRequired(authManager), mutationAuditMiddleware(state), submit)
	group.POST("/api/jobs/:id/cancel", csrfRequired(authManager), mutationAuditMiddleware(state), func(c *gin.Context) {
		if !available(c) {
			return
		}
		manager.mu.Lock()
		defer manager.mu.Unlock()
		job := manager.jobs[c.Param("id")]
		if job == nil || job.Hidden || job.Scope != manager.scope(authInfo(c).Username) {
			c.JSON(404, gin.H{"ok": false, "code": "not_found"})
			return
		}
		if !requireAudit(c, state, "job.cancel", job.Sources[0].Path, 1) {
			return
		}
		if !jobTerminal(job.State) {
			job.CancelRequested = true
			if job.State == "queued" {
				job.State = "cancelled"
				job.Code = "request_cancelled"
			} else if manager.runningCancel != nil {
				manager.runningCancel()
			}
			if err := manager.persist(job); err != nil {
				c.JSON(503, gin.H{"ok": false, "code": "job_unavailable"})
				return
			}
		}
		if !finishMutation(c, state, "job.cancel", job.Sources[0].Path, 1) {
			return
		}
		c.JSON(200, gin.H{"ok": true, "job": publicJob(job)})
	})
}

func jobError(err error) (int, string) {
	switch code := err.Error(); code {
	case "not_found":
		return http.StatusNotFound, code
	case "job_hidden", "job_not_terminal", "job_key_conflict", "job_limit":
		return http.StatusConflict, code
	case "audit_unavailable":
		return http.StatusServiceUnavailable, code
	default:
		return http.StatusServiceUnavailable, "job_unavailable"
	}
}

// Public responses exclude storage scope, idempotency keys and private stages.
func publicJob(job *FileJob) gin.H {
	return gin.H{"id": job.ID, "previous": job.Previous, "kind": job.Kind, "sources": job.Sources, "destination": job.Destination, "name": job.Name, "target": job.Target, "state": job.State, "phase": job.Phase, "bytes": job.Bytes, "items": job.Items, "published": job.Published, "intent": job.Intent, "code": job.Code, "cancel_requested": job.CancelRequested, "created": job.Created, "updated": job.Updated}
}
func publicJobs(jobs []FileJob) []gin.H {
	result := make([]gin.H, 0, len(jobs))
	for i := range jobs {
		result = append(result, publicJob(&jobs[i]))
	}
	return result
}
