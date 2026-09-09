package management

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

func (h *Handler) GetUsageQueryCapabilities(c *gin.Context) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "usage_query_unavailable"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, h.usageStats.QueryCapabilities())
}

func (h *Handler) QueryUsageSummary(c *gin.Context) { h.queryUsage(c, "summary") }
func (h *Handler) QueryUsagePricing(c *gin.Context) { h.queryUsage(c, "pricing") }
func (h *Handler) QueryUsageDetails(c *gin.Context) { h.queryUsage(c, "details") }

func (h *Handler) queryUsage(c *gin.Context, operation string) {
	if h == nil || h.usageStats == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "usage_query_unavailable"})
		return
	}
	var q usage.QueryRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "usage_query_invalid"})
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"code": "usage_query_invalid"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	var result any
	var err error
	if operation == "details" {
		result, err = h.usageStats.QueryDetails(ctx, q)
	} else {
		result, err = h.usageStats.QuerySummary(ctx, q, operation == "pricing")
	}
	if err != nil {
		status, code := http.StatusBadRequest, "usage_query_invalid"
		if errors.Is(err, usage.ErrQueryExpired) {
			status, code = http.StatusConflict, "usage_query_expired"
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			status, code = http.StatusRequestTimeout, "usage_query_timeout"
		}
		c.JSON(status, gin.H{"code": code})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}
