package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/utils"
)

func searchHandler(c *gin.Context) {
	setPrivateResponse(c)
	if authInfo(c).Bearer {
		c.JSON(http.StatusForbidden, gin.H{"ok": false, "code": "browser_session_required"})
		return
	}
	allowed := map[string]bool{"path": true, "name": true, "extension": true, "recursive": true, "min_size": true, "max_size": true, "modified_after": true, "modified_before": true}
	for key, values := range c.Request.URL.Query() {
		if !allowed[key] || len(values) != 1 || len(values[0]) > 4096 {
			jsonError(c, http.StatusBadRequest, utils.ErrInvalidPath)
			return
		}
	}
	options := utils.SearchOptions{Directory: c.Query("path"), Name: c.Query("name"), Extension: c.Query("extension")}
	invalid := false
	if values, present := c.Request.URL.Query()["recursive"]; present {
		value := values[0]
		parsed, err := strconv.ParseBool(value)
		invalid = err != nil
		options.Recursive = parsed
	}
	for key, target := range map[string]**int64{"min_size": &options.MinSize, "max_size": &options.MaxSize} {
		if values, present := c.Request.URL.Query()[key]; present {
			value := values[0]
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				invalid = true
			} else {
				*target = &parsed
			}
		}
	}
	for key, target := range map[string]**time.Time{"modified_after": &options.ModifiedAfter, "modified_before": &options.ModifiedBefore} {
		if values, present := c.Request.URL.Query()[key]; present {
			value := values[0]
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				invalid = true
			} else {
				*target = &parsed
			}
		}
	}
	if invalid {
		jsonError(c, http.StatusBadRequest, utils.ErrInvalidPath)
		return
	}
	release, ok := beginReadScan(c)
	if !ok {
		return
	}
	defer release()
	result, err := utils.SearchManaged(c.Request.Context(), options)
	if err != nil {
		jsonError(c, operationStatus(err), err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": result})
}
