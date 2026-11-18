package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type continuityFallbackQuotaError struct{ codexQuotaRejectionErr }

func (continuityFallbackQuotaError) ModelFallbackReason() string {
	return internalconfig.CodexModelFallbackTriggerUsageLimit
}

// A terminated plugin wait owns the whole request, including LTS model fallback.
func TestCodexContinuityTerminationDoesNotOpenLTSModelFallback(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Codex: internalconfig.CodexConfig{
				ModelFallback: internalconfig.CodexModelFallbackConfig{
					Enabled:  true,
					Mappings: []internalconfig.CodexModelFallbackMapping{{From: "gpt-source", To: []string{"gpt-target"}}},
				},
			}})
			manager.SetRetryConfig(1, 0, 0)
			clock := newFakeContinuityClock(time.Now())
			manager.SetContinuityClockForTest(clock)
			authID := "continuity-lts-" + name
			_, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive})
			if err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-source"}, {ID: "gpt-target"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			var calls []string
			fail := func(req cliproxyexecutor.Request) error {
				calls = append(calls, req.Model)
				return continuityFallbackQuotaError{codexQuotaRejectionErr{status: http.StatusTooManyRequests, msg: `{"error":{"code":"usage_limit_reached"}}`, credentialScoped: true}}
			}
			manager.RegisterExecutor(&continuityTestExecutor{
				provider: "codex",
				execFn: func(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, fail(req)
				},
				streamFn: func(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					return nil, fail(req)
				},
			})
			polls := 0
			manager.SetPluginScheduler(&testContinuityScheduler{hasContinuity: true,
				continuityFunc: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
					polls++
					if req.Phase != "pre_output_429" || req.RequestID == "" {
						t.Fatalf("unexpected phase/identity: %q / %q", req.Phase, req.RequestID)
					}
					return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 10, WaitBudgetMs: 20}, true, nil
				},
			})
			req := cliproxyexecutor.Request{Model: "gpt-source"}
			if stream {
				var result *cliproxyexecutor.StreamResult
				result, err = manager.ExecuteStream(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{Stream: true})
				if result != nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
			} else {
				_, err = manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
			}
			if !IsContinuityTerminatedError(err) || polls != 2 || len(calls) != 1 || calls[0] != "gpt-source" {
				t.Fatalf("termination=%v polls=%d calls=%v err=%v", IsContinuityTerminatedError(err), polls, calls, err)
			}
		})
	}
}

func TestCodexContinuityCandidatesRespectLTSExcludedAuthIDs(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
	for _, id := range []string{"continuity-lts-excluded", "continuity-lts-available"} {
		if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-scope"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExcludeAuthIDsMetadataKey: []string{"continuity-lts-excluded"}}}
	candidates := manager.buildCodexContinuityCandidates(context.Background(), []string{"codex"}, "gpt-scope", opts, 0, false, 0, time.Now())
	if len(candidates) != 1 || candidates[0].ID != "continuity-lts-available" {
		t.Fatalf("candidate exclusions lost: %+v", candidates)
	}
}

func TestProviderTransitionClearsContinuityCooldownKinds(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	original, err := manager.Register(context.Background(), &Auth{ID: "continuity-provider-transition", Provider: "codex", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	original.quotaForceCooldownUntil = time.Now().Add(time.Hour)
	original.nonQuotaCooldownUntil = time.Now().Add(time.Hour)
	original.Provider = "claude"
	updated, err := manager.Update(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.quotaForceCooldownUntil.IsZero() || !updated.nonQuotaCooldownUntil.IsZero() {
		t.Fatal("new provider inherited old continuity cooldown classification")
	}
}
