package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthModelsRefreshRouteUsesManagementAuthentication(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "refresh-test-key")
	server := newTestServerWithOptions(t, WithLocalManagementPassword("refresh-test-key"))
	path := "/v0/management/auth-files/models/refresh?name=missing.json"

	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.RemoteAddr = "127.0.0.1:10001"
	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, request)
	if unauthorized.Code == http.StatusOK || strings.Contains(unauthorized.Body.String(), "auth_not_found") {
		t.Fatalf("unauthorized refresh reached handler: status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, path, nil)
	request.RemoteAddr = "127.0.0.1:10001"
	request.Header.Set("Authorization", "Bearer refresh-test-key")
	authorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(authorized, request)
	if authorized.Code != http.StatusNotFound || !strings.Contains(authorized.Body.String(), "auth_not_found") {
		t.Fatalf("authorized refresh route: status=%d body=%s", authorized.Code, authorized.Body.String())
	}
}
