package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type deferredAPIRequestSnapshotter interface {
	SnapshotDeferredAPIRequests() []logging.DeferredAPIRequest
}

func snapshotDeferredAPIRequests(t *testing.T, value any) []logging.DeferredAPIRequest {
	t.Helper()
	switch source := value.(type) {
	case []logging.DeferredAPIRequest:
		return source
	case deferredAPIRequestSnapshotter:
		return source.SnapshotDeferredAPIRequests()
	default:
		t.Fatalf("deferred API request source type = %T", value)
		return nil
	}
}

func TestRequestLoggingDoesNotMarkUpstreamAttempt(t *testing.T) {
	tests := []struct {
		name   string
		record func(context.Context)
	}{
		{
			name: "HTTP",
			record: func(ctx context.Context) {
				RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{URL: "https://api.example.com", Method: http.MethodPost})
			},
		},
		{
			name: "websocket",
			record: func(ctx context.Context) {
				RecordAPIWebsocketRequest(ctx, &config.Config{}, UpstreamRequestLog{URL: "wss://api.example.com", Method: "WEBSOCKET"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
			test.record(ctx)
			if cliproxyexecutor.UpstreamAttempted(ctx) {
				t.Fatal("request logging marked an upstream attempt before transport")
			}
		})
	}
}

func TestRecordAPIRequestClonesDeferredBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	body := []byte(`{"model":"original"}`)

	RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   body,
	})
	body[10] = 'X'

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API request was not captured")
	}
	requests := snapshotDeferredAPIRequests(t, value)
	if len(requests) != 1 {
		t.Fatalf("deferred API requests = %#v, want one request", value)
	}
	captured := string(requests[0]())
	if !strings.Contains(captured, `{"model":"original"}`) {
		t.Fatalf("captured API request = %q, want original body", captured)
	}
}

func TestRecordAPIRequestRedactsCodexWorkspaceMetadataFromDeferredLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	body, headerMetadata := codexLoggingMetadataFixture(t)
	headers := http.Header{"X-Codex-Turn-Metadata": {headerMetadata}}
	originalBody := bytes.Clone(body)
	originalHeader := headers.Get("X-Codex-Turn-Metadata")

	RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
		URL:      "https://chatgpt.com/backend-api/codex/responses",
		Method:   http.MethodPost,
		Body:     body,
		Headers:  headers,
		Provider: "codex",
	})

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API request was not captured")
	}
	requests := snapshotDeferredAPIRequests(t, value)
	captured := string(requests[0]())
	if strings.Contains(captured, "credential-sentinel") || strings.Contains(captured, "/Users/private/project") || strings.Contains(captured, `"workspaces"`) {
		t.Fatalf("deferred Codex request log leaked workspace metadata: %s", captured)
	}
	if !bytes.Equal(body, originalBody) || headers.Get("X-Codex-Turn-Metadata") != originalHeader {
		t.Fatal("Codex log redaction mutated the outbound request")
	}
}

func TestRecordAPIWebsocketRequestRedactsCodexWorkspaceMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	body, headerMetadata := codexLoggingMetadataFixture(t)
	body, err := json.Marshal(map[string]any{
		"type": "response.create",
		"client_metadata": map[string]any{
			"x-codex-turn-metadata": headerMetadata,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	RecordAPIWebsocketRequest(ctx, &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}, UpstreamRequestLog{
		URL:      "wss://chatgpt.com/backend-api/codex/responses",
		Method:   "WEBSOCKET",
		Body:     body,
		Headers:  http.Header{"X-Codex-Turn-Metadata": {headerMetadata}},
		Provider: "codex",
	})

	value, exists := ginCtx.Get("API_WEBSOCKET_TIMELINE")
	if !exists {
		t.Fatal("websocket timeline was not captured")
	}
	timeline, ok := value.([]byte)
	if !ok {
		t.Fatalf("websocket timeline type = %T", value)
	}
	if strings.Contains(string(timeline), "credential-sentinel") || strings.Contains(string(timeline), "/Users/private/project") || strings.Contains(string(timeline), `"workspaces"`) {
		t.Fatalf("Codex websocket log leaked workspace metadata: %s", timeline)
	}
}

func codexLoggingMetadataFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	metadata := `{"thread_id":"thread-1","workspaces":{"/Users/private/project":{"associated_remote_urls":{"origin":"https://user:credential-sentinel@example.com/org/repo.git?token=credential-sentinel"},"latest_git_commit_hash":"abcdef","has_changes":false}}}`
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.6",
		"client_metadata": map[string]any{
			"x-codex-turn-metadata": metadata,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body, metadata
}

func TestRecordAPIRequestBoundsDeferredBodyWhenRequestLogDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	const safeDeferredBodyLimit = 1 << 20
	body := bytes.Repeat([]byte{0xaa}, safeDeferredBodyLimit+4096)

	RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
		URL:    "https://api.example.com/v1/responses",
		Method: http.MethodPost,
		Body:   body,
	})

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API request was not captured")
	}
	requests := snapshotDeferredAPIRequests(t, value)
	if len(requests) != 1 {
		t.Fatalf("deferred API requests = %#v, want one request", value)
	}
	captured := requests[0]()
	if got := bytes.Count(captured, []byte{0xaa}); got > safeDeferredBodyLimit {
		t.Fatalf("captured body bytes = %d, want at most %d", got, safeDeferredBodyLimit)
	}
	if !bytes.Contains(captured, []byte("API REQUEST BODY TRUNCATED")) {
		t.Fatalf("captured API request missing truncation marker: %q", captured[len(captured)-128:])
	}
}

func TestRecordAPIRequestConcurrentDeferredRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	const attempts = 200
	bodySize := maxDeferredAPIRequestBodyBytes/attempts + 1024
	body := bytes.Repeat([]byte("~"), bodySize)
	previousProcs := runtime.GOMAXPROCS(8)
	t.Cleanup(func() {
		runtime.GOMAXPROCS(previousProcs)
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			<-start
			RecordAPIRequest(ctx, &config.Config{}, UpstreamRequestLog{
				URL:    "https://api.example.com/v1/responses",
				Method: http.MethodPost,
				Body:   body,
			})
		}()
	}
	close(start)
	wg.Wait()

	value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	if !exists {
		t.Fatal("deferred API requests were not captured")
	}
	requests := snapshotDeferredAPIRequests(t, value)
	if len(requests) != attempts {
		t.Fatalf("retained %d deferred requests, want %d", len(requests), attempts)
	}

	seen := make(map[int]struct{}, attempts)
	capturedBodyBytes := 0
	for _, buildRequest := range requests {
		if buildRequest == nil {
			t.Fatal("deferred API request builder is nil")
		}
		payload := buildRequest()
		capturedBodyBytes += bytes.Count(payload, []byte("~"))
		var index int
		if _, errScan := fmt.Sscanf(string(payload), "=== API REQUEST %d ===", &index); errScan != nil {
			t.Fatalf("parse deferred request index: %v; payload=%q", errScan, payload)
		}
		if _, duplicate := seen[index]; duplicate {
			t.Fatalf("duplicate deferred request index %d", index)
		}
		seen[index] = struct{}{}
	}
	if len(seen) != attempts {
		t.Fatalf("unique deferred request indexes = %d, want %d", len(seen), attempts)
	}
	if capturedBodyBytes != maxDeferredAPIRequestBodyBytes {
		t.Fatalf("captured deferred request body bytes = %d, want cap %d", capturedBodyBytes, maxDeferredAPIRequestBodyBytes)
	}
}

func TestRecordAPIResponseMetadataStoresHeadersWhenRequestLogDisabled(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	headers := http.Header{}
	headers.Add("X-Upstream-Request-Id", "upstream-req-1")

	RecordAPIResponseMetadata(ctx, &config.Config{}, http.StatusOK, headers)
	headers.Set("X-Upstream-Request-Id", "mutated")

	got := logging.GetResponseHeaders(ctx)
	if got.Get("X-Upstream-Request-Id") != "upstream-req-1" {
		t.Fatalf("response header = %q, want %q", got.Get("X-Upstream-Request-Id"), "upstream-req-1")
	}
}

func TestAPIResponseAttemptsAreSeparated(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		fileBacked        bool
		firstResponseBody []byte
	}{
		{name: "memory backed error"},
		{name: "file backed error", fileBacked: true},
		{name: "memory backed partial body", firstResponseBody: []byte("partial")},
		{name: "file backed partial body", fileBacked: true, firstResponseBody: []byte("partial")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(recorder)
			var responseSource *logging.FileBodySource
			if tt.fileBacked {
				var errSource error
				responseSource, errSource = logging.NewFileBodySourceInDir(t.TempDir(), "api-response")
				if errSource != nil {
					t.Fatalf("NewFileBodySourceInDir: %v", errSource)
				}
				t.Cleanup(func() {
					if errCleanup := responseSource.Cleanup(); errCleanup != nil {
						t.Errorf("Cleanup: %v", errCleanup)
					}
				})
				ginCtx.Set(logging.APIResponseSourceContextKey, responseSource)
			}

			ctx := context.WithValue(context.Background(), "gin", ginCtx)
			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
			RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://api.example.com/first", Method: http.MethodPost})
			if len(tt.firstResponseBody) > 0 {
				AppendAPIResponseChunk(ctx, cfg, tt.firstResponseBody)
			} else {
				RecordAPIResponseError(ctx, cfg, errors.New("EOF"))
			}
			RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://api.example.com/second", Method: http.MethodPost})
			RecordAPIResponseError(ctx, cfg, errors.New("retry failed"))

			var response []byte
			if responseSource != nil {
				var errBytes error
				response, errBytes = responseSource.Bytes()
				if errBytes != nil {
					t.Fatalf("responseSource.Bytes: %v", errBytes)
				}
			} else {
				value, exists := ginCtx.Get(apiResponseKey)
				if !exists {
					t.Fatal("API_RESPONSE was not captured")
				}
				response, _ = value.([]byte)
			}

			previousEnd := "Error: EOF"
			if len(tt.firstResponseBody) > 0 {
				previousEnd = string(tt.firstResponseBody)
			}
			wantBoundary := previousEnd + "\n\n=== API RESPONSE 2 ==="
			if !strings.Contains(string(response), wantBoundary) {
				t.Fatalf("API response attempts are not separated by one blank line:\n%q\nwant boundary %q", response, wantBoundary)
			}
		})
	}
}
