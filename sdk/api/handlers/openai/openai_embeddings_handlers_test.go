package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type embeddingsOwnershipExecutor struct {
	original []byte
	alias    string
}

func (e *embeddingsOwnershipExecutor) Identifier() string { return "xai" }

func (e *embeddingsOwnershipExecutor) Execute(ctx context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.original = opts.OriginalRequest
	if len(e.original) == 0 {
		e.original = req.Payload
	}
	e.alias = usage.RequestedModelAliasFromContext(ctx)
	return coreexecutor.Response{Payload: []byte(`{"object":"list","data":[],"usage":{"prompt_tokens":1,"total_tokens":1}}`)}, nil
}

func (e *embeddingsOwnershipExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("unexpected streaming request")
}

func (e *embeddingsOwnershipExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *embeddingsOwnershipExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected token count request")
}

func (e *embeddingsOwnershipExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HTTP delegation")
}

func TestEmbeddingsRequestedModelOwnsInput(t *testing.T) {
	executor := &embeddingsOwnershipExecutor{}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "embedding-ownership-auth", Provider: "xai", Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	const model = "embedding-ownership-model"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model, Object: "model", OwnedBy: "xai", Type: "xai"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.POST("/v1/embeddings", handler.Embeddings)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"`+model+`","input":"synthetic input"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("embedding status %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(executor.original) == 0 {
		t.Fatal("executor did not receive the original request")
	}
	for i := range executor.original {
		executor.original[i] = 'X'
	}
	if executor.alias != model {
		t.Fatalf("requested model changed after input reuse: %q", executor.alias)
	}
}
