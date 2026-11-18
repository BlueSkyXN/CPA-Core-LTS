package helps

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestCacheDiagnosticsHTTPRetryAndDecodedResponse(t *testing.T) {
	store := diagnostics.NewStore()
	ctx := store.Begin(context.Background(), "logical", "caller", "session", "model", "responses", []byte(`{"input":"private-content"}`))
	ctx = diagnostics.BeginSelection(ctx)
	diagnostics.Record(ctx, diagnostics.Event{Layer: "account", Method: "selection", Result: "selected", AuthIndex: "A"})
	reporter := NewUsageReporter(ctx, "codex", "model", nil)
	calls := 0
	client := reporter.TrackHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var compressed bytes.Buffer
		z := gzip.NewWriter(&compressed)
		_, _ = z.Write([]byte(`{"id":"resp_result","object":"response","status":"completed","model":"actual-model","usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0}},"prompt_cache_diagnostics":{"type":"unavailable"}}`))
		_ = z.Close()
		status := 200
		if calls == 1 {
			status = 503
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}, Body: io.NopCloser(bytes.NewReader(compressed.Bytes())), Request: req}, nil
	})})
	for i := 0; i < 2; i++ {
		var encoded bytes.Buffer
		z := gzip.NewWriter(&encoded)
		_, _ = z.Write([]byte(`{"input":"private-content","prompt_cache_key":"private-key"}`))
		_ = z.Close()
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/responses", bytes.NewReader(encoded.Bytes()))
		req.Header.Set("Content-Encoding", "gzip")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeResponseBody(res.Body, res.Header)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(decoded)
		_ = decoded.Close()
		if err != nil || !bytes.Contains(raw, []byte(`"resp_result"`)) {
			t.Fatalf("decoded body changed: %v", err)
		}
	}
	trace, _ := store.Get(diagnostics.FromContext(ctx).ID())
	if len(trace.Attempts) != 2 {
		t.Fatalf("attempts=%d", len(trace.Attempts))
	}
	for i, a := range trace.Attempts {
		if a.ExecutionID != reporter.requestID || a.DecisionID != trace.Events[0].DecisionID || !a.Request.Known || a.Request.Fields["prompt_cache_key"].Value != "" {
			t.Fatalf("bad attempt link/privacy %d: %+v", i, a)
		}
		if a.CacheObservation == nil || a.CacheObservation.Read.State != "present" || *a.CacheObservation.Read.Value != 0 || a.Diagnostic.Type != "unavailable" || a.Outcome != "body_consumed" || a.ResponseModel != "actual-model" {
			t.Fatalf("bad decoded observation %d: %+v", i, a)
		}
	}
	if trace.Attempts[0].Status != 503 || trace.Attempts[1].Status != 200 {
		t.Fatal("retry replaced first status")
	}
	record := reporter.buildRecord(usage.Detail{}, false)
	if record.DiagnosticsID != trace.ID || record.DiagnosticsInstanceID != trace.InstanceID {
		t.Fatal("usage lost instance/trace correlation")
	}
}

func TestCacheDiagnosticsWebsocketDone(t *testing.T) {
	for _, extra := range []string{"", `,"object":"response","usage":{"input_tokens":10,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}`} {
		store := diagnostics.NewStore()
		ctx := store.Begin(t.Context(), "request", "caller", "", "model", "codex", []byte(`{}`))
		reporter := NewUsageReporter(ctx, "codex", "model", nil)
		reporter.ObserveUpstreamWebsocketRequestTo([]byte(`{"type":"response.create","input":[]}`), "wss://api.openai.com/v1/responses", "")
		reporter.ObserveWebsocketDiagnostics([]byte(`{"type":"response.created","response":{"id":"resp_one","status":"in_progress"}}`))
		reporter.ObserveWebsocketDiagnostics([]byte(`{"type":"response.done","response":{"id":"resp_one","status":"completed"` + extra + `}}`))
		trace, _ := store.Get(diagnostics.FromContext(ctx).ID())
		attempt := trace.Attempts[0]
		if attempt.Outcome != "completed" || attempt.ObservationState != "observed" || attempt.ResponseStatus != "completed" {
			t.Fatalf("done did not finish the attempt: %+v", attempt)
		}
		// 通用模型观察不能把尚未归属的后续拒绝写入已完成的尝试。
		reporter.ObserveResponseModel([]byte(`{"type":"response.failed","response":{"id":"other","status":"failed"}}`))
		trace, _ = store.Get(diagnostics.FromContext(ctx).ID())
		if trace.Attempts[0].Outcome != "completed" || trace.Attempts[0].ResponseID != "resp_one" {
			t.Fatal("model observation changed diagnostic ownership")
		}
	}
}
