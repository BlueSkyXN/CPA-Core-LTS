package executor

import (
	"bytes"
	"context"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_PreservesExplicitPromptCacheKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("userApiKey", "test-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","prompt_cache_key":"tenant:explicit"}`),
	}

	httpReq, body, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), "https://example.com/responses", nil, req, req.Payload, []byte(`{"model":"gpt-5.6-sol","stream":true,"prompt_cache_key":"tenant:explicit"}`))
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "tenant:explicit" {
		t.Fatalf("prompt_cache_key = %q, want explicit client key; body=%s", got, body)
	}
	if got := codexSessionHeaderValue(httpReq.Header); got != "tenant:explicit" {
		t.Fatalf("Session-Id = %q, want tenant:explicit", got)
	}
}

func TestCodexExecutorCacheHelperCanonicalMetadataBypassesLegacyIdentityConfuse(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ginCtx.Request.Header.Set("X-Codex-Turn-Metadata", `{"thread_id":"header-conflict"}`)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{cfg: &config.Config{
		Routing: config.RoutingConfig{Strategy: "fill-first"},
		Codex: config.CodexConfig{
			IdentityConfuse: true,
			ClientMetadata: config.CodexClientMetadataConfig{
				Mode:            config.CodexClientMetadataModeRepair,
				WorkspacePolicy: config.CodexClientMetadataWorkspacePolicyPassthrough,
			},
		},
	}}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex", Metadata: map[string]any{"account_id": "acct-1"}}
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":true,"client_metadata":{"x-codex-turn-metadata":"{\"installation_id\":\"install-1\",\"session_id\":\"thread-1\",\"thread_id\":\"thread-1\",\"turn_id\":\"turn-1\",\"window_id\":\"thread-1:1\",\"request_kind\":\"turn\",\"workspaces\":{\"/Users/private/project\":{\"associated_remote_urls\":{\"origin\":\"https://user:secret@example.com/org/repo.git?token=leak#fragment\"},\"has_changes\":false}}}","x-codex-installation-id":"wrong-install","session_id":"wrong-session","thread_id":"wrong-thread","turn_id":"wrong-turn","x-codex-window-id":"wrong-window:0"}}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-installation-id":"install-1"}}`),
	}

	httpReq, body, state, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), "https://example.com/responses", auth, req, req.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	applyCodexHeaders(httpReq, auth, "oauth-token", true, executor.cfg)
	applyCodexOutboundMetadataHeaders(httpReq.Header, &state)

	if state.enabled || state.promptCacheKey != "" || !state.clientMetadata.CanonicalPresent {
		t.Fatalf("canonical state unexpectedly enabled legacy identity mapping: %+v", state)
	}
	metadata := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
	for key, want := range map[string]string{
		"installation_id": "install-1",
		"session_id":      "thread-1",
		"thread_id":       "thread-1",
		"turn_id":         "turn-1",
		"window_id":       "thread-1:1",
	} {
		if got := gjson.Get(metadata, key).String(); got != want {
			t.Fatalf("canonical %s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(metadata, "secret") || strings.Contains(metadata, "token=leak") || strings.Contains(metadata, "#fragment") {
		t.Fatalf("canonical workspace remote was not sanitized: %s", metadata)
	}
	if got := gjson.Get(metadata, "workspaces./Users/private/project.associated_remote_urls.origin").String(); got != "https://example.com/org/repo.git" {
		t.Fatalf("sanitized remote = %q", got)
	}
	if got := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); got != "install-1" {
		t.Fatalf("flat installation = %q", got)
	}
	if got := gjson.GetBytes(body, "client_metadata.x-codex-window-id").String(); got != "thread-1:1" {
		t.Fatalf("flat window = %q", got)
	}
	if got := httpReq.Header.Get("X-Codex-Window-Id"); got != "thread-1:1" {
		t.Fatalf("X-Codex-Window-Id = %q", got)
	}
	if got := codexSessionHeaderValue(httpReq.Header); got != "thread-1" {
		t.Fatalf("Session_id = %q, want canonical session", got)
	}
	if got := httpReq.Header.Get("X-Codex-Turn-Metadata"); got != state.clientMetadata.TurnMetadata {
		t.Fatalf("X-Codex-Turn-Metadata does not match normalized canonical metadata")
	}
	responseWithIdentityText := []byte(`{"output_text":"install-1 thread-1 turn-1"}`)
	if got := string(applyCodexIdentityConfuseResponsePayload(responseWithIdentityText, state)); got != string(responseWithIdentityText) {
		t.Fatalf("canonical response text was changed by legacy identity replacement: %s", got)
	}
}

func TestCodexExecutorCacheHelperStrictMetadataReturnsSafeBadRequest(t *testing.T) {
	executor := &CodexExecutor{cfg: &config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode: config.CodexClientMetadataModeStrict,
	}}}}
	rawJSON := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"thread_id\":\"thread-private\"}","thread_id":"conflicting-private"}}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: rawJSON}

	_, _, _, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://example.com/responses", &cliproxyauth.Auth{ID: "auth-1"}, req, req.Payload, rawJSON)
	if err == nil {
		t.Fatal("strict metadata conflict was accepted")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("error = %#v, want status 400", err)
	}
	if !strings.Contains(err.Error(), `"code":"invalid_client_metadata"`) {
		t.Fatalf("error body missing stable code: %s", err)
	}
	if strings.Contains(err.Error(), "thread-private") || strings.Contains(err.Error(), "conflicting-private") {
		t.Fatalf("error body leaked client metadata: %s", err)
	}
	requestErr, ok := err.(interface{ IsRequestScoped() bool })
	if !ok || !requestErr.IsRequestScoped() {
		t.Fatalf("error = %T, want request-scoped", err)
	}
}

func TestCodexExecutorCacheHelperOffModeProjectsCanonicalSessionWithoutMutatingBody(t *testing.T) {
	executor := &CodexExecutor{cfg: &config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode: config.CodexClientMetadataModeOff,
	}}}}
	rawJSON := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"off-http-session\",\"thread_id\":\"off-http-session\"}","thread_id":"legacy-conflict"}}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: rawJSON}

	httpReq, body, state, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://example.com/responses", &cliproxyauth.Auth{ID: "auth-off"}, req, req.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper() error = %v", err)
	}
	if !bytes.Equal(body, rawJSON) {
		t.Fatalf("off mode mutated body: got %s want %s", body, rawJSON)
	}
	applyModelHeaderOverrides(httpReq.Header, codexModelHeaderProfile{overrides: map[string]string{"User-Agent": "Codex Desktop (Mac OS)"}})
	if fallback := codexSessionHeaderValue(httpReq.Header); fallback == "" || fallback == "off-http-session" {
		t.Fatalf("expected pre-projection random fallback, got %q", fallback)
	}
	applyCodexOutboundMetadataHeaders(httpReq.Header, &state)
	if got := codexSessionHeaderValue(httpReq.Header); got != "off-http-session" {
		t.Fatalf("Session_id = %q, want off-http-session", got)
	}
	if got := httpReq.Header.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("off mode rebuilt X-Codex-Turn-Metadata = %q", got)
	}
}

func TestCodexExecutorCacheHelperOffModePreservesSDKHeaderOnlyCanonical(t *testing.T) {
	executor := &CodexExecutor{cfg: &config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode: config.CodexClientMetadataModeOff,
	}}}}
	rawJSON := []byte(`{"model":"gpt-5-codex","input":"hello"}`)
	canonical := "  " + `{"request_kind":"turn","session_id":"off-sdk-header-session","thread_id":"off-sdk-header-session"}` + "\t"
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: rawJSON}

	httpReq, body, state, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://example.com/responses", &cliproxyauth.Auth{ID: "auth-off-sdk"}, req, req.Payload, rawJSON, http.Header{"X-Codex-Turn-Metadata": {canonical}})
	if err != nil {
		t.Fatalf("cacheHelper() error = %v", err)
	}
	if !bytes.Equal(body, rawJSON) || gjson.GetBytes(body, "client_metadata").Exists() {
		t.Fatalf("off mode rebuilt body canonical metadata: got %s want %s", body, rawJSON)
	}
	applyCodexOutboundMetadataHeaders(httpReq.Header, &state)
	if got := httpReq.Header.Get("X-Codex-Turn-Metadata"); got != canonical {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want original %q", got, canonical)
	}
	if got := codexSessionHeaderValue(httpReq.Header); got != "off-sdk-header-session" {
		t.Fatalf("Session_id = %q, want off-sdk-header-session", got)
	}
}

func TestCodexExecutorCacheHelperOffModeBodyCanonicalSuppressesConflictingDirectHeader(t *testing.T) {
	executor := &CodexExecutor{cfg: &config.Config{Codex: config.CodexConfig{ClientMetadata: config.CodexClientMetadataConfig{
		Mode: config.CodexClientMetadataModeOff,
	}}}}
	rawJSON := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"session_id\":\"off-body-session\"}"}}`)
	direct := `{"request_kind":"turn","session_id":"off-header-session"}`
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: rawJSON}

	httpReq, body, state, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("openai-response"), "https://example.com/responses", &cliproxyauth.Auth{ID: "auth-off-conflict"}, req, req.Payload, rawJSON, http.Header{"X-Codex-Turn-Metadata": {direct}})
	if err != nil {
		t.Fatalf("cacheHelper() error = %v", err)
	}
	if !bytes.Equal(body, rawJSON) {
		t.Fatalf("off mode mutated body: got %s want %s", body, rawJSON)
	}
	applyCodexOutboundMetadataHeaders(httpReq.Header, &state)
	if got := httpReq.Header.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("conflicting direct canonical header survived body precedence: %q", got)
	}
	if got := codexSessionHeaderValue(httpReq.Header); got != "off-body-session" {
		t.Fatalf("Session_id = %q, want off-body-session", got)
	}
}
