package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/irains/fileharbor/utils"
)

func archivePreviewHandler(c *gin.Context) {
	setPrivateResponse(c)
	if authInfo(c).Bearer {
		c.JSON(http.StatusForbidden, gin.H{"ok": false, "code": "browser_session_required"})
		return
	}
	for key, values := range c.Request.URL.Query() {
		if (key != "path" && key != "version") || len(values) != 1 {
			jsonError(c, http.StatusBadRequest, utils.ErrInvalidPath)
			return
		}
	}
	if len(c.Query("path")) > 4096 || len(c.Query("version")) > 256 {
		jsonError(c, http.StatusBadRequest, utils.ErrInvalidPath)
		return
	}
	release, ok := beginReadScan(c)
	if !ok {
		return
	}
	defer release()
	result, err := utils.PreviewArchive(c.Request.Context(), c.Query("path"), c.Query("version"))
	if err != nil {
		jsonError(c, operationStatus(err), err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "preview": result})
}
