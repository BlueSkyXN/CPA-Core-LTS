package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	executor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
)

type lifetimeFixtureExecutor struct {
	flowFixtureExecutor
	seen chan (<-chan struct{})
	fail bool
}

func (e *lifetimeFixtureExecutor) Identifier() string { return "codex" }

func (e *lifetimeFixtureExecutor) Execute(ctx context.Context, _ *Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	done := util.LogicalRequestDone(ctx)
	e.seen <- done
	lane, cancel := context.WithCancel(ctx)
	cancel()
	select {
	case <-util.LogicalRequestDone(lane):
		return executor.Response{}, errors.New("lane canceled logical request")
	default:
	}
	if e.fail {
		return executor.Response{}, errors.New("synthetic terminal failure")
	}
	return executor.Response{Payload: []byte("ok")}, nil
}
func (e *lifetimeFixtureExecutor) ExecuteStream(ctx context.Context, a *Auth, r executor.Request, o executor.Options) (*executor.StreamResult, error) {
	e.seen <- util.LogicalRequestDone(ctx)
	if e.fail {
		return nil, errors.New("synthetic terminal failure")
	}
	return e.flowFixtureExecutor.ExecuteStream(ctx, a, r, o)
}
func TestManagerLogicalRequestLifetime(t *testing.T) {
	for _, flow := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, fail := range []bool{false, true} {
				name := fmt.Sprintf("flow=%v/stream=%v/fail=%v", flow, stream, fail)
				t.Run(name, func(t *testing.T) {
					m := NewManager(nil, &FillFirstSelector{}, nil)
					e := &lifetimeFixtureExecutor{flowFixtureExecutor: flowFixtureExecutor{finish: make(chan struct{})}, seen: make(chan (<-chan struct{}), 16), fail: fail}
					m.RegisterExecutor(e)
					m.SetConfig(&internalconfig.Config{FlowControl: flowcontrol.Config{Enabled: flow}})
					a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: e.Identifier(), Status: StatusActive})
					if err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(a.ID, e.Identifier(), []*registry.ModelInfo{{ID: "flow-model"}})
					defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
					defer m.CloseFlowControl()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var done <-chan struct{}
					if stream {
						result, err := m.ExecuteStream(ctx, []string{e.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{Stream: true})
						if !fail && err != nil {
							t.Fatal(err)
						}
						done = <-e.seen
						if !fail {
							if _, ok := <-result.Chunks; !ok {
								t.Fatal("no delivered first chunk")
							}
							select {
							case <-done:
								t.Fatal("ended at StreamResult return")
							default:
							}
							close(e.finish)
							for range result.Chunks {
							}
						} else if result != nil {
							for range result.Chunks {
							}
						}
					} else {
						_, err := m.Execute(ctx, []string{e.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{})
						if (err != nil) != fail {
							t.Fatalf("unexpected execution error: %v", err)
						}
						done = <-e.seen
					}
					if done == nil {
						t.Fatal("missing logical lifetime")
					}
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("logical request not released")
					}
				})
			}
		}
	}
}

// 真实 conductor 并行重试分支必须继承同一个整次请求信号。
type lifetimeHedgedExecutor struct {
	*hedgedRetryTestExecutor
	seen chan (<-chan struct{})
}

func (e *lifetimeHedgedExecutor) Execute(ctx context.Context, a *Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	done := util.LogicalRequestDone(ctx)
	select {
	case <-done:
		return executor.Response{}, errors.New("logical lifetime ended before retry")
	default:
	}
	e.seen <- done
	return e.hedgedRetryTestExecutor.Execute(ctx, a, r, o)
}
func TestCodexCacheLifetimeHedgedRetry(t *testing.T) {
	base := &hedgedRetryTestExecutor{behaviors: map[string][]hedgedRetryBehavior{
		"lifetime-hedge": {{kind: "retry"}, {kind: "retry", delay: 20 * time.Millisecond}, {kind: "success", payload: "ok"}},
	}, maxRetries: 2, hedgeDelay: time.Millisecond}
	m, _ := newHedgedRetryTestManager(t, base, "lifetime-hedge")
	m.SetRetryConfig(0, 0, 0)
	e := &lifetimeHedgedExecutor{hedgedRetryTestExecutor: base, seen: make(chan (<-chan struct{}), 8)}
	m.RegisterExecutor(e)
	resp, err := m.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.5"}, executor.Options{})
	if err != nil || string(resp.Payload) != "ok" {
		t.Fatalf("hedged execution failed: %v", err)
	}
	if len(e.seen) != 3 {
		t.Fatalf("want initial and two hedged attempts, got %d", len(e.seen))
	}
	done := <-e.seen
	if done == nil {
		t.Fatal("no logical lifetime")
	}
	for len(e.seen) > 0 {
		if (<-e.seen) != done {
			t.Fatal("hedged attempt acquired a different lifetime")
		}
	}
	select {
	case <-done:
	default:
		t.Fatal("logical lifetime not ended after result")
	}
}

func TestCodexCacheLifetimeCredentialRetry(t *testing.T) {
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.SetConfig(&internalconfig.Config{})
	m.SetRetryConfig(0, 0, 0)
	var done <-chan struct{}
	var firstAuth string
	calls := 0
	e := &mockCustomErrorExecutor{identifier: "codex", executeFn: func(ctx context.Context, a *Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
		calls++
		current := util.LogicalRequestDone(ctx)
		if current == nil {
			t.Fatal("no lifetime")
		}
		select {
		case <-current:
			t.Fatal("lifetime ended between credentials")
		default:
		}
		if calls == 1 {
			done = current
			firstAuth = a.ID
			return executor.Response{}, customStatusError{code: 503, msg: "synthetic retry"}
		}
		if current != done || a.ID == firstAuth {
			t.Fatal("credential retry lost logical identity or did not change account")
		}
		return executor.Response{Payload: []byte("ok")}, nil
	}}
	m.RegisterExecutor(e)
	for _, id := range []string{"lifetime-account-a", "lifetime-account-b"} {
		if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
		defer registry.GetGlobalRegistry().UnregisterClient(id)
	}
	_, err := m.Execute(context.Background(), []string{"codex"}, executor.Request{Model: "gpt-5.5"}, executor.Options{})
	if err != nil || calls != 2 {
		t.Fatalf("credential retry failed: calls=%d err=%v", calls, err)
	}
	select {
	case <-done:
	default:
		t.Fatal("lifetime not released")
	}
}

func TestCodexCacheLifetimeOtherProvidersUnchanged(t *testing.T) {
	ctx := context.Background()
	next, finish := withCodexCacheLifetime(ctx, []string{"claude", "gemini"})
	if next != ctx || finish != nil || util.LogicalRequestDone(next) != nil {
		t.Fatal("other providers acquired Codex cache lifetime")
	}
}
