package executor

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_StablePromptCacheKeyFromAPIKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "test-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex"}`),
	}
	url := "https://example.com/responses"

	httpReq, err := executor.cacheHelper(ctx, nil, sdktranslator.FromString("openai"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}

	expectedKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:test-api-key")).String()
	gotKey := gjson.GetBytes(body, "prompt_cache_key").String()
	if gotKey != expectedKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedKey)
	}
	if gotConversation := httpReq.Header.Get("Conversation_id"); gotConversation != "" {
		t.Fatalf("Conversation_id = %q, want empty", gotConversation)
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != expectedKey {
		t.Fatalf("Session_id = %q, want %q", gotSession, expectedKey)
	}

	httpReq2, err := executor.cacheHelper(ctx, nil, sdktranslator.FromString("openai"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error (second call): %v", err)
	}
	body2, errRead2 := io.ReadAll(httpReq2.Body)
	if errRead2 != nil {
		t.Fatalf("read request body (second call): %v", errRead2)
	}
	gotKey2 := gjson.GetBytes(body2, "prompt_cache_key").String()
	if gotKey2 != expectedKey {
		t.Fatalf("prompt_cache_key (second call) = %q, want %q", gotKey2, expectedKey)
	}
}

func TestCodexClaudeCacheKeyPreservesLegacyKeyWithoutNamespace(t *testing.T) {
	got := codexClaudeCacheKey("gpt-5-codex", nil, "user-1")
	if got != "gpt-5-codex-user-1" {
		t.Fatalf("codexClaudeCacheKey without namespace = %q, want legacy key", got)
	}
}

func TestCodexClaudeCacheKeyDisambiguatesScopedComponents(t *testing.T) {
	authAB := &cliproxyauth.Auth{Metadata: map[string]any{"codex_installation_id": "a-b"}}
	authA := &cliproxyauth.Auth{Metadata: map[string]any{"codex_installation_id": "a"}}

	keyABUserC := codexClaudeCacheKey("gpt-5-codex", authAB, "c")
	keyAUserBC := codexClaudeCacheKey("gpt-5-codex", authA, "b-c")
	if keyABUserC == "" || keyAUserBC == "" {
		t.Fatalf("codexClaudeCacheKey returned empty scoped key")
	}
	if keyABUserC == "gpt-5-codex-a-b-c" || keyAUserBC == "gpt-5-codex-a-b-c" {
		t.Fatalf("codexClaudeCacheKey used ambiguous delimiter-only key")
	}
	if keyABUserC == keyAUserBC {
		t.Fatalf("codexClaudeCacheKey collided for namespace/user components containing hyphens")
	}

	keyABUserC2 := codexClaudeCacheKey("gpt-5-codex", authAB, "c")
	if keyABUserC2 != keyABUserC {
		t.Fatalf("codexClaudeCacheKey = %q, want stable %q", keyABUserC2, keyABUserC)
	}
}

func TestCodexExecutorCacheHelper_OpenAIResponsesScopesPromptCacheKeyByAuth(t *testing.T) {
	ctx := context.Background()
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"shared-session"}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: rawJSON,
	}
	url := "https://example.com/responses"
	authA := &cliproxyauth.Auth{
		ID:       "auth-a",
		Metadata: map[string]any{"codex_installation_id": "install-a"},
	}
	authB := &cliproxyauth.Auth{
		ID:       "auth-b",
		Metadata: map[string]any{"codex_installation_id": "install-b"},
	}

	httpReqA, err := executor.cacheHelper(ctx, authA, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper auth A error: %v", err)
	}
	bodyA, errReadA := io.ReadAll(httpReqA.Body)
	if errReadA != nil {
		t.Fatalf("read auth A body: %v", errReadA)
	}
	keyA := gjson.GetBytes(bodyA, "prompt_cache_key").String()
	if keyA == "" || keyA == "shared-session" {
		t.Fatalf("auth A prompt_cache_key = %q, want scoped key", keyA)
	}
	if gotSession := httpReqA.Header.Get("Session_id"); gotSession != keyA {
		t.Fatalf("auth A Session_id = %q, want %q", gotSession, keyA)
	}

	httpReqB, err := executor.cacheHelper(ctx, authB, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper auth B error: %v", err)
	}
	bodyB, errReadB := io.ReadAll(httpReqB.Body)
	if errReadB != nil {
		t.Fatalf("read auth B body: %v", errReadB)
	}
	keyB := gjson.GetBytes(bodyB, "prompt_cache_key").String()
	if keyB == "" || keyB == keyA {
		t.Fatalf("auth B prompt_cache_key = %q, want different from auth A %q", keyB, keyA)
	}

	httpReqA2, err := executor.cacheHelper(ctx, authA, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper auth A second error: %v", err)
	}
	bodyA2, errReadA2 := io.ReadAll(httpReqA2.Body)
	if errReadA2 != nil {
		t.Fatalf("read auth A second body: %v", errReadA2)
	}
	keyA2 := gjson.GetBytes(bodyA2, "prompt_cache_key").String()
	if keyA2 != keyA {
		t.Fatalf("auth A prompt_cache_key second = %q, want stable %q", keyA2, keyA)
	}
}

func TestScopedCodexPromptCacheKeyDisambiguatesScopedComponents(t *testing.T) {
	authAB := &cliproxyauth.Auth{Metadata: map[string]any{"codex_installation_id": "a:b"}}
	authA := &cliproxyauth.Auth{Metadata: map[string]any{"codex_installation_id": "a"}}

	keyABWithC := scopedCodexPromptCacheKey(authAB, "c")
	keyAWithBC := scopedCodexPromptCacheKey(authA, "b:c")
	if keyABWithC == "" || keyAWithBC == "" {
		t.Fatalf("scopedCodexPromptCacheKey returned empty scoped key")
	}
	if keyABWithC == keyAWithBC {
		t.Fatalf("scopedCodexPromptCacheKey collided for namespace/prompt_cache_key components containing colons")
	}

	ambiguousDelimiterKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:auth-session:a:b:c")).String()
	if keyABWithC == ambiguousDelimiterKey || keyAWithBC == ambiguousDelimiterKey {
		t.Fatalf("scopedCodexPromptCacheKey used ambiguous delimiter-only input")
	}

	if got := scopedCodexPromptCacheKey(nil, " cache-1 "); got != "cache-1" {
		t.Fatalf("scopedCodexPromptCacheKey without namespace = %q, want trimmed legacy key", got)
	}
	if got := scopedCodexPromptCacheKey(authA, "   "); got != "" {
		t.Fatalf("scopedCodexPromptCacheKey blank key = %q, want empty", got)
	}
}
