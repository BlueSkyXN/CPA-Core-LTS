package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpstreamErrorCode(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"numeric", `{"error":{"code":1301,"message":"private prompt"}}`, "1301"},
		{"string", `{"error":{"code":"1301"}}`, "1301"},
		{"root", `{"code":1308}`, "1308"},
		{"formatted message", `{"error":{"message":"[1301][private prompt][trace]"}}`, "1301"},
		{"type", `{"error":{"type":"overloaded_error"}}`, "overloaded_error"},
		{"code before type", `{"error":{"code":"1301","type":"api_error"}}`, "1301"},
		{"unknown code", `{"error":{"code":1399}}`, "1399"},
		{"unknown type", `{"error":{"type":"private-secret"}}`, ""},
		{"not a leading code", `{"error":{"message":"user said [1301][private]"}}`, ""},
		{"float", `{"error":{"code":1301.5}}`, ""},
		{"exponent", `{"error":{"code":1.301e3}}`, ""},
		{"credential", `{"error":{"code":"synthetic-key.synthetic-secret"}}`, ""},
		{"huge code", `{"error":{"code":"20261016211511"}}`, ""},
		{"html", `<html>1301 private prompt</html>`, ""},
		{"empty", ``, ""},
		{"truncated", `{"error":{"code":"1301"`, ""},
		{"too large", `{"error":{"code":1301,"message":"` + strings.Repeat("x", maxErrorBody) + `"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamErrorCode([]byte(tc.body)); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSSEBusinessErrorPreservesStatusAndSuppressesRawFrame(t *testing.T) {
	for _, source := range []string{"bigmodel", "zai"} {
		for _, frame := range []string{
			"event: error\ndata: {\"error\":{\"code\":1301,\"message\":\"private-prompt\"}}\n\n",
			"data: {\"type\":\"error\",\"error\":{\"code\":\"1301\"}}\n\n",
		} {
			var out bytes.Buffer
			p := sseParser{source: source, emit: func(b []byte) error { _, err := out.Write(b); return err }}
			var err error
			for _, b := range []byte(frame) {
				if err = p.feed([]byte{b}); err != nil {
					break
				}
			}
			if err == nil {
				t.Fatal("error event accepted")
			}
			failure := safeError(err)
			if failure.Status != 502 || rpcErrorCode(failure) != "upstream_error" || failure.FailureReason != source+"_1301" {
				t.Fatalf("failure = %+v", failure)
			}
			if out.Len() != 0 || strings.Contains(string(envelope(nil, err)), "private-prompt") {
				t.Fatal("raw error escaped")
			}
		}
	}
	p := sseParser{source: "bigmodel", emit: func([]byte) error { return nil }}
	if err := p.feed([]byte("data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"[1301][ordinary quoted text]\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")); err != nil {
		t.Fatal("normal model output classified as an error", err)
	}
}

type errorBodyHost struct {
	mu     sync.Mutex
	chunks []httpChunk
	closed chan struct{}
	once   sync.Once
	block  bool
	logs   []string
}

func (h *errorBodyHost) Call(method string, value any) (json.RawMessage, error) {
	switch method {
	case "host.http.stream_close":
		h.once.Do(func() { close(h.closed) })
		return encode(map[string]any{}), nil
	case "host.http.stream_read":
		if h.block {
			<-h.closed
			return nil, context.Canceled
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.chunks) == 0 {
			return encode(httpChunk{Done: true}), nil
		}
		chunk := h.chunks[0]
		h.chunks = h.chunks[1:]
		return encode(chunk), nil
	case "host.log":
		h.logs = append(h.logs, string(encode(value)))
		return encode(map[string]any{}), nil
	}
	return nil, context.Canceled
}

func newErrorBodyExecution(h *errorBodyHost) (*pluginRuntime, *execution) {
	return newRuntime(h), &execution{ctx: context.Background(), upstream: "error", signer: &signer{endpoint: upstreamEndpoint("bigmodel")}, req: executorRequest{CallbackID: "synthetic-callback"}}
}

func TestRejectedRequestDiagnostic(t *testing.T) {
	for _, status := range []int{400, 401, 429, 502, 307} {
		h := &errorBodyHost{closed: make(chan struct{}), chunks: []httpChunk{
			{Payload: []byte(`{"error":{"code":`)},
			{Payload: []byte(`1301,"message":"private-prompt api_key=synthetic-secret"}}`)},
		}}
		r, e := newErrorBodyExecution(h)
		err := r.rejectedRequest(e, httpStream{StatusCode: status, StreamID: "error", Headers: http.Header{"X-Request-Id": {"0123456789abcdef0123456789abcdef"}}}, "messages", "Upstream rejected the request")
		failure := safeError(err)
		want := status
		if status == 307 {
			want = 502
		}
		if failure.Status != want || failure.Code != "upstream_error" || failure.FailureReason != "bigmodel_1301" {
			t.Fatalf("failure = %+v", failure)
		}
		wire := string(envelope(nil, err)) + strings.Join(h.logs, "\n")
		if strings.Contains(wire, "private-prompt") || strings.Contains(wire, "synthetic-secret") || !strings.Contains(wire, "bigmodel_1301") {
			t.Fatal("diagnostic lost code or leaked raw content")
		}
		if len(h.logs) != 1 || !strings.Contains(h.logs[0], "synthetic-callback") || !strings.Contains(h.logs[0], "upstream_request_id=") {
			t.Fatal("missing correlated log")
		}
	}
}

func TestErrorBodyReadBoundAndCancellation(t *testing.T) {
	t.Run("size", func(t *testing.T) {
		h := &errorBodyHost{closed: make(chan struct{}), chunks: []httpChunk{{Payload: bytes.Repeat([]byte("x"), maxErrorBody+1)}}}
		r, e := newErrorBodyExecution(h)
		if body := r.readErrorBody(e, "error", time.Second); body != nil {
			t.Fatal("oversized body retained")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		h := &errorBodyHost{closed: make(chan struct{}), block: true}
		r, e := newErrorBodyExecution(h)
		done := make(chan []byte, 1)
		go func() { done <- r.readErrorBody(e, "error", 10*time.Millisecond) }()
		select {
		case body := <-done:
			if body != nil {
				t.Fatal("timed-out body retained")
			}
		case <-time.After(time.Second):
			t.Fatal("rejected response read was not bounded")
		}
		if e.ctx.Err() != nil {
			t.Fatal("error read canceled execution context")
		}
	})
}

type handshakeErrorHost struct {
	errorBodyHost
	status int
}

func (h *handshakeErrorHost) Call(method string, value any) (json.RawMessage, error) {
	if method == "host.http.do_stream" {
		return encode(httpStream{StatusCode: h.status, StreamID: "error"}), nil
	}
	return h.errorBodyHost.Call(method, value)
}

func TestHandshakeBusinessErrorKeepsRequestScopedClassification(t *testing.T) {
	for _, status := range []int{200, 403} {
		h := &handshakeErrorHost{status: status, errorBodyHost: errorBodyHost{
			closed: make(chan struct{}),
			chunks: []httpChunk{{Payload: []byte(`{"code":1301,"message":"private content","data":{"privateCipher":"private-key-material"}}`), Done: true}},
		}}
		r, e := newErrorBodyExecution(&h.errorBodyHost)
		r.caller = h
		e.signer.apiKey = "synthetic-key.synthetic-secret"
		_, err := e.signer.handshake(r, e)
		if err == nil {
			t.Fatal("handshake rejection accepted")
		}
		failure := safeError(err)
		if failure.Status != 502 || failure.Code != "signing_failed" || rpcErrorCode(failure) != "request_scoped" || failure.FailureReason != "bigmodel_1301" {
			t.Fatalf("failure = %+v", failure)
		}
		if len(h.logs) != 1 || strings.Contains(h.logs[0], "private") || strings.Contains(string(envelope(nil, err)), "private") {
			t.Fatal("handshake diagnostic missing or leaked private data")
		}
	}
}

func TestTraceIDRejectsArbitraryText(t *testing.T) {
	for _, value := range []string{"Bearer private-secret", "trace\nforged", strings.Repeat("a", 65), "private-device-value"} {
		if safeUpstreamTraceID(http.Header{"X-Request-Id": {value}}) != "" {
			t.Fatal("unsafe trace accepted")
		}
	}
}
