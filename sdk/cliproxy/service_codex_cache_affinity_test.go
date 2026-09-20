package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	execpkg "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestServiceReloadPreservesCodexCacheAffinity(t *testing.T) {
	keys := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "prompt_cache_key").Exists() {
			t.Error("unexpected automatic pck")
		}
		keys <- r.Header.Get("Session-Id")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.Codex.CacheAffinity.Strategy = "client-aware"
	cfg.DisableImageGeneration = internalconfig.DisableImageGenerationAll
	service := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := &coreauth.Auth{ID: "synthetic-auth", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL}, Metadata: map[string]any{"access_token": "synthetic"}}
	for n := 0; n < 2; n++ {
		if n == 0 {
			service.ensureExecutorsForAuth(auth)
		} else {
			updated := *cfg
			updated.Port = 8081
			service.cfg = &updated
			service.ensureExecutorsForAuthWithMode(auth, true)
		}
		executor, ok := service.coreManager.Executor("codex")
		if !ok {
			t.Fatal("executor missing")
		}
		_, err := executor.Execute(context.Background(), auth, execpkg.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"synthetic"}]}`), Metadata: map[string]any{execpkg.CallerScopeMetadataKey: "caller", execpkg.RequestIDMetadataKey: fmt.Sprint(n)}}, execpkg.Options{SourceFormat: translator.FormatOpenAIResponse})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, second := <-keys, <-keys
	if first == "" || first != second {
		t.Fatal("force rebind discarded inferred group")
	}
}
