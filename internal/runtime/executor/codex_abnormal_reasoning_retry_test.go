package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutorAbnormalReasoningRetry_NonStreaming(t *testing.T) {
	testCases := []struct {
		name       string
		cfg        *config.Config
		auth       *cliproxyauth.Auth
		model      string
		reasoning  int
		wantRetry  bool
		authIDs    []string
		authKind   string
		provider   string
		metadata   map[string]any
		attributes map[string]string
	}{
		{
			name:      "feature disabled",
			cfg:       &config.Config{},
			model:     "gpt-5.5",
			reasoning: 516,
		},
		{
			name:      "enabled matching oauth",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 516,
			wantRetry: true,
		},
		{
			name:      "normal reasoning token",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 128,
		},
		{
			name:      "model mismatch",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.4",
			reasoning: 516,
		},
		{
			name:      "api key channel ignored",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 516,
			authKind:  "apikey",
			metadata:  map[string]any{},
		},
		{
			name:      "auth id whitelist mismatch",
			cfg:       codexAbnormalReasoningRetryTestConfig([]string{"other-auth"}, nil),
			model:     "gpt-5.5",
			reasoning: 516,
		},
		{
			name:      "provider mismatch",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 516,
			provider:  "openai",
		},
		{
			name:      "auth kind fallback oauth",
			cfg:       codexAbnormalReasoningRetryTestConfig(nil, nil),
			model:     "gpt-5.5",
			reasoning: 1034,
			authKind:  " ",
			wantRetry: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newCodexAbnormalReasoningRetryServer(t, tc.model, tc.reasoning)
			defer server.Close()

			auth := tc.auth
			if auth == nil {
				auth = codexAbnormalReasoningRetryTestAuth(server.URL)
			}
			if tc.provider != "" {
				auth.Provider = tc.provider
			}
			if tc.authKind != "" {
				auth.Attributes["auth_kind"] = tc.authKind
			}
			if tc.metadata != nil {
				auth.Metadata = tc.metadata
			}
			for key, value := range tc.attributes {
				auth.Attributes[key] = value
			}

			executor := NewCodexExecutor(tc.cfg)
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   tc.model,
				Payload: []byte(`{"model":"` + tc.model + `","input":"hello"}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Stream:       false,
			})

			if tc.wantRetry {
				assertRetryWithoutPenaltyError(t, err)
				return
			}
			if err != nil {
				t.Fatalf("Execute error = %v, want nil", err)
			}
		})
	}
}

func TestCodexExecutorAbnormalReasoningRetry_MatchesRequestedModelMetadata(t *testing.T) {
	server := newCodexAbnormalReasoningRetryServer(t, "gpt-5.4-upstream", 516)
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, nil))
	_, err := executor.Execute(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.4-upstream",
		Payload: []byte(`{"model":"gpt-5.4-upstream","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.5-client",
		},
	})
	assertRetryWithoutPenaltyError(t, err)
}

func TestCodexAbnormalReasoningRetryAuthKindNormalization(t *testing.T) {
	for _, raw := range []string{"apikey", "api-key", "api_key", " APIKEY "} {
		if got := normalizeCodexAbnormalReasoningRetryAuthKind(raw); got != "api_key" {
			t.Fatalf("normalizeCodexAbnormalReasoningRetryAuthKind(%q) = %q, want api_key", raw, got)
		}
	}
	if got := normalizeCodexAbnormalReasoningRetryAuthKind("OAuth"); got != "oauth" {
		t.Fatalf("normalizeCodexAbnormalReasoningRetryAuthKind(OAuth) = %q, want oauth", got)
	}
}

func TestCodexExecutorAbnormalReasoningRetry_StreamingBufferDropsAbnormalChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"bad"}` + "\n\n"))
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5", 1034)))
	}))
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, nil))
	result, err := executor.ExecuteStream(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}

	var payloads int
	var streamErr error
	for chunk := range result.Chunks {
		if len(chunk.Payload) > 0 {
			payloads++
		}
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if payloads != 0 {
		t.Fatalf("payload chunks = %d, want 0", payloads)
	}
	assertRetryWithoutPenaltyError(t, streamErr)
}

func TestCodexExecutorAbnormalReasoningRetry_StreamingBufferDisabledDoesNotRetry(t *testing.T) {
	streamBuffer := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"visible"}` + "\n\n"))
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5", 1034)))
	}))
	defer server.Close()

	executor := NewCodexExecutor(codexAbnormalReasoningRetryTestConfig(nil, &streamBuffer))
	result, err := executor.ExecuteStream(context.Background(), codexAbnormalReasoningRetryTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v", err)
	}

	var sawPayload bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		if bytes.Contains(chunk.Payload, []byte("visible")) || bytes.Contains(chunk.Payload, []byte("response.completed")) {
			sawPayload = true
		}
	}
	if !sawPayload {
		t.Fatal("expected visible streamed payload when stream-buffer=false")
	}
}

func codexAbnormalReasoningRetryTestConfig(authIDs []string, streamBuffer *bool) *config.Config {
	return &config.Config{
		Codex: config.CodexConfig{
			AbnormalReasoningRetry: config.CodexAbnormalReasoningRetryConfig{
				Enabled:      true,
				AuthIDs:      authIDs,
				StreamBuffer: streamBuffer,
			},
		},
	}
}

func codexAbnormalReasoningRetryTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "codex-oauth-1",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":  baseURL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
			"email":        "test@example.com",
		},
	}
}

func newCodexAbnormalReasoningRetryServer(t *testing.T, model string, reasoning int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexCompletedSSE(model, reasoning)))
	}))
}

func codexCompletedSSE(model string, reasoning int) string {
	return `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1775555723,"status":"completed","model":"` + model + `","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3,"output_tokens_details":{"reasoning_tokens":` + strconv.Itoa(reasoning) + `}}}}` + "\n\n"
}

func assertRetryWithoutPenaltyError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want retry without penalty")
	}
	var retry interface {
		RetryWithoutPenalty() bool
	}
	if !errors.As(err, &retry) || !retry.RetryWithoutPenalty() {
		t.Fatalf("error %T does not implement RetryWithoutPenalty(): %v", err, err)
	}
}
