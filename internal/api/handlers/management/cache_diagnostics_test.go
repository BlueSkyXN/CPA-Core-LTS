package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCacheDiagnosticsManagementAuthAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{UsageStatisticsEnabled: true}, failedAttempts: make(map[string]*attemptInfo), envSecret: "synthetic-management-secret", authManager: auth.NewManager(nil, nil, nil)}
	router := gin.New()
	group := router.Group("/v0/management", h.Middleware())
	group.GET("/cache-diagnostics/capabilities", h.GetCacheDiagnosticsCapabilities)
	group.GET("/cache-diagnostics/traces", h.QueryCacheTraces)
	group.GET("/cache-diagnostics/requests/:id", h.GetCacheDiagnostics)
	group.POST("/cache-diagnostics/session", h.QueryCacheSession)
	for _, tc := range []struct {
		method, path, body string
		authorized         bool
		status             int
	}{
		{"GET", "/capabilities", "", false, 401},
		{"GET", "/capabilities", "", true, 200},
		{"GET", "/traces", "", false, 401},
		{"GET", "/traces", "", true, 200},
		{"GET", "/traces?limit=101", "", true, 400},
		{"GET", "/traces?scope=invalid", "", true, 400},
		{"GET", "/requests/bad", "", true, 400},
		{"GET", "/requests/" + strings.Repeat("a", 64), "", true, 404},
		{"POST", "/session", `{"provider":"codex","model":"model","session_hash":"` + strings.Repeat("a", 64) + `"}`, false, 401},
		{"POST", "/session", `{"provider":"codex","model":"model","session_hash":"` + strings.Repeat("a", 64) + `"}`, true, 200},
		{"POST", "/session", `{"model":"` + strings.Repeat("x", 5000) + `"}`, true, 400},
	} {
		req := httptest.NewRequest(tc.method, "/v0/management/cache-diagnostics"+tc.path, strings.NewReader(tc.body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Content-Type", "application/json")
		if tc.authorized {
			req.Header.Set("X-Management-Key", "synthetic-management-secret")
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, res.Code, res.Body.String())
		}
	}
}

func TestUsageManagementImportDropsDraftCacheRoute(t *testing.T) {
	stats := usage.NewRequestStatistics()
	h := &Handler{authManager: auth.NewManager(nil, nil, nil), usageStats: stats}
	body := []byte(`{"version":3,"usage":{"apis":{"fixture":{"models":{"model":{"details":[{
		"timestamp":"2026-11-20T08:00:00Z","request_id":"synthetic-execution","trace_id":"fixture-log",
		"cache_route":{"version":1,"references":[{"kind":"pck","digest":"ephemeral-only"}]},
		"cache_observation":{"version":1,"read":{"state":"present","value":0},"write":{"state":"absent"}},
		"tokens":{"input_tokens":100,"output_tokens":1,"reasoning_tokens":0,"cached_tokens":0,"total_tokens":101}
	}]}}}}}}`)
	for _, expected := range []usage.MergeResult{{Added: 1}, {Skipped: 1}} {
		resp := performUsageImport(h, body)
		var receipt usage.MergeResult
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &receipt) != nil || receipt != expected {
			t.Fatalf("import status %d, receipt %+v, want %+v", resp.Code, receipt, expected)
		}
	}

	for _, query := range []string{"synthetic-execution", "fixture-log"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/cache-diagnostics/traces?q="+query, nil)
		h.QueryCacheTraces(c)
		var page cacheTracePage
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.HistoryIncluded || len(page.Items) != 0 {
			t.Fatalf("usage leaked into diagnostic lookup: %d %+v", w.Code, page)
		}
	}

	export := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(export)
	h.ExportUsageStatistics(c)
	if export.Code != http.StatusOK || bytes.Contains(export.Body.Bytes(), []byte("cache_route")) {
		t.Fatalf("export includes draft route or failed: %d", export.Code)
	}
	var snapshot usageExportPayload
	if err := json.Unmarshal(export.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	row := snapshot.Usage.APIs["fixture"].Models["model"].Details[0]
	if snapshot.Version != 3 || row.RequestID != "synthetic-execution" || row.Tokens.InputTokens != 100 ||
		row.CacheObservation == nil || row.CacheObservation.Read.State != "present" ||
		row.CacheObservation.Read.Value == nil || *row.CacheObservation.Read.Value != 0 ||
		row.CacheObservation.Write.State != "absent" {
		t.Fatalf("export changed established metering: %+v", row)
	}
}
