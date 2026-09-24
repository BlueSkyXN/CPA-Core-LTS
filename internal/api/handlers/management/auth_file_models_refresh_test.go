package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type testModelDiscoveryError struct{ code string }

func (e testModelDiscoveryError) Error() string      { return "secret pt-test must stay private" }
func (e testModelDiscoveryError) PluginCode() string { return e.code }

func TestRefreshAuthFileModelsReportsSafeFailureAndSuccess(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "qoder-refresh-test", FileName: "qoder-refresh-test.json", Provider: "qoder", Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	h.SetAuthModelsRefreshHook(func(_ context.Context, got *coreauth.Auth) error {
		if got.ID != auth.ID {
			t.Fatalf("refresh auth ID = %q", got.ID)
		}
		return testModelDiscoveryError{code: "catalog_unavailable"}
	})

	call := func(name string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/models/refresh?name="+name, nil)
		h.RefreshAuthFileModels(ctx)
		return recorder
	}

	failure := call(auth.FileName)
	if failure.Code != http.StatusBadGateway || strings.Contains(failure.Body.String(), "pt-test") {
		t.Fatalf("unsafe discovery failure: status=%d body=%s", failure.Code, failure.Body.String())
	}
	var failedBody map[string]any
	if err := json.Unmarshal(failure.Body.Bytes(), &failedBody); err != nil || failedBody["error"] != "catalog_unavailable" {
		t.Fatalf("failure body = %s", failure.Body.String())
	}

	h.SetAuthModelsRefreshHook(func(_ context.Context, _ *coreauth.Auth) error {
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "qmodel_38max", DisplayName: "Qwen3.8-Max"}})
		return nil
	})
	success := call(auth.ID)
	if success.Code != http.StatusOK {
		t.Fatalf("success status=%d body=%s", success.Code, success.Body.String())
	}
	var successBody struct {
		Status string `json:"status"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(success.Body.Bytes(), &successBody); err != nil || successBody.Status != "ready" || len(successBody.Models) != 1 || successBody.Models[0].ID != "qmodel_38max" {
		t.Fatalf("success body = %s", success.Body.String())
	}
}

func TestRefreshAuthFileModelsRejectsMissingOrDisabledAuth(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: "disabled-qoder", FileName: "disabled-qoder.json", Provider: "qoder", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	for _, tc := range []struct {
		name string
		want int
	}{
		{name: "", want: http.StatusBadRequest},
		{name: "missing.json", want: http.StatusNotFound},
		{name: "disabled-qoder.json", want: http.StatusConflict},
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/models/refresh?name="+tc.name, nil)
		h.RefreshAuthFileModels(ctx)
		if recorder.Code != tc.want {
			t.Fatalf("name=%q status=%d want=%d", tc.name, recorder.Code, tc.want)
		}
	}
}

func TestSafeModelDiscoveryCode(t *testing.T) {
	if got := safeModelDiscoveryCode("models_schema_invalid"); got != "models_schema_invalid" {
		t.Fatalf("valid code = %q", got)
	}
	for _, code := range []string{"BadCode", "secret pt-test", "a\nsecret", strings.Repeat("a", 65)} {
		if got := safeModelDiscoveryCode(code); got != "" {
			t.Fatalf("unsafe code %q accepted as %q", code, got)
		}
	}
}
