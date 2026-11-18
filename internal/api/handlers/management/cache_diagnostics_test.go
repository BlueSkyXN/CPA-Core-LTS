package management

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
	group.GET("/cache-diagnostics/requests/:id", h.GetCacheDiagnostics)
	group.POST("/cache-diagnostics/session", h.QueryCacheSession)
	for _, tc := range []struct {
		method, path, body string
		authorized         bool
		status             int
	}{
		{"GET", "/capabilities", "", false, 401},
		{"GET", "/capabilities", "", true, 200},
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
