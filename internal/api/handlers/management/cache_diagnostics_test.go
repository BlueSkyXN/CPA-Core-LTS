package management

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

type cacheDiagnosticsTestExecutor struct {
	auth.ProviderExecutor
	provider    string
	lastTraceID string
	lastAttempt *diagnostics.AttemptRef
}

func (e *cacheDiagnosticsTestExecutor) Identifier() string { return e.provider }

func (e *cacheDiagnosticsTestExecutor) Execute(ctx context.Context, a *auth.Auth, req exec.Request, opts exec.Options) (exec.Response, error) {
	ref := diagnostics.FromContext(ctx)
	e.lastTraceID = ref.ID()
	diagnostics.Record(ctx, diagnostics.Event{
		Layer:        "oauth_group",
		Method:       "session_affinity",
		Result:       "selected",
		ScopeHash:    "scope-h",
		IdentityHash: "id-h",
	})
	reqID := ""
	if opts.Metadata != nil {
		if v, ok := opts.Metadata[exec.RequestIDMetadataKey].(string); ok {
			reqID = v
		}
	}
	attempt := (&diagnostics.Execution{
		Ref:       ref,
		ID:        reqID,
		AuthIndex: a.Index,
		Provider:  e.provider,
		Model:     req.Model,
	}).Begin("http", "https://api.openai.com/v1/responses", req.Payload, nil)
	attempt.Status(200, false)
	attempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"resp_` + reqID + `","model":"` + req.Model + `"}`))
	e.lastAttempt = attempt
	return exec.Response{}, nil
}

func (e *cacheDiagnosticsTestExecutor) SnapshotCacheAffinity(scope, identity string, _ int) diagnostics.AffinitySnapshot {
	return diagnostics.AffinitySnapshot{
		Status:       "bound",
		ScopeHash:    scope,
		IdentityHash: identity,
		Rows: []diagnostics.AffinityRow{{
			Kind:       "group",
			ObjectHash: "obj",
			GroupHash:  "grp",
		}},
	}
}

func newCacheDiagnosticsTestRouter(t *testing.T) (*gin.Engine, *auth.Manager, *cacheDiagnosticsTestExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	m := auth.NewManager(nil, nil, nil)
	m.SetConfig(&config.Config{UsageStatisticsEnabled: true})
	executor := &cacheDiagnosticsTestExecutor{provider: "cache-diag-test"}
	m.RegisterExecutor(executor)
	authID := "cache-diag-auth-" + t.Name()
	a := &auth.Auth{ID: authID, Provider: executor.provider, Status: auth.StatusActive}
	if _, err := m.Register(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, a.Provider, []*registry.ModelInfo{{ID: "diag-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	h := &Handler{
		cfg:            &config.Config{UsageStatisticsEnabled: true},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "synthetic-management-secret",
		authManager:    m,
	}
	router := gin.New()
	group := router.Group("/v0/management", h.Middleware())
	group.GET("/cache-diagnostics/capabilities", h.GetCacheDiagnosticsCapabilities)
	group.GET("/cache-diagnostics/traces", h.QueryCacheTraces)
	group.GET("/cache-diagnostics/requests/:id", h.GetCacheDiagnostics)
	group.POST("/cache-diagnostics/session", h.QueryCacheSession)
	return router, m, executor
}

func doManagementJSONRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/v0/management/cache-diagnostics"+path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Management-Key", "synthetic-management-secret")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}

func TestCacheDiagnosticsUppercaseIDAndSessionNoStore(t *testing.T) {
	router, m, executor := newCacheDiagnosticsTestRouter(t)
	_, err := m.Execute(t.Context(), []string{executor.provider}, exec.Request{
		Model:   "diag-model",
		Payload: []byte(`{"input":"hello"}`),
	}, exec.Options{
		Metadata: map[string]any{
			exec.RequestIDMetadataKey:          "req-upper-1",
			exec.CanonicalSessionIDMetadataKey: "test-session",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	traceID := executor.lastTraceID
	sessionHash := diagnostics.Hash("test-session")

	// GetCacheDiagnostics must accept uppercase 64-hex ID.
	getRes := doManagementJSONRequest(router, "GET", "/requests/"+strings.ToUpper(traceID), "")
	if getRes.Code != http.StatusOK {
		t.Fatalf("GET uppercase id status=%d body=%s", getRes.Code, getRes.Body.String())
	}
	if got := getRes.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("GET Cache-Control=%q, want no-store", got)
	}
	var gotTrace diagnostics.Trace
	if err := json.Unmarshal(getRes.Body.Bytes(), &gotTrace); err != nil {
		t.Fatal(err)
	}
	if gotTrace.ID != traceID {
		t.Fatalf("trace ID=%q, want %q", gotTrace.ID, traceID)
	}

	pageRes := doManagementJSONRequest(router, "GET", "/traces?q="+strings.ToUpper(traceID), "")
	var page cacheTracePage
	if pageRes.Code != http.StatusOK || json.Unmarshal(pageRes.Body.Bytes(), &page) != nil ||
		len(page.Items) != 1 || page.Items[0].DiagnosticsID != traceID {
		t.Fatalf("uppercase diagnostic search returned status %d, %d rows", pageRes.Code, len(page.Items))
	}

	// QueryCacheSession must accept uppercase 64-hex diagnostics_id and session_hash, and emit Cache-Control: no-store.
	sessionBody := fmt.Sprintf(`{"provider":%q,"model":"diag-model","session_hash":%q,"diagnostics_id":%q}`,
		executor.provider, strings.ToUpper(sessionHash), strings.ToUpper(traceID))
	postRes := doManagementJSONRequest(router, "POST", "/session", sessionBody)
	if postRes.Code != http.StatusOK {
		t.Fatalf("POST session status=%d body=%s", postRes.Code, postRes.Body.String())
	}
	if got := postRes.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("POST session Cache-Control=%q, want no-store", got)
	}
	var snap auth.CacheSessionSnapshot
	if err := json.Unmarshal(postRes.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.SessionHash != sessionHash || snap.OAuth == nil || snap.OAuth.Status != "bound" ||
		snap.OAuth.ScopeHash != "scope-h" || snap.OAuth.IdentityHash != "id-h" {
		t.Fatalf("unexpected session snapshot for uppercase diagnostics_id: %+v", snap)
	}

	// Omitting diagnostics_id still returns 200 with Cache-Control: no-store and OAuth status not_recorded.
	noDiagBody := fmt.Sprintf(`{"provider":%q,"model":"diag-model","session_hash":%q}`, executor.provider, sessionHash)
	noDiagRes := doManagementJSONRequest(router, "POST", "/session", noDiagBody)
	if noDiagRes.Code != http.StatusOK || noDiagRes.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("POST session without diagnostics_id failed: %d %q", noDiagRes.Code, noDiagRes.Header().Get("Cache-Control"))
	}
	var noDiagSnap auth.CacheSessionSnapshot
	if err := json.Unmarshal(noDiagRes.Body.Bytes(), &noDiagSnap); err != nil {
		t.Fatal(err)
	}
	if noDiagSnap.OAuth == nil || noDiagSnap.OAuth.Status != "not_recorded" {
		t.Fatalf("expected OAuth status not_recorded without diagnostics_id, got %+v", noDiagSnap.OAuth)
	}

	// Invalid diagnostics_id returns 400 and preserves Cache-Control: no-store.
	badDiagBody := fmt.Sprintf(`{"provider":%q,"model":"diag-model","session_hash":%q,"diagnostics_id":"bad-id"}`, executor.provider, sessionHash)
	badRes := doManagementJSONRequest(router, "POST", "/session", badDiagBody)
	if badRes.Code != http.StatusBadRequest || badRes.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("POST invalid diagnostics_id status=%d header=%q", badRes.Code, badRes.Header().Get("Cache-Control"))
	}
}

func TestQueryCacheTracesCursorValidationAndPagination(t *testing.T) {
	router, m, executor := newCacheDiagnosticsTestRouter(t)
	for i := 1; i <= 3; i++ {
		_, err := m.Execute(t.Context(), []string{executor.provider}, exec.Request{
			Model:   "diag-model",
			Payload: []byte(`{"input":"hello"}`),
		}, exec.Options{
			Metadata: map[string]any{
				exec.RequestIDMetadataKey: fmt.Sprintf("req-page-%d", i),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Page 1
	p1Res := doManagementJSONRequest(router, "GET", "/traces?limit=2", "")
	if p1Res.Code != http.StatusOK || p1Res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page 1 failed: status=%d header=%q body=%s", p1Res.Code, p1Res.Header().Get("Cache-Control"), p1Res.Body.String())
	}
	var page1 cacheTracePage
	if err := json.Unmarshal(p1Res.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if len(page1.Items) != 2 || page1.NextCursor == "" {
		t.Fatalf("unexpected page 1: %+v", page1)
	}

	// Streaming identical chunks on the active attempt must not expire page 1's cursor (P1-05 + P2-17 integration).
	for i := 0; i < 50; i++ {
		executor.lastAttempt.Observe([]byte(`{"object":"chat.completion.chunk","id":"resp_req-page-3","model":"diag-model"}`))
	}

	// Page 2
	p2Res := doManagementJSONRequest(router, "GET", "/traces?limit=2&cursor="+page1.NextCursor, "")
	if p2Res.Code != http.StatusOK {
		t.Fatalf("page 2 failed after unchanged chunks: status=%d body=%s", p2Res.Code, p2Res.Body.String())
	}
	var page2 cacheTracePage
	if err := json.Unmarshal(p2Res.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || page2.NextCursor != "" {
		t.Fatalf("unexpected page 2: %+v", page2)
	}
	seen := map[string]bool{}
	for _, item := range append(page1.Items, page2.Items...) {
		if seen[item.DiagnosticsID] {
			t.Fatalf("duplicate DiagnosticsID across pages: %s", item.DiagnosticsID)
		}
		seen[item.DiagnosticsID] = true
	}

	// Extract valid cursor fields for boundary tests.
	rawCursorBytes, err := base64.RawURLEncoding.DecodeString(page1.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	var validCursor cacheTraceCursor
	if err := json.Unmarshal(rawCursorBytes, &validCursor); err != nil {
		t.Fatal(err)
	}

	encodeRaw := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	encodeObj := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}

	// Valid explicit a=0 cursor must succeed.
	zeroOffset := encodeObj(cacheTraceCursor{
		Instance: validCursor.Instance, Query: validCursor.Query, Revision: validCursor.Revision, ActiveOffset: 0,
	})
	zRes := doManagementJSONRequest(router, "GET", "/traces?limit=2&cursor="+zeroOffset, "")
	if zRes.Code != http.StatusOK {
		t.Fatalf("explicit a=0 cursor failed: status=%d body=%s", zRes.Code, zRes.Body.String())
	}

	// Invalid cursors must return 400 cache_trace_query_invalid.
	invalidCases := []struct {
		name   string
		cursor string
	}{
		{"empty_object", encodeRaw(`{}`)},
		{"null", encodeRaw(`null`)},
		{"only_offset", encodeRaw(`{"a":0}`)},
		{"only_query", encodeObj(map[string]any{"q": validCursor.Query})},
		{"missing_r_and_a", encodeObj(map[string]any{"i": validCursor.Instance, "q": validCursor.Query})},
		{"missing_i_and_a", encodeObj(map[string]any{"q": validCursor.Query, "r": validCursor.Revision})},
		{"missing_a", encodeObj(map[string]any{"i": validCursor.Instance, "q": validCursor.Query, "r": validCursor.Revision})},
		{"empty_i", encodeObj(map[string]any{"i": "", "q": validCursor.Query, "r": validCursor.Revision, "a": 0})},
		{"empty_r", encodeObj(map[string]any{"i": validCursor.Instance, "q": validCursor.Query, "r": "", "a": 0})},
		{"tampered_q", encodeObj(map[string]any{"i": validCursor.Instance, "q": strings.Repeat("f", 64), "r": validCursor.Revision, "a": 0})},
		{"negative_a", encodeObj(map[string]any{"i": validCursor.Instance, "q": validCursor.Query, "r": validCursor.Revision, "a": -1})},
		{"out_of_bounds_a", encodeObj(map[string]any{"i": validCursor.Instance, "q": validCursor.Query, "r": validCursor.Revision, "a": 99})},
		{"unknown_field", encodeObj(map[string]any{"i": validCursor.Instance, "q": validCursor.Query, "r": validCursor.Revision, "a": 0, "extra": true})},
		{"trailing_json", encodeRaw(string(rawCursorBytes) + `{}`)},
		{"wrong_type", encodeObj(map[string]any{"i": 123, "q": validCursor.Query, "r": validCursor.Revision, "a": 0})},
		{"bad_base64", "!!!"},
		{"padded_base64", "e30="},
		{"malformed_json", encodeRaw(`{bad`)},
	}
	for _, tc := range invalidCases {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			res := doManagementJSONRequest(router, "GET", "/traces?limit=2&cursor="+tc.cursor, "")
			var body map[string]string
			_ = json.Unmarshal(res.Body.Bytes(), &body)
			if res.Code != http.StatusBadRequest || body["code"] != "cache_trace_query_invalid" {
				t.Fatalf("cursor %s: got status=%d body=%s, want 400 cache_trace_query_invalid", tc.name, res.Code, res.Body.String())
			}
		})
	}

	// Expired cursors (instance or revision mismatch, even when offset > len(active)) must return 409 cache_trace_query_expired.
	expiredCases := []struct {
		name   string
		cursor string
	}{
		{"wrong_instance", encodeObj(cacheTraceCursor{Instance: "other-instance", Query: validCursor.Query, Revision: validCursor.Revision, ActiveOffset: 0})},
		{"wrong_revision", encodeObj(cacheTraceCursor{Instance: validCursor.Instance, Query: validCursor.Query, Revision: strings.Repeat("0", 64), ActiveOffset: 0})},
		{"wrong_revision_large_offset", encodeObj(cacheTraceCursor{Instance: validCursor.Instance, Query: validCursor.Query, Revision: strings.Repeat("0", 64), ActiveOffset: 99})},
	}
	for _, tc := range expiredCases {
		t.Run("expired/"+tc.name, func(t *testing.T) {
			res := doManagementJSONRequest(router, "GET", "/traces?limit=2&cursor="+tc.cursor, "")
			var body map[string]string
			_ = json.Unmarshal(res.Body.Bytes(), &body)
			if res.Code != http.StatusConflict || body["code"] != "cache_trace_query_expired" {
				t.Fatalf("cursor %s: got status=%d body=%s, want 409 cache_trace_query_expired", tc.name, res.Code, res.Body.String())
			}
		})
	}
}
