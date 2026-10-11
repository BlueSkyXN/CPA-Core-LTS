package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const codexRequestSizeCompleted = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`

type codexRequestSizeExecutor interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func runCodexRequestSizeCase(t *testing.T, name string, executor codexRequestSizeExecutor, serverURL string, stream bool) usage.Record {
	t.Helper()
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin(name, recorder)
	t.Cleanup(func() { usage.RegisterNamedPlugin(name, noopUsagePlugin{}) })

	auth := codexAbnormalReasoningRetryTestAuth(serverURL)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5-sol",
		Payload: []byte(`{"model":"gpt-5.5-sol","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: stream}
	if stream {
		result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
		if err != nil {
			t.Fatalf("ExecuteStream: %v", err)
		}
		if _, err = drainChunks(result); err != nil {
			t.Fatalf("stream chunk: %v", err)
		}
	} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return recorder.waitForRecord(t, func(record usage.Record) bool {
		return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.5-sol"
	})
}

func TestCodexExecutorRecordsHTTPRequestSize(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "non-stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			responseBody := "event: response.completed\ndata: " + codexRequestSizeCompleted + "\n\n"
			var requestBytes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requestBytes.Store(int64(len(body)))
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(responseBody))
			}))
			defer server.Close()

			record := runCodexRequestSizeCase(t, "codex-request-size-http-"+name, NewCodexExecutor(&config.Config{}), server.URL, stream)
			size := record.Size
			if size.Version != usage.RequestSizeVersionV1 || size.UpstreamTransport != usage.UpstreamTransportHTTP {
				t.Fatalf("size version/transport = %d/%q, want 1/http", size.Version, size.UpstreamTransport)
			}
			if want := requestBytes.Load(); want == 0 || size.UpstreamRequestBytes != want || size.UpstreamRequestWireBytes != want {
				t.Fatalf("request bytes = %d wire = %d, want %d", size.UpstreamRequestBytes, size.UpstreamRequestWireBytes, want)
			}
			if size.UpstreamResponseBytes != int64(len(responseBody)) {
				t.Fatalf("response bytes = %d, want %d", size.UpstreamResponseBytes, len(responseBody))
			}
		})
	}
}

func TestCodexWebsocketsExecutorRecordsWebsocketRequestSize(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "non-stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			created := `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5-sol"}}`
			var requestBytes atomic.Int64
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade websocket: %v", err)
					return
				}
				defer func() { _ = conn.Close() }()
				_, message, err := conn.ReadMessage()
				if err != nil {
					t.Errorf("read websocket request: %v", err)
					return
				}
				requestBytes.Store(int64(len(message)))
				for _, frame := range []string{created, codexRequestSizeCompleted} {
					if err = conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
						t.Errorf("write websocket frame: %v", err)
						return
					}
				}
			}))
			defer server.Close()

			record := runCodexRequestSizeCase(t, "codex-request-size-ws-"+name, NewCodexWebsocketsExecutor(&config.Config{}), server.URL, stream)
			size := record.Size
			if size.Version != usage.RequestSizeVersionV1 || size.UpstreamTransport != usage.UpstreamTransportWebsocket {
				t.Fatalf("size version/transport = %d/%q, want 1/websocket", size.Version, size.UpstreamTransport)
			}
			if want := requestBytes.Load(); want == 0 || size.UpstreamRequestBytes != want || size.UpstreamRequestWireBytes != want {
				t.Fatalf("request bytes = %d wire = %d, want %d", size.UpstreamRequestBytes, size.UpstreamRequestWireBytes, want)
			}
			if want := int64(len(created) + len(codexRequestSizeCompleted)); size.UpstreamResponseBytes != want {
				t.Fatalf("response bytes = %d, want %d", size.UpstreamResponseBytes, want)
			}
		})
	}
}

func TestCodexExecutorRecordsPayloadAndWireSizeOfCompressedRequest(t *testing.T) {
	var wireBytes, payloadBytes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		wireBytes.Store(int64(len(raw)))
		if r.Header.Get("Content-Encoding") == "zstd" {
			decoder, err := zstd.NewReader(nil)
			if err != nil {
				t.Errorf("zstd reader: %v", err)
				return
			}
			decoded, err := decoder.DecodeAll(raw, nil)
			decoder.Close()
			if err != nil {
				t.Errorf("decode zstd body: %v", err)
				return
			}
			payloadBytes.Store(int64(len(decoded)))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: " + codexRequestSizeCompleted + "\n\n"))
	}))
	defer server.Close()

	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-request-size-compressed", recorder)
	t.Cleanup(func() { usage.RegisterNamedPlugin("codex-request-size-compressed", noopUsagePlugin{}) })

	cfg := &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		CodexKey:  []config.CodexKey{{APIKey: "k", BaseURL: server.URL, RequestCompression: config.CodexRequestCompressionZstd}},
	}
	auth := &cliproxyauth.Auth{ID: "codex-apikey-size", Provider: "codex", Attributes: map[string]string{"api_key": "k", "base_url": server.URL}}
	result, err := NewCodexExecutor(cfg).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("hello ", 400) + `","store":false}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if _, err = drainChunks(result); err != nil {
		t.Fatalf("stream chunk: %v", err)
	}
	record := recorder.waitForRecord(t, func(record usage.Record) bool { return record.AuthID == "codex-apikey-size" })
	size := record.Size
	if size.UpstreamRequestEncoding != "zstd" || size.UpstreamTransport != usage.UpstreamTransportHTTP {
		t.Fatalf("encoding/transport = %q/%q, want zstd/http", size.UpstreamRequestEncoding, size.UpstreamTransport)
	}
	if size.UpstreamRequestWireBytes != wireBytes.Load() || size.UpstreamRequestBytes != payloadBytes.Load() {
		t.Fatalf("payload/wire = %d/%d, want %d/%d", size.UpstreamRequestBytes, size.UpstreamRequestWireBytes, payloadBytes.Load(), wireBytes.Load())
	}
	if size.UpstreamRequestWireBytes >= size.UpstreamRequestBytes {
		t.Fatalf("wire %d not smaller than payload %d for a repetitive body", size.UpstreamRequestWireBytes, size.UpstreamRequestBytes)
	}
}
