package management

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUsageOutputTimingImportRoundTripAndAtomicRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stats := usage.NewRequestStatistics()
	h := &Handler{}
	h.SetUsageStatistics(stats)
	raw := []byte(`{"version":3,"usage":{"total_requests":1,"success_count":1,"failure_count":0,"total_tokens":3,"apis":{"test":{"total_requests":1,"total_tokens":3,"models":{"model":{"total_requests":1,"total_tokens":3,"details":[{"timestamp":"2026-10-13T00:00:00Z","latency_ms":1000,"ttfb_ms":100,"timing_version":1,"output_timing":{"version":1,"streamed":true,"duration_ms":200},"tokens":{"input_tokens":1,"output_tokens":2,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":3},"failed":false,"generate":true}]}}}},"requests_by_day":{},"requests_by_hour":{},"tokens_by_day":{},"tokens_by_hour":{}}}`)
	call := func(body []byte) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(body))
		h.ImportUsageStatistics(c)
		return w.Code
	}
	if got := call(raw); got != http.StatusOK {
		t.Fatalf("valid import status=%d", got)
	}
	before, _ := json.Marshal(stats.Snapshot())
	if !bytes.Contains(before, []byte(`"duration_ms":200`)) {
		t.Fatal("measurement lost")
	}
	for _, bad := range []string{`null`, `{"version":1}`, `{"version":1,"streamed":true,"duration_ms":901}`, `{"version":1,"streamed":false,"duration_ms":0}`, `{"version":1,"streamed":true,"duration_ms":1.5}`} {
		body := bytes.Replace(raw, []byte(`{"version":1,"streamed":true,"duration_ms":200}`), []byte(bad), 1)
		if got := call(body); got != http.StatusBadRequest {
			t.Fatalf("accepted invalid measurement %s status=%d", bad, got)
		}
		after, _ := json.Marshal(stats.Snapshot())
		if !bytes.Equal(before, after) {
			t.Fatal("rejected import changed statistics")
		}
	}
}
