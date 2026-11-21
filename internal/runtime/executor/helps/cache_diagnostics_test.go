package helps

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestCacheUsageSurvivesDiagnosticEviction(t *testing.T) {
	for _, mode := range []string{"retained", "evicted", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			store := diagnostics.NewStore()
			ctx := t.Context()
			if mode != "disabled" {
				ctx = store.Begin(ctx, "eviction-fixture", "caller", "", "model", "responses", []byte(`{}`))
			}
			reporter := NewUsageReporter(ctx, "codex", "model", nil)
			reporter.ObserveUpstreamWebsocketRequestTo([]byte(`{"type":"response.create","input":[]}`), "wss://synthetic.invalid/v1/responses", "")
			reporter.ObserveWebsocketDiagnostics([]byte(`{"type":"response.created","response":{"usage":{"input_tokens":32,"input_tokens_details":{"cached_tokens":0}}}}`))
			if mode == "evicted" {
				for i := range diagnostics.MaxTraces {
					store.Begin(t.Context(), fmt.Sprintf("new-%d", i), "caller", "", "model", "responses", nil)
				}
			}
			if _, ok := store.Get(diagnostics.FromContext(ctx).ID()); ok != (mode == "retained") {
				t.Fatalf("unexpected trace retention for %s", mode)
			}
			reporter.ObserveWebsocketDiagnostics([]byte(`{"type":"response.completed","response":{"id":"resp_fixture","status":"completed","usage":{"input_tokens":32,"output_tokens":1,"input_tokens_details":{"cache_write_tokens":8}}}}`))
			record := reporter.buildRecord(usage.Detail{InputTokens: 32}, false)
			got := record.Detail.CacheObservation
			if got == nil || got.Read.State != "present" || got.Read.Value == nil || *got.Read.Value != 0 ||
				got.Write.State != "present" || got.Write.Value == nil || *got.Write.Value != 8 {
				t.Fatalf("incorrect usage after response: %+v", got)
			}
		})
	}
}

func TestCacheUsageDoesNotCarryReadsAcrossAttempts(t *testing.T) {
	reporter := NewUsageReporter(t.Context(), "codex", "model", nil)
	first := reporter.trackAttemptCacheUsage(nil, false)
	first.Observe([]byte(`{"usage":{"input_tokens_details":{"cached_tokens":12}}}`))
	reporter.ObserveUpstreamWebsocketRequestTo(nil, "wss://synthetic.invalid/v1/responses", "")
	reporter.ObserveWebsocketDiagnostics([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens_details":{"cache_write_tokens":3}}}}`))
	first.Observe([]byte(`{"usage":{"input_tokens_details":{"cached_tokens":11}}}`))
	got := reporter.buildRecord(usage.Detail{InputTokens: 20}, false).Detail.CacheObservation
	if got == nil || got.Read.State != "absent" || got.Write.Value == nil || *got.Write.Value != 3 {
		t.Fatalf("wire observations from another attempt reached usage: %+v", got)
	}
}

func TestCacheUsageRequiresCompleteSSEEvent(t *testing.T) {
	reporter := NewUsageReporter(t.Context(), "openai-compat", "model", nil)
	attempt := reporter.trackAttemptCacheUsage(nil, false)
	stream := "data: {\"usage\":{\"prompt_tokens_details\":{\"cached_tokens\":99}}}\n" +
		"data: {\"malformed_event\":true}\n\n" +
		"data: {\"usage\":{\n" +
		"data: \"prompt_tokens_details\":{\"cache_write_tokens\":3}}}\n\n"
	body := attempt.WrapResponse(io.NopCloser(strings.NewReader(stream)), "text/event-stream", "")
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	obs := reporter.buildRecord(usage.Detail{InputTokens: 20}, false).Detail.CacheObservation
	if obs == nil || obs.Read.State != "absent" || obs.Write.Value == nil || *obs.Write.Value != 3 {
		t.Fatalf("wrong SSE usage evidence: %+v", obs)
	}
}

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
		req.Header.Set("Session-Id", "separate-session")
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
	rows, _ := store.Lookup(diagnostics.TraceQuery{ID: reporter.requestID})
	if len(rows) != 1 {
		t.Fatalf("recorded attempts = %d", len(rows))
	}
	wire, err := json.Marshal(rows[0].CacheRoute)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"kind":"input_session"`, `"kind":"pck"`, `"kind":"outbound_session"`, `"attempt_count":2`} {
		if !bytes.Contains(wire, []byte(expected)) {
			t.Fatalf("execution summary missing %s", expected)
		}
	}
	for _, raw := range []string{"private-content", "private-key", "separate-session"} {
		if bytes.Contains(wire, []byte(raw)) {
			t.Fatalf("raw value in execution summary: %s", raw)
		}
	}
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

func TestCacheDiagnosticsProtocolWireEvidence(t *testing.T) {
	for _, tc := range []struct {
		provider, body, response, field, kind string
		read                                  int64
		breakpoints                           int
	}{
		{"claude", `{"messages":[{"role":"user","content":[{"type":"text","text":"fixture","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"tools":[{"name":"fixture","cache_control":{"type":"ephemeral"}}]}`,
			`{"type":"message","id":"msg_synthetic","usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":3,"cache_creation":{"ephemeral_1h_input_tokens":3,"ephemeral_5m_input_tokens":0}}}`, "cache_read_input_tokens", "", 0, 2},
		{"gemini", `{"contents":[]}`, `{"usageMetadata":{"promptTokenCount":90,"cachedContentTokenCount":42,"totalTokenCount":92}}`, "cachedContentTokenCount", "", 42, 0},
		{"gemini", `{"contents":[],"cachedContent":"cachedContents/synthetic"}`, `{"usageMetadata":{"promptTokenCount":90,"cachedContentTokenCount":42,"totalTokenCount":92}}`, "cachedContentTokenCount", "named_cache", 42, 0},
		{"xai", `{"input":[],"prompt_cache_key":"wire-fixture"}`, `{"object":"response","status":"completed","usage":{"input_tokens":32,"input_tokens_details":{"cached_tokens":8}}}`, "input_tokens_details.cached_tokens", "pck", 8, 0},
		{"openai-compat", `{"messages":[],"prompt_cache_key":"wire-fixture"}`, `{"object":"chat.completion","usage":{"prompt_tokens":32,"prompt_tokens_details":{"cached_tokens":7}}}`, "prompt_tokens_details.cached_tokens", "pck", 7, 0},
	} {
		t.Run(tc.provider+"/"+tc.kind, func(t *testing.T) {
			store := diagnostics.NewStore()
			ctx := store.Begin(t.Context(), "logical", "caller", "", "model", "fixture", []byte(tc.body))
			reporter := NewUsageReporter(ctx, tc.provider, "model", nil)
			client := reporter.TrackHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(tc.response)), Request: req}, nil
			})})
			request, _ := http.NewRequestWithContext(ctx, "POST", "http://synthetic.invalid/v1/generate", strings.NewReader(tc.body))
			resp, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.ReadAll(resp.Body); err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			record := reporter.buildRecord(usage.Detail{InputTokens: 90}, false)
			obs := record.Detail.CacheObservation
			if obs == nil || obs.Read.Source != tc.field || obs.Read.Value == nil || *obs.Read.Value != tc.read {
				t.Fatalf("observation %+v", obs)
			}
			trace, _ := store.Get(diagnostics.FromContext(ctx).ID())
			references := trace.Attempts[0].CacheReferences
			if tc.kind == "" && len(references) != 0 || tc.kind != "" && (len(references) != 1 || references[0].Kind != tc.kind || references[0].Scope == "") {
				t.Fatalf("unexpected references %+v", references)
			}
			if tc.breakpoints != 0 {
				control := trace.Attempts[0].CacheControl
				buckets := trace.Attempts[0].CacheWriteByTTL
				short, explicit := buckets["5m"]
				if buckets["1h"] != 3 || short != 0 || !explicit {
					t.Fatalf("cache write TTL %+v", buckets)
				}
				if control == nil || control.Breakpoints != tc.breakpoints || control.OneHour != 1 {
					t.Fatalf("cache control %+v", control)
				}
			}
		})
	}
}

func TestCacheDiagnosticsPluginWithoutWireEvidence(t *testing.T) {
	store := diagnostics.NewStore()
	ctx := store.Begin(t.Context(), "plugin-request", "caller", "", "plugin-model", "responses",
		[]byte(`{"input":"synthetic","prompt_cache_key":"unobserved-input-key"}`))
	reporter := NewUsageReporter(ctx, "fixture-plugin", "plugin-model", nil)
	rec := reporter.buildRecord(usage.Detail{InputTokens: 32, CachedTokens: 8}, false)
	trace, _ := store.Get(rec.DiagnosticsID)
	if len(trace.Attempts) != 0 || rec.Detail.CacheObservation != nil {
		t.Fatalf("plugin usage manufactured wire evidence: %+v", rec.Detail.CacheObservation)
	}
}
