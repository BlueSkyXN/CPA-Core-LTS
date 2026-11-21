package management

import (
	"encoding/json"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestCacheTraceHistoryLookupKeepsSourceInstanceAndSnapshot(t *testing.T) {
	h := &Handler{authManager: auth.NewManager(nil, nil, nil), usageStats: usage.NewRequestStatistics()}
	origin := "archive-fixture"
	join := strings.Repeat("d", 64)
	lookupID := "resp_archived_fixture"
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	add := func(id string, ts time.Time) {
		h.usageStats.Record(t.Context(), coreusage.Record{
			RequestID: id, TraceID: "log-fixture", DiagnosticsID: join, DiagnosticsInstanceID: origin,
			RequestedAt: ts, Model: "fixture", APIKey: "fixture",
			Detail:     coreusage.Detail{InputTokens: 100},
			CacheRoute: &coreusage.CacheRouteSummary{Version: 1, AttemptCount: 1, ResponseDigests: []string{diagnostics.Hash(lookupID)}},
		})
	}
	add("first", at)
	add("second", at.Add(time.Second))
	request := func(values url.Values) cacheTracePage {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/cache-diagnostics/traces?"+values.Encode(), nil)
		h.QueryCacheTraces(c)
		var page cacheTracePage
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	filter := url.Values{"q": {lookupID}, "limit": {"1"}, "instance_id": {origin}}
	first := request(filter)
	if len(first.Items) != 1 || first.Items[0].RequestID != "second" || first.Items[0].Available ||
		first.Items[0].Matched[0] != "response_id" || first.Items[0].InstanceID != origin || first.NextCursor == "" {
		t.Fatalf("historical page: %+v", first)
	}
	add("late", at.Add(2*time.Second))
	filter.Set("cursor", first.NextCursor)
	second := request(filter)
	if len(second.Items) != 1 || second.Items[0].RequestID != "first" || second.NextCursor != "" {
		t.Fatalf("late usage crossed snapshot: %+v", second)
	}
	filter.Del("cursor")
	filter.Set("instance_id", first.CurrentInstanceID)
	if rows := request(filter).Items; len(rows) != 0 {
		t.Fatalf("cross-instance match: %+v", rows)
	}
}

func TestCacheTraceHistoryRowsWithoutIDsHaveStableDistinctKeys(t *testing.T) {
	h := &Handler{authManager: auth.NewManager(nil, nil, nil), usageStats: usage.NewRequestStatistics()}
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	for _, source := range []string{"fixture-a", "fixture-b"} {
		h.usageStats.Record(t.Context(), coreusage.Record{
			TraceID: "shared-log", RequestedAt: at, Source: source, Model: "fixture", APIKey: "fixture",
		})
	}
	request := func() cacheTracePage {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/cache-diagnostics/traces?q=shared-log", nil)
		h.QueryCacheTraces(c)
		var page cacheTracePage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 2 {
			t.Fatalf("unexpected lookup result %d: %s", w.Code, w.Body)
		}
		return page
	}
	first, second := request(), request()
	left, right := first.Items[0].HistoryKey, first.Items[1].HistoryKey
	if left == "" || right == "" || left == right || left != second.Items[0].HistoryKey || right != second.Items[1].HistoryKey {
		t.Fatalf("unstable or colliding historical keys: %+v", first.Items)
	}
}
