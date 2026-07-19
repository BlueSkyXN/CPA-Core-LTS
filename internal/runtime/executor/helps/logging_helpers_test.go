package helps

import (
	"bytes"
	"context"
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
