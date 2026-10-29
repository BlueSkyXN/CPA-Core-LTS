package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// codexDualTransportServer answers both the HTTP /responses SSE endpoint and the Responses
// websocket on one host, counting which transport each request used.
func codexDualTransportServer(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var httpHits, wsHits atomic.Int32
	var lastPath atomic.Value
	lastPath.Store("")
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath.Store(r.URL.Path)
		if websocket.IsWebSocketUpgrade(r) {
			wsHits.Add(1)
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade websocket: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				t.Errorf("read websocket request: %v", errRead)
				return
			}
			completed := `{"type":"response.completed","response":{"id":"resp-ws","object":"response","created_at":0,"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(completed)); errWrite != nil {
				t.Errorf("write websocket completed: %v", errWrite)
			}
			return
		}
		httpHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-http\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	t.Cleanup(server.Close)
	return server, &httpHits, &wsHits, &lastPath
}

func codexOutboundTestAuth(baseURL string, websockets bool) *cliproxyauth.Auth {
	attrs := map[string]string{"base_url": baseURL, "plan_type": "pro"}
	if websockets {
		attrs["websockets"] = "true"
	}
	return &cliproxyauth.Auth{
		ID:         "codex-outbound-test",
		Provider:   "codex",
		Attributes: attrs,
		Metadata:   map[string]any{"access_token": "test-token"},
	}
}

func TestCodexAutoExecutorOutboundTransportRouting(t *testing.T) {
	small := []byte(`{"model":"gpt-5.4","input":"hello","store":false}`)
	large := []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("x", 512) + `","store":false}`)
	cases := []struct {
		name       string
		transport  string
		websockets bool
		payload    []byte
		wantWS     bool
	}{
		{name: "default follow keeps HTTP", transport: "", websockets: true, payload: large, wantWS: false},
		{name: "explicit follow keeps HTTP", transport: config.CodexOutboundTransportFollow, websockets: true, payload: large, wantWS: false},
		{name: "websocket mode uses websocket", transport: config.CodexOutboundTransportWebsocket, websockets: true, payload: small, wantWS: true},
		{name: "websocket mode needs websocket-enabled auth", transport: config.CodexOutboundTransportWebsocket, websockets: false, payload: large, wantWS: false},
		{name: "auto below threshold stays HTTP", transport: config.CodexOutboundTransportAuto, websockets: true, payload: small, wantWS: false},
		{name: "auto at threshold uses websocket", transport: config.CodexOutboundTransportAuto, websockets: true, payload: large, wantWS: true},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			name := tc.name
			if stream {
				name += "/stream"
			}
			t.Run(name, func(t *testing.T) {
				server, httpHits, wsHits, _ := codexDualTransportServer(t)
				cfg := &config.Config{
					SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
					Codex: config.CodexConfig{Outbound: config.CodexOutboundConfig{
						Transport:               tc.transport,
						WebsocketThresholdBytes: 256,
					}},
				}
				exec := NewCodexAutoExecutor(cfg)
				auth := codexOutboundTestAuth(server.URL, tc.websockets)
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: tc.payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: stream}

				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatalf("ExecuteStream() error = %v", err)
					}
					sawCompleted := false
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk error = %v", chunk.Err)
						}
						if strings.Contains(string(chunk.Payload), "response.completed") {
							sawCompleted = true
						}
					}
					if !sawCompleted {
						t.Fatal("stream did not deliver response.completed to the HTTP client")
					}
				} else {
					resp, err := exec.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatalf("Execute() error = %v", err)
					}
					if len(resp.Payload) == 0 {
						t.Fatal("Execute() returned an empty payload")
					}
				}

				gotWS, gotHTTP := wsHits.Load(), httpHits.Load()
				if tc.wantWS && (gotWS != 1 || gotHTTP != 0) {
					t.Fatalf("transport hits ws=%d http=%d, want websocket only", gotWS, gotHTTP)
				}
				if !tc.wantWS && (gotHTTP != 1 || gotWS != 0) {
					t.Fatalf("transport hits ws=%d http=%d, want HTTP only", gotWS, gotHTTP)
				}
			})
		}
	}
}

func TestCodexAutoExecutorOutboundWebsocketKeepsCompactOnHTTP(t *testing.T) {
	server, httpHits, wsHits, lastPath := codexDualTransportServer(t)
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		Codex:     config.CodexConfig{Outbound: config.CodexOutboundConfig{Transport: config.CodexOutboundTransportWebsocket}},
	}
	exec := NewCodexAutoExecutor(cfg)
	auth := codexOutboundTestAuth(server.URL, true)
	_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"compact me"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Alt: "responses/compact"})
	if wsHits.Load() != 0 || httpHits.Load() != 1 {
		t.Fatalf("compact transport hits ws=%d http=%d, want HTTP only", wsHits.Load(), httpHits.Load())
	}
	if got, _ := lastPath.Load().(string); got != "/responses/compact" {
		t.Fatalf("compact path = %q, want /responses/compact", got)
	}
}
