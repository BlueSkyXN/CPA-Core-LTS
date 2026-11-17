package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCodexContinuityAfterAbnormalRetryQuota(t *testing.T) {
	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		for _, path := range []string{"direct", "serial", "speed", "quality"} {
			cases := []string{"ready"}
			if path == "speed" || path == "quality" {
				cases = append(cases, "retry_exhausted", "wait_exhausted", "canceled", "transient", "disabled")
			}
			for _, scenario := range cases {
				t.Run(mode+"/"+path+"/"+scenario, func(t *testing.T) {
					ctx, cancel := context.WithCancel(WithRequestContinuitySession(context.Background()))
					defer cancel()
					manager := NewManager(nil, nil, nil)
					manager.SetRetryConfig(3, 0, 0)
					if scenario == "retry_exhausted" {
						manager.SetRetryConfig(1, 0, 0)
					}
					manager.SetContinuityClockForTest(newFakeContinuityClock(time.Now()))
					authID := "continuity-" + mode + "-" + path + "-" + scenario
					if _, err := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-continuity"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
					var calls atomic.Int32
					usage := coreusage.Detail{InputTokens: 10, OutputTokens: 20, ReasoningTokens: 16, TotalTokens: 30}
					nextError := func(a *Auth, opts cliproxyexecutor.Options) error {
						n := calls.Add(1)
						quotaAt := int32(1)
						if path != "direct" {
							quotaAt = 2
							if n == 1 {
								return hedgedRetryTestError{maxRetries: 1, hedgeEnabled: path != "serial", hedgeMode: path, authID: a.ID, usage: usage}
							}
						}
						if n == quotaAt {
							if scenario == "transient" {
								return codexQuotaRejectionErr{status: http.StatusTooManyRequests, msg: `{"error":{"code":"turn_rate_limit_exceeded"}}`, transient: true}
							}
							return codexQuotaRejectionErr{status: http.StatusTooManyRequests, msg: `{"error":{"code":"usage_limit_reached"}}`, credentialScoped: true}
						}
						if path != "direct" {
							acc, _ := opts.Metadata[cliproxyexecutor.CodexAbnormalReasoningRetryUsageMetadataKey].(*cliproxyexecutor.UsageAccumulator)
							if acc == nil || acc.Snapshot() != usage {
								t.Errorf("abnormal attempt usage was lost or counted twice: %v", acc)
							}
						}
						return nil
					}
					manager.RegisterExecutor(&continuityTestExecutor{
						provider: "codex",
						execFn: func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
							if err := nextError(a, opts); err != nil {
								return cliproxyexecutor.Response{}, err
							}
							return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
						},
						streamFn: func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
							if err := nextError(a, opts); err != nil {
								return nil, err
							}
							chunks := make(chan cliproxyexecutor.StreamChunk, 1)
							chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
							close(chunks)
							return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
						},
					})
					polls := 0
					manager.SetPluginScheduler(&testContinuityScheduler{
						hasContinuity: scenario != "disabled",
						continuityFunc: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
							polls++
							if req.Phase != "pre_output_429" || req.RequestID != "continuity-hedge-request" || req.Stream != stream {
								t.Errorf("continuity lost request context: %+v", req)
							}
							if scenario == "canceled" {
								cancel()
							}
							if polls == 1 || scenario == "wait_exhausted" {
								return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 10, WaitBudgetMs: 20}, true, nil
							}
							manager.mu.Lock()
							cur := manager.auths[authID]
							clearCooldownStateForAuth(cur, time.Now())
							cur.LastError = nil
							cur.Status = StatusActive
							snap := cur.Clone()
							manager.mu.Unlock()
							manager.scheduler.upsertAuth(snap)
							return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
						},
					})
					opts := cliproxyexecutor.Options{Stream: stream, Metadata: map[string]any{cliproxyexecutor.RequestIDMetadataKey: "continuity-hedge-request"}}
					request := cliproxyexecutor.Request{Model: "gpt-continuity"}
					var err error
					if stream {
						var result *cliproxyexecutor.StreamResult
						result, err = manager.ExecuteStream(ctx, []string{"codex"}, request, opts)
						if result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
								}
							}
						}
					} else {
						_, err = manager.Execute(ctx, []string{"codex"}, request, opts)
					}
					wantCalls, wantPolls := int32(2), 0
					switch scenario {
					case "ready":
						wantPolls = 2
						if path != "direct" {
							wantCalls = 3
						}
						if err != nil {
							t.Errorf("continuity failed to recover: %v", err)
						}
						if stream && continuitySessionFromContext(ctx).codexAttemptsUsed != int(wantCalls-1) {
							t.Errorf("stream bootstrap lost the spent retry budget: %d", continuitySessionFromContext(ctx).codexAttemptsUsed)
						}
					case "wait_exhausted", "retry_exhausted":
						if scenario == "wait_exhausted" {
							wantPolls = 2
						}
						if !IsContinuityTerminatedError(err) {
							t.Errorf("exhausted continuity must terminate: %v", err)
						}
					case "canceled":
						wantPolls = 1
						if !errors.Is(err, context.Canceled) {
							t.Errorf("cancellation lost: %v", err)
						}
					default:
						if statusCodeFromError(err) != http.StatusTooManyRequests || IsContinuityTerminatedError(err) {
							t.Errorf("ineligible continuity changed quota error: %v", err)
						}
					}
					if calls.Load() != wantCalls || polls != wantPolls {
						t.Errorf("calls=%d polls=%d, want %d/%d", calls.Load(), polls, wantCalls, wantPolls)
					}
				})
			}
		}
	}
}
