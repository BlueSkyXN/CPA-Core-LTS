package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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
