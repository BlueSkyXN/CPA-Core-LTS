package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type partialUsageCallbackClient struct {
	staticEnvelopePluginClient
	callback func([]byte)
}

func (c partialUsageCallbackClient) Call(ctx context.Context, method string, raw []byte) ([]byte, error) {
	c.callback(raw)
	return c.staticEnvelopePluginClient.Call(ctx, method, raw)
}

func TestExecutorPartialUsageIsLocalToRPC(t *testing.T) {
	h := New()
	var outer executorPartialUsage
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), partialUsageContextKey{}, &outer))
	defer cancel()
	adapter := rpcPluginAdapter{id: "fixture", host: h, client: partialUsageCallbackClient{staticEnvelopePluginClient{[]byte(`{"ok":false,"error":{"code":"upstream_error","message":"outer"}}`)}, func(raw []byte) {
		var req rpcExecutorRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		cb, _, _, ok := h.callbackContexts.lookup(req.HostCallbackID)
		if !ok || cb.Done() != ctx.Done() {
			t.Fatal("lost callback ownership or cancellation")
		}
		_, err := callPlugin[rpcEmptyResponse](cb, staticEnvelopePluginClient{[]byte(`{"ok":false,"error":{"code":"upstream_error","message":"nested","partial_usage":{"total_tokens":9}}}`)}, pluginabi.MethodExecutorExecute, rpcEmptyResponse{})
		if err == nil || err.Error() != "nested" {
			t.Fatal("nested error lost")
		}
	}}}
	_, err := adapter.Execute(ctx, pluginapi.ExecutorRequest{})
	if err == nil || err.Error() != "outer" || outer.observed {
		t.Fatal("nested failure contaminated parent usage")
	}
}

func TestExecutorPartialUsagePreservesRPCFailure(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		observed  bool
	}{
		{"absent", "", false}, {"null", `null`, false}, {"empty", `{}`, false}, {"zero", `{"total_tokens":0}`, true},
		{"cache_zero", `{"input_tokens":8,"cache_read_tokens":0,"total_tokens":8}`, true},
		{"subset", `{"input_tokens":10,"output_tokens":2,"total_tokens":12,"cache_read_tokens":4,"cache_creation_tokens":1,"reasoning_tokens":1}`, true},
		{"partial", `{"total_tokens":8,"output_tokens":2}`, true}, {"fraction", `{"input_tokens":1.5}`, false}, {"negative", `{"output_tokens":-1}`, false},
		{"string", `{"input_tokens":"42"}`, false}, {"null_count", `{"total_tokens":null}`, false}, {"overflow", `{"total_tokens":9223372036854775808}`, false},
		{"sum_overflow", `{"input_tokens":9223372036854775807,"output_tokens":1}`, false}, {"cache_overflow", `{"cache_read_tokens":9223372036854775807,"cache_creation_tokens":1}`, false},
		{"excess_cache", `{"input_tokens":2,"cache_read_tokens":3}`, false}, {"excess_reasoning", `{"output_tokens":2,"reasoning_tokens":3}`, false}, {"inconsistent_zero", `{"input_tokens":1,"total_tokens":0}`, false},
		{"untrusted_attribution", `{"input_tokens":1,"source":"foreign"}`, false}, {"oversized", `{"total_tokens":` + strings.Repeat(" ", 1100) + `1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := ""
			if tc.raw != "" {
				field = `,"partial_usage":` + tc.raw
			}
			wire := []byte(`{"ok":false,"error":{"code":"credential_quota","message":"original failure","http_status":429,"retry_after_seconds":7,"credential_scoped":true` + field + `}}`)
			if !isPluginErrorEnvelope(wire) {
				t.Fatal("optional usage broke the error envelope")
			}
			for _, method := range []string{pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream, pluginabi.MethodExecutorCountTokens} {
				var p executorPartialUsage
				ctx := context.WithValue(context.Background(), partialUsageContextKey{}, &p)
				_, err := callPlugin[rpcEmptyResponse](ctx, staticEnvelopePluginClient{wire}, method, rpcEmptyResponse{})
				e, ok := err.(rpcError)
				if !ok || e.Error() != "original failure" || e.ErrorCode() != "credential_quota" || e.StatusCode() != http.StatusTooManyRequests || !e.IsCredentialScoped() || e.SafeResponseHeaders().Get("Retry-After") != "7" {
					t.Fatalf("original routing error changed: %v", err)
				}
				if p.observed != (tc.observed && method == pluginabi.MethodExecutorExecute) {
					t.Fatalf("%s: observed=%v", method, p.observed)
				}
				if tc.name == "subset" && p.observed && (p.detail.TotalTokens != 12 || p.detail.InputTokens != 10 || p.detail.OutputTokens != 2 || p.detail.CachedTokens != 4 || p.detail.CacheReadTokens != 4 || p.detail.CacheCreationTokens != 1 || p.detail.ReasoningTokens != 1 || !p.detail.TokenBreakdown.Valid()) {
					t.Fatal("lost token breakdown")
				}
				if p.observed && method == pluginabi.MethodExecutorExecute {
					switch tc.name {
					case "subset":
						if p.detail.CacheObservation == nil || p.detail.CacheObservation.Read.State != "present" || p.detail.CacheObservation.Read.Value == nil || *p.detail.CacheObservation.Read.Value != 4 || p.detail.CacheObservation.Write.State != "present" || p.detail.CacheObservation.Write.Value == nil || *p.detail.CacheObservation.Write.Value != 1 {
							t.Fatalf("unexpected subset cache observation: %+v", p.detail.CacheObservation)
						}
					case "zero":
						if p.detail.CacheObservation == nil || p.detail.CacheObservation.Read.State != "absent" || p.detail.CacheObservation.Write.State != "absent" {
							t.Fatalf("unexpected zero cache observation: %+v", p.detail.CacheObservation)
						}
					case "cache_zero":
						if p.detail.CacheObservation == nil || p.detail.CacheObservation.Read.State != "present" || p.detail.CacheObservation.Read.Value == nil || *p.detail.CacheObservation.Read.Value != 0 || p.detail.CacheObservation.Write.State != "absent" {
							t.Fatalf("unexpected cache_zero cache observation: %+v", p.detail.CacheObservation)
						}
					}
				}
			}
		})
	}
	wire := []byte(`{"ok":false,"error":{"code":"connection_lifecycle","message":"lost connection","retryable":true,"partial_usage":{"total_tokens":3}}}`)
	var p executorPartialUsage
	ctx := context.WithValue(context.Background(), partialUsageContextKey{}, &p)
	_, err := callPlugin[rpcEmptyResponse](ctx, staticEnvelopePluginClient{wire}, pluginabi.MethodExecutorExecute, rpcEmptyResponse{})
	var lifecycle *coreauth.Error
	if !errors.As(err, &lifecycle) || !lifecycle.Retryable || !p.observed || p.detail.TotalTokens != 3 {
		t.Fatal("lost lifecycle error or counts")
	}
}

func TestExecutorCanceledRPCSettlesPartialUsageOnce(t *testing.T) {
	for _, tc := range []struct {
		name, partial string
		wantTokens    int64
		provenance    string
		timeOut       bool
	}{
		{name: "reported", partial: `,"partial_usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}`, wantTokens: 9, provenance: coreusage.UsageProvenanceProviderReportedUnverified},
		{name: "reported zero", partial: `,"partial_usage":{"total_tokens":0}`, provenance: coreusage.UsageProvenanceProviderReportedUnverified},
		{name: "invalid", partial: `,"partial_usage":{"total_tokens":-1}`, provenance: coreusage.UsageProvenanceUnavailable},
		{name: "cancel budget elapsed", partial: `,"partial_usage":{"total_tokens":3}`, provenance: coreusage.UsageProvenanceUnavailable, timeOut: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const provider = "partial-usage-cancel"
			sink := newTestUsageCapturePlugin(provider)
			registerTestUsagePlugin(t, "test:partial-usage-cancel", sink)

			record := normalizeTestCapabilityRecord(capabilityRecord{id: "partial-usage-cancel-plugin"})
			host := newHostWithRecords(record)
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			wire := []byte(`{"ok":false,"error":{"code":"request_cancelled","message":"cancelled"` + tc.partial + `}}`)
			client := newGuardedPluginClient(partialUsageCallbackClient{staticEnvelopePluginClient{wire}, func([]byte) {
				close(started)
				<-release
			}})
			defer client.Shutdown()
			defer unblock()
			rpc := rpcPluginAdapter{id: record.id, host: host, client: client}
			executor := &fakeExecutor{identifier: provider, execute: rpc.Execute}
			formats := []sdktranslator.Format{sdktranslator.FormatOpenAI}
			adapter := newExecutorAdapterForRecordForTest(host, record, executor, formats, formats)
			adapter.provider = provider
			auth := &coreauth.Auth{ID: "fixture-auth", Index: "fixture-index", Provider: provider}
			req := coreexecutor.Request{Model: "fixture-model", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			returned := make(chan error, 1)
			go func() {
				_, err := adapter.Execute(ctx, auth, req, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
				returned <- err
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("RPC did not start")
			}
			cancel()
			select {
			case err := <-returned:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation waited for the plugin reply")
			}
			returnedAt := time.Now()
			if !tc.timeOut {
				unblock()
			}
			usage := sink.waitRecord(t, pluginExecutionLifecycleTimeout+2*time.Second)
			if !usage.Failed || usage.AuthIndex != auth.Index || usage.Detail.TotalTokens != tc.wantTokens || usage.UsageProvenance != tc.provenance {
				t.Fatalf("failure=%v auth=%s tokens=%d provenance=%s", usage.Failed, usage.AuthIndex, usage.Detail.TotalTokens, usage.UsageProvenance)
			}
			if usage.Latency <= 0 || usage.RequestedAt.Add(usage.Latency).After(returnedAt) {
				t.Fatalf("counted accounting wait as request latency: %v", usage.Latency)
			}
			unblock()
			client.Shutdown()
			select {
			case <-sink.records:
				t.Fatal("published more than one failure")
			default:
			}
		})
	}
}
