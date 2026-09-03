package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestOpenAIResponsesUsesHotReloadedRequestBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	const (
		authID  = "request-body-limit-auth"
		modelID = "request-body-limit-model"
	)
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{APIRequestBodyMaxBytes: 64}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	body := `{"model":"` + modelID + `","input":"` + strings.Repeat("x", 64) + `"}`
	if len(body) <= 64 || len(body) > 128 {
		t.Fatalf("test body length = %d, want 65..128", len(body))
	}

	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		return resp
	}

	if resp := request(); resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status before reload = %d, want %d; body=%s", resp.Code, http.StatusRequestEntityTooLarge, resp.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls before reload = %d, want 0", executor.calls)
	}

	base.UpdateClients(&sdkconfig.SDKConfig{APIRequestBodyMaxBytes: 128})
	if resp := request(); resp.Code != http.StatusOK {
		t.Fatalf("status after reload = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls after reload = %d, want 1", executor.calls)
	}
}
