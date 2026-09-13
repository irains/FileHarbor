package main

import (
	"errors"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/auth"
	"github.com/irains/fileharbor/utils"
)

func registerFavoriteRoutes(group *gin.RouterGroup, manager *auth.Manager, state *RuntimeState) {
	store, err := newFavoriteStore(state)
	if err != nil {
		panic(err)
	}
	group.GET("/api/favorites", func(c *gin.Context) {
		setPrivateResponse(c)
		entries, err := store.list(authInfo(c).Username)
		if err != nil {
			c.JSON(503, gin.H{"ok": false, "code": "favorites_unavailable"})
			return
		}
		c.JSON(200, gin.H{"ok": true, "entries": entries})
	})
	handler := func(c *gin.Context) {
		setPrivateResponse(c)
		var request struct {
			Path     string `json:"path"`
			Label    string `json:"label"`
			Revision uint64 `json:"revision"`
		}
		if err := decodeStrictJSON(c, &request, 8192); err != nil {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		method := c.Request.Method
		if method != "POST" && request.Path != "" || method == "DELETE" && request.Label != "" || method == "POST" && request.Revision != 0 {
			jsonError(c, 400, utils.ErrInvalidPath)
			return
		}
		event := "favorite." + map[string]string{"POST": "create", "PATCH": "rename", "DELETE": "remove"}[method]
		if !requireAudit(c, state, event, "", 1) {
			return
		}
		entry, err := store.change(authInfo(c).Username, method, c.Param("id"), request.Path, request.Label, request.Revision)
		if err != nil {
			code := utils.ErrorCode(err)
			status := operationStatus(err)
			switch {
			case errors.Is(err, errFavoriteConflict):
				status, code = 409, "favorite_conflict"
			case errors.Is(err, errFavoriteLimit):
				status, code = 409, "favorite_limit"
			case errors.Is(err, os.ErrNotExist):
				status, code = 404, "not_found"
			}
			if recordAction(state, c, event, "failed", "", code, 0) != nil {
				status, code = 503, "audit_unavailable"
			}
			c.JSON(status, gin.H{"ok": false, "code": code})
			return
		}
		if !finishMutation(c, state, event, entry.Path, 1) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "entry": entry})
	}
	group.POST("/api/favorites", csrfRequired(manager), mutationAuditMiddleware(state), handler)
	group.PATCH("/api/favorites/:id", csrfRequired(manager), mutationAuditMiddleware(state), handler)
	group.DELETE("/api/favorites/:id", csrfRequired(manager), mutationAuditMiddleware(state), handler)
}
