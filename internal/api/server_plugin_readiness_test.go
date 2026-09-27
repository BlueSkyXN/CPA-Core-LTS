package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPluginReadinessRouteUsesManagementAuthentication(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "synthetic-readiness-key")
	server := newTestServerWithOptions(t, WithLocalManagementPassword("synthetic-readiness-key"))
	for _, authorized := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/missing/readiness", nil)
		request.RemoteAddr = "127.0.0.1:10001"
		if authorized {
			request.Header.Set("Authorization", "Bearer synthetic-readiness-key")
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if !authorized && recorder.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated readiness status=%d", recorder.Code)
		}
		if authorized && (recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "plugin_host_unavailable")) {
			t.Fatalf("authenticated readiness did not reach handler: status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
}
