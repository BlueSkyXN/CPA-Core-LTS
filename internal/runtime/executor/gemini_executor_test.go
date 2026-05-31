package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCapGeminiMaxOutputTokensUsesOutputTokenLimit(t *testing.T) {
	body := []byte(`{"generationConfig":{"maxOutputTokens":500000,"temperature":0.2},"contents":[]}`)

	out := capGeminiMaxOutputTokens(body, "gemini-3.1-pro-preview")

	if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != 65536 {
		t.Fatalf("maxOutputTokens = %d, want 65536", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.temperature").Float(); got != 0.2 {
		t.Fatalf("temperature = %v, want 0.2", got)
	}
}

func TestCapGeminiMaxOutputTokensLeavesAllowedOrUnknown(t *testing.T) {
	tests := []struct {
		name  string
		model string
		body  []byte
		want  int64
	}{
		{
			name:  "allowed value",
			model: "gemini-3.1-pro-preview",
			body:  []byte(`{"generationConfig":{"maxOutputTokens":64000}}`),
			want:  64000,
		},
		{
			name:  "unknown model",
			model: "custom-gemini-model",
			body:  []byte(`{"generationConfig":{"maxOutputTokens":500000}}`),
			want:  500000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := capGeminiMaxOutputTokens(tt.body, tt.model)
			if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != tt.want {
				t.Fatalf("maxOutputTokens = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGeminiExecutorExecuteCapsMaxOutputTokensBeforeUpstream(t *testing.T) {
	var upstreamMaxOutputTokens int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		upstreamMaxOutputTokens = gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-pro-preview",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":500000}}`),
	}

	if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamMaxOutputTokens != 65536 {
		t.Fatalf("upstream maxOutputTokens = %d, want 65536", upstreamMaxOutputTokens)
	}
}

func TestGeminiExecutorExecuteReturnsRetryAfterFromRetryInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2.500s"}]}}`))
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.1-pro-preview",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}

	_, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if err == nil {
		t.Fatal("expected error")
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok {
		t.Fatalf("expected retryable status error, got %T", err)
	}
	got := retryable.RetryAfter()
	if got == nil {
		t.Fatal("expected retryAfter, got nil")
	}
	if *got != 2500*time.Millisecond {
		t.Fatalf("retryAfter = %v, want %v", *got, 2500*time.Millisecond)
	}
}
