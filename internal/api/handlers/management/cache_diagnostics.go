package management

import (
	"encoding/hex"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func validDiagnosticHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (h *Handler) GetCacheDiagnostics(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if !validDiagnosticHash(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid diagnostic id"})
		return
	}
	if h.authManager == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "cache diagnostics unavailable"})
		return
	}
	trace, ok := h.authManager.CacheDiagnostics(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "diagnostic record unavailable"})
		return
	}
	c.JSON(http.StatusOK, trace)
}

func (h *Handler) GetCacheDiagnosticsCapabilities(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "cache diagnostics unavailable"})
		return
	}
	cfg := h.configSnapshot()
	enabled := cfg != nil && cfg.UsageStatisticsEnabled
	c.JSON(http.StatusOK, gin.H{"version": 1, "recording_enabled": enabled, "request_details": true, "session_snapshot": true, "store": h.authManager.CacheDiagnosticsOverview()})
}

func (h *Handler) QueryCacheSession(c *gin.Context) {
	var query struct {
		Provider      string `json:"provider"`
		Model         string `json:"model"`
		SessionHash   string `json:"session_hash"`
		DiagnosticsID string `json:"diagnostics_id"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err := c.ShouldBindJSON(&query); err != nil || !validDiagnosticHash(query.SessionHash) || strings.TrimSpace(query.Provider) == "" || strings.TrimSpace(query.Model) == "" || len(query.Model) > 192 || len(query.Provider) > 192 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider, model and session_hash are required"})
		return
	}
	if h.authManager == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "session snapshot unavailable"})
		return
	}
	if query.DiagnosticsID != "" && !validDiagnosticHash(query.DiagnosticsID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid diagnostic id"})
		return
	}
	c.JSON(http.StatusOK, h.authManager.SnapshotCacheSession(strings.TrimSpace(query.Provider), strings.TrimSpace(query.Model), strings.ToLower(query.SessionHash), query.DiagnosticsID))
}
