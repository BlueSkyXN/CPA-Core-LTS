package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const codexCompressionCompletedSSE = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"

type codexCompressionCapture struct {
	encoding string
	decoded  []byte
}

func TestIsCodexBackendResponsesURL(t *testing.T) {
	cases := map[string]bool{
		"https://chatgpt.com/backend-api/codex/responses":              true,
		"https://chatgpt.com/backend-api/codex/responses/":             true,
		"https://gov.chatgpt.com/backend-api/codex/responses":          true,
		"https://chatgpt.com/backend-api/codex/responses/compact":      false,
		"http://chatgpt.com/backend-api/codex/responses":               false,
		"https://api.openai.com/v1/responses":                          false,
		"https://chatgpt.com.evil.example/backend-api/codex/responses": false,
	}
	for raw, want := range cases {
		if got := isCodexBackendResponsesURL(raw); got != want {
			t.Errorf("isCodexBackendResponsesURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestCodexRequestCompressionEnabled(t *testing.T) {
	const backend = "https://chatgpt.com/backend-api/codex/responses"
	oauth := &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{"access_token": "t"}}
	apiKeyAuth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "k", "base_url": "https://relay.example.com/v1"}}
	keyCfg := func(mode string) *config.Config {
		return &config.Config{CodexKey: []config.CodexKey{{APIKey: "k", BaseURL: "https://relay.example.com/v1", RequestCompression: mode}}}
	}
	cases := []struct {
		name string
		cfg  *config.Config
		auth *cliproxyauth.Auth
		url  string
		want bool
	}{
		{name: "oauth default auto compresses codex backend", cfg: &config.Config{}, auth: oauth, url: backend, want: true},
		{name: "oauth none disables", cfg: &config.Config{Codex: config.CodexConfig{Outbound: config.CodexOutboundConfig{RequestCompression: "none"}}}, auth: oauth, url: backend, want: false},
		{name: "oauth other host stays raw", cfg: &config.Config{}, auth: oauth, url: "https://relay.example.com/v1/responses", want: false},
		{name: "api key default stays raw", cfg: keyCfg(""), auth: apiKeyAuth, url: "https://relay.example.com/v1/responses", want: false},
		{name: "api key opt-in zstd compresses", cfg: keyCfg("zstd"), auth: apiKeyAuth, url: "https://relay.example.com/v1/responses", want: true},
		{name: "nil config stays raw", cfg: nil, auth: oauth, url: backend, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexRequestCompressionEnabled(tc.cfg, tc.auth, tc.url); got != tc.want {
				t.Fatalf("codexRequestCompressionEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func codexCompressionServer(t *testing.T, rejectZstd bool) (*httptest.Server, func() []codexCompressionCapture) {
	t.Helper()
	var mu sync.Mutex
	var captured []codexCompressionCapture
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		encoding := r.Header.Get("Content-Encoding")
		decoded := raw
		if encoding == "zstd" {
			if rejectZstd {
				mu.Lock()
				captured = append(captured, codexCompressionCapture{encoding: encoding})
				mu.Unlock()
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
			reader, err := zstd.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Errorf("zstd reader: %v", err)
				return
			}
			decoded, err = reader.DecodeAll(raw, nil)
			reader.Close()
			if err != nil {
				t.Errorf("decode zstd body: %v", err)
				return
			}
		}
		mu.Lock()
		captured = append(captured, codexCompressionCapture{encoding: encoding, decoded: decoded})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexCompressionCompletedSSE))
	}))
	t.Cleanup(server.Close)
	return server, func() []codexCompressionCapture {
		mu.Lock()
		defer mu.Unlock()
		return append([]codexCompressionCapture(nil), captured...)
	}
}

func runCodexAPIKeyStream(t *testing.T, serverURL, compression string) {
	t.Helper()
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		CodexKey:  []config.CodexKey{{APIKey: "k", BaseURL: serverURL, RequestCompression: compression}},
	}
	exec := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "k", "base_url": serverURL}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("hello ", 200) + `","store":false}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}
}

func TestCodexExecutorCompressesOptedInAPIKeyRequests(t *testing.T) {
	server, captured := codexCompressionServer(t, false)
	runCodexAPIKeyStream(t, server.URL, config.CodexRequestCompressionZstd)
	got := captured()
	if len(got) != 1 || got[0].encoding != "zstd" {
		t.Fatalf("captured = %+v, want one zstd request", got)
	}
	if model := gjson.GetBytes(got[0].decoded, "model").String(); model != "gpt-5.4" {
		t.Fatalf("decoded body model = %q, want gpt-5.4 (body must round-trip)", model)
	}
}

func TestCodexExecutorLeavesDefaultAPIKeyRequestsUncompressed(t *testing.T) {
	server, captured := codexCompressionServer(t, false)
	runCodexAPIKeyStream(t, server.URL, "")
	got := captured()
	if len(got) != 1 || got[0].encoding != "" {
		t.Fatalf("captured = %+v, want one uncompressed request", got)
	}
}

func TestCodexExecutorRetriesUncompressedAfter415(t *testing.T) {
	server, captured := codexCompressionServer(t, true)
	runCodexAPIKeyStream(t, server.URL, config.CodexRequestCompressionZstd)
	got := captured()
	if len(got) != 2 || got[0].encoding != "zstd" || got[1].encoding != "" {
		t.Fatalf("captured = %+v, want zstd attempt then uncompressed retry", got)
	}
	if model := gjson.GetBytes(got[1].decoded, "model").String(); model != "gpt-5.4" {
		t.Fatalf("retry body model = %q, want original body", model)
	}
}

func TestPrepareCodexOutboundHTTPRequestAcceptEncodingMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want bool
	}{{mode: "", want: true}, {mode: config.CodexAcceptEncodingLegacy, want: false}, {mode: config.CodexAcceptEncodingOfficial, want: true}} {
		cfg := &config.Config{Codex: config.CodexConfig{Outbound: config.CodexOutboundConfig{AcceptEncoding: tc.mode, RequestCompression: "none"}}}
		req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader([]byte(`{}`)))
		got, compressed := prepareCodexOutboundHTTPRequest(cfg, nil, req.URL.String(), req, []byte(`{}`), true)
		if compressed {
			t.Fatalf("mode %q: compressed with request-compression none", tc.mode)
		}
		if disabled := helps.AutoAcceptEncodingDisabled(got.Context()); disabled != tc.want {
			t.Fatalf("mode %q: auto Accept-Encoding disabled = %v, want %v", tc.mode, disabled, tc.want)
		}
	}
}
