package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestCodexEmptyIncompletePreservesFailureUsage(t *testing.T) {
	cases := []struct {
		name   string
		usage  string
		input  int64
		cached int64
	}{
		{
			name:  "input-only",
			usage: `{"input_tokens":1000,"output_tokens":0,"total_tokens":1000}`,
			input: 1000,
		},
		{
			name:   "cached-input",
			usage:  `{"input_tokens":1000,"input_tokens_details":{"cached_tokens":600},"output_tokens":0,"total_tokens":1000}`,
			input:  1000,
			cached: 600,
		},
		{
			name:   "cached-input-without-total",
			usage:  `{"input_tokens":1000,"input_tokens_details":{"cached_tokens":600},"output_tokens":0}`,
			input:  1000,
			cached: 600,
		},
		{
			name:  "zero-tokens",
			usage: `{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"total_tokens":0}`,
		},
	}
	for _, transport := range []string{"http", "websocket"} {
		for _, mode := range []string{"non-stream", "stream", "bootstrap"} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/%s/%s", transport, mode, tc.name), func(t *testing.T) {
					terminal := fmt.Sprintf(`{"type":"response.incomplete","response":{"id":"synthetic","model":"gpt-5.5","status":"incomplete","service_tier":"priority","output":[],"usage":%s}}`, tc.usage)
					var serverURL string
					if transport == "http" {
						server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, terminal)
						t.Cleanup(server.Close)
						serverURL = server.URL
					} else {
						server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, terminal)
						t.Cleanup(server.Close)
						serverURL = server.URL
					}

					alias := uuid.NewString()
					capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 8)}
					coreusage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{}) })
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					t.Cleanup(cancel)
					ctx = coreusage.WithRequestedModelAlias(ctx, alias)
					ctx = coreusage.WithTraceID(ctx, alias)
					ctx = coreusage.WithStream(ctx, mode != "non-stream")
					auth := codexAPIKeyTestAuth(serverURL)
					auth.ID, auth.Index = "test-auth", "test-index"
					req, opts := codexWebsocketRequest()
					req.Model = "gpt-5.5"
					cfg := codexBufferingConfig(mode == "bootstrap")
					execute := NewCodexExecutor(cfg).Execute
					executeStream := NewCodexExecutor(cfg).ExecuteStream
					if transport == "websocket" {
						executor := NewCodexWebsocketsExecutor(cfg)
						execute, executeStream = executor.Execute, executor.ExecuteStream
					}

					var executeErr error
					if mode == "non-stream" {
						response, err := execute(ctx, auth, req, opts)
						executeErr = err
						if len(response.Payload) != 0 {
							t.Error("empty incomplete returned a successful response payload")
						}
					} else {
						opts.Stream = true
						result, err := executeStream(ctx, auth, req, opts)
						executeErr = err
						if transport == "http" && mode == "bootstrap" {
							if result != nil || err == nil {
								t.Error("HTTP bootstrap must reject empty incomplete before returning a stream")
							}
						} else if result == nil || err != nil {
							t.Errorf("expected an in-stream empty incomplete error, got result=%t error=%v", result != nil, err)
						}
						if result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									if executeErr != nil {
										t.Error("empty incomplete returned more than one error")
									}
									executeErr = chunk.Err
								}
							}
						}
					}
					if coded, ok := errors.AsType[interface {
						error
						StatusCode() int
					}](executeErr); !ok || coded.StatusCode() != http.StatusBadGateway {
						t.Errorf("empty incomplete error=%v, want 502", executeErr)
					}
					if scoped, ok := errors.AsType[cliproxyexecutor.RequestScopedError](executeErr); !ok || !scoped.IsRequestScoped() {
						t.Errorf("empty incomplete error=%v, want request-scoped", executeErr)
					}
					if executeErr == nil || executeErr.Error() != helps.CodexEmptyIncompleteStreamMessage {
						t.Errorf("empty incomplete error=%v, want existing error message", executeErr)
					}

					// The FIFO marker includes deferred failure reporting and completed stream cleanup.
					coreusage.PublishRecord(ctx, coreusage.Record{Alias: alias, Model: "dispatch-barrier"})
					record := capture.await(t)
					if !record.Failed || record.Fail.StatusCode != http.StatusBadGateway || record.Fail.Body != helps.CodexEmptyIncompleteStreamMessage {
						t.Errorf("failure outcome=%t %+v, want the empty incomplete 502", record.Failed, record.Fail)
					}
					if record.RequestID == "" || record.TraceID != alias || record.Alias != alias || record.AuthID != auth.ID || record.AuthIndex != auth.Index {
						t.Error("failure usage lost request or credential attribution")
					}
					if record.Model != req.Model || record.ResponseModel != "gpt-5.5" || record.Stream != opts.Stream {
						t.Errorf("failure attribution: model=%q responseModel=%q stream=%t", record.Model, record.ResponseModel, record.Stream)
					}
					want := coreusage.Detail{
						InputTokens:         tc.input,
						CachedTokens:        tc.cached,
						CacheReadTokens:     tc.cached,
						TotalTokens:         tc.input,
						TokenBreakdown:      coreusage.NewSubsetTokenBreakdown(tc.input, tc.cached, 0, 0, 0, tc.input),
						ResponseServiceTier: "priority",
					}
					if record.Detail != want {
						t.Errorf("failure detail=%+v, want %+v", record.Detail, want)
					}
					if record.ResponseServiceTier != "priority" || record.EffectiveServiceTier != "priority" {
						t.Error("failure usage lost upstream service tier")
					}
					if next := capture.await(t); next.Model != "dispatch-barrier" {
						t.Errorf("duplicate main failure record before barrier: model=%q", next.Model)
					}
				})
			}
		}
	}
}
