package main

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// Shared by bounded read scans. Reject rather than accumulate waiting requests.
type scanLimiter struct {
	mu     sync.Mutex
	active int
	owners map[string]bool
}

var readScans = scanLimiter{owners: make(map[string]bool)}

func (limiter *scanLimiter) acquire(owner string) (func(), bool) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.active >= 2 || limiter.owners[owner] {
		return nil, false
	}
	limiter.active++
	limiter.owners[owner] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			limiter.mu.Lock()
			defer limiter.mu.Unlock()
			limiter.active--
			delete(limiter.owners, owner)
		})
	}, true
}

func beginReadScan(c *gin.Context) (func(), bool) {
	release, ok := readScans.acquire(authInfo(c).Username)
	if !ok {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusTooManyRequests, gin.H{"ok": false, "code": "scan_busy"})
	}
	return release, ok
}
