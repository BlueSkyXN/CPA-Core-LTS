package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func sizeTestResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func doSizeTestRequest(t *testing.T, client *http.Client, body string, encoding string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, err = io.ReadAll(resp.Body); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	_ = resp.Body.Close()
}

func TestUsageReporterRecordsHTTPRequestSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.Repeat("c", 40)))
	ginCtx.Request.Header.Set("Content-Encoding", "ZSTD")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	reporter := NewUsageReporter(ctx, "codex", "gpt-5.6", nil)
	responseBody := "data: {\"type\":\"response.completed\"}\n\n"
	client := reporter.TrackHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return sizeTestResponse(req, responseBody), nil
		}),
	})
	doSizeTestRequest(t, client, strings.Repeat("u", 128), "")

	got := reporter.buildRecord(usage.Detail{}, false).Size
	want := usage.RequestSize{
		Version:                  usage.RequestSizeVersionV1,
		ClientRequestBytes:       40,
		ClientRequestEncoding:    "zstd",
		UpstreamTransport:        usage.UpstreamTransportHTTP,
		UpstreamRequestBytes:     128,
		UpstreamRequestWireBytes: 128,
		UpstreamResponseBytes:    int64(len(responseBody)),
	}
	if got != want {
		t.Fatalf("size = %+v, want %+v", got, want)
	}
}

func TestUsageReporterRequestSizeDescribesFinalAttempt(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
	responses := []string{"first response body", "ok"}
	call := 0
	client := reporter.TrackHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := responses[call]
			call++
			return sizeTestResponse(req, body), nil
		}),
	})
	doSizeTestRequest(t, client, strings.Repeat("a", 300), "")
	doSizeTestRequest(t, client, strings.Repeat("b", 60), "zstd")

	got := reporter.buildRecord(usage.Detail{}, false).Size
	if got.UpstreamRequestWireBytes != 60 || got.UpstreamRequestEncoding != "zstd" {
		t.Fatalf("wire = %d encoding = %q, want 60 zstd", got.UpstreamRequestWireBytes, got.UpstreamRequestEncoding)
	}
	// An encoded body does not reveal its payload size at the transport.
	if got.UpstreamRequestBytes != 0 {
		t.Fatalf("payload bytes = %d, want unmeasured for encoded body", got.UpstreamRequestBytes)
	}
	if got.UpstreamResponseBytes != 2 {
		t.Fatalf("response bytes = %d, want 2 from the final attempt", got.UpstreamResponseBytes)
	}
}

func TestUsageReporterRecordsWebsocketRequestSize(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
	reporter.ObserveUpstreamWebsocketRequest(900)
	reporter.ObserveUpstreamResponseBytes(10)
	// A resend over a fresh connection replaces the first attempt.
	reporter.ObserveUpstreamWebsocketRequest(1000)
	reporter.ObserveUpstreamResponseBytes(25)
	reporter.ObserveUpstreamResponseBytes(5)

	got := reporter.buildRecord(usage.Detail{}, false).Size
	want := usage.RequestSize{
		Version:                  usage.RequestSizeVersionV1,
		UpstreamTransport:        usage.UpstreamTransportWebsocket,
		UpstreamRequestBytes:     1000,
		UpstreamRequestWireBytes: 1000,
		UpstreamResponseBytes:    30,
	}
	if got != want {
		t.Fatalf("size = %+v, want %+v", got, want)
	}
}

func TestUsageReporterRequestSizeUnmeasuredByDefault(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
	if got := reporter.buildRecord(usage.Detail{}, false).Size; got != (usage.RequestSize{}) {
		t.Fatalf("size = %+v, want zero value", got)
	}

	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	upgrade := NewUsageReporter(ctx, "codex", "gpt-5.6", nil)
	if got := upgrade.buildRecord(usage.Detail{}, false).Size; got != (usage.RequestSize{}) {
		t.Fatalf("bodyless request size = %+v, want zero value", got)
	}
}

func TestUsageReporterRequestSizeSkipsAdditionalModelRecords(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
	reporter.ObserveUpstreamWebsocketRequest(512)
	if got := reporter.buildRecordForModel("gpt-image-2", usage.Detail{}, false, usage.Failure{}).Size; got != (usage.RequestSize{}) {
		t.Fatalf("side-model size = %+v, want zero value", got)
	}
	if got := reporter.buildRecord(usage.Detail{}, false).Size.UpstreamRequestBytes; got != 512 {
		t.Fatalf("primary request bytes = %d, want 512", got)
	}
}

func TestUsageReporterRecordsPayloadSizeOfEncodedRequest(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
	responseBody := "ok"
	client := reporter.TrackHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return sizeTestResponse(req, responseBody), nil
		}),
	})
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", strings.NewReader(strings.Repeat("z", 30)))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Encoding", "zstd")
	req = req.WithContext(WithUpstreamPayloadBytes(req.Context(), 500))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	got := reporter.buildRecord(usage.Detail{}, false).Size
	if got.UpstreamRequestBytes != 500 || got.UpstreamRequestWireBytes != 30 || got.UpstreamRequestEncoding != "zstd" {
		t.Fatalf("size = %+v, want payload 500, wire 30, encoding zstd", got)
	}

	// An unencoded request ignores a stale payload size and reports its own body.
	reporter = NewUsageReporter(context.Background(), "codex", "gpt-5.6", nil)
	client = reporter.TrackHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return sizeTestResponse(req, responseBody), nil
		}),
	})
	req, _ = http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", strings.NewReader(strings.Repeat("u", 40)))
	req = req.WithContext(WithUpstreamPayloadBytes(req.Context(), 500))
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	got = reporter.buildRecord(usage.Detail{}, false).Size
	if got.UpstreamRequestBytes != 40 || got.UpstreamRequestWireBytes != 40 || got.UpstreamRequestEncoding != "" {
		t.Fatalf("size = %+v, want payload and wire 40 without encoding", got)
	}
}

func TestObserveUpstreamHTTPRequestKeepsFinalAttempt(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "zcode-coding-plan", "glm-5.3", nil)
	first, err := http.NewRequest(http.MethodPost, "https://example.invalid/client", strings.NewReader("handshake"))
	if err != nil {
		t.Fatal(err)
	}
	addFirst := reporter.ObserveUpstreamHTTPRequest(first)
	addFirst(len("handshake-response"))

	secondBody := strings.Repeat("m", 80)
	second, err := http.NewRequest(http.MethodPost, "https://example.invalid/messages", strings.NewReader(secondBody))
	if err != nil {
		t.Fatal(err)
	}
	second.Header.Set("Content-Encoding", "zstd")
	addSecond := reporter.ObserveUpstreamHTTPRequest(second)
	addFirst(999)
	addSecond(25)

	got := reporter.buildRecord(usage.Detail{}, false).Size
	want := usage.RequestSize{
		Version:                  usage.RequestSizeVersionV1,
		UpstreamTransport:        usage.UpstreamTransportHTTP,
		UpstreamRequestWireBytes: int64(len(secondBody)),
		UpstreamRequestEncoding:  "zstd",
		UpstreamResponseBytes:    25,
	}
	if got != want {
		t.Fatalf("size = %+v, want %+v", got, want)
	}
}
