package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type schedulerProviderTestExecutor struct {
	provider string
}

func (e schedulerProviderTestExecutor) Identifier() string { return e.provider }

func (e schedulerProviderTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e schedulerProviderTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e schedulerProviderTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type blockingRefreshTestExecutor struct {
	schedulerProviderTestExecutor
	entered   chan struct{}
	release   chan struct{}
	active    int32
	maxActive int32
}

func (e *blockingRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	active := atomic.AddInt32(&e.active, 1)
	for {
		maxActive := atomic.LoadInt32(&e.maxActive)
		if active <= maxActive || atomic.CompareAndSwapInt32(&e.maxActive, maxActive, active) {
			break
		}
	}
	select {
	case e.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		atomic.AddInt32(&e.active, -1)
		return nil, ctx.Err()
	case <-e.release:
	}
	time.Sleep(10 * time.Millisecond)
	atomic.AddInt32(&e.active, -1)
	return auth, nil
}

type unauthorizedRefreshTestExecutor struct {
	schedulerProviderTestExecutor
}

func (e unauthorizedRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return nil, errors.New("token refresh failed with status 401: invalid_grant")
}

func TestManager_AuthRefreshJitterWindowReadsRuntimeConfig(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if got := manager.authRefreshJitterWindow(); got != 0 {
		t.Fatalf("authRefreshJitterWindow() = %s, want 0", got)
	}
	manager.SetConfig(&internalconfig.Config{AuthRefreshJitterMinutes: 7})
	if got := manager.authRefreshJitterWindow(); got != 7*time.Minute {
		t.Fatalf("authRefreshJitterWindow() = %s, want 7m", got)
	}
}

func TestManager_ProviderRefreshSemaphoreRespectsConfigBeforeExecutorRegistration(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{AuthMaxConcurrentRefreshPerProvider: 1})
	executor := &blockingRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
		entered:                       make(chan struct{}, 3),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	for _, id := range []string{"refresh-a", "refresh-b", "refresh-c"} {
		if _, errRegister := manager.Register(ctx, &Auth{
			ID:       id,
			Provider: "codex",
			Metadata: map[string]any{
				"email": id + "@example.com",
			},
		}); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}

	var wg sync.WaitGroup
	for _, id := range []string{"refresh-a", "refresh-b", "refresh-c"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			manager.refreshAuth(ctx, id)
		}()
	}
	select {
	case <-executor.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first refresh to enter")
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&executor.maxActive); got != 1 {
		t.Fatalf("max concurrent refreshes = %d, want 1", got)
	}
	if extra := len(executor.entered); extra != 0 {
		t.Fatalf("refreshes entered while semaphore was held = %d, want 0", extra)
	}

	close(executor.release)
	wg.Wait()
	if got := atomic.LoadInt32(&executor.maxActive); got != 1 {
		t.Fatalf("max concurrent refreshes after completion = %d, want 1", got)
	}
}

func TestManager_ProviderRefreshSemaphoreRebuildsOnConfigReload(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})
	manager.SetConfig(&internalconfig.Config{AuthMaxConcurrentRefreshPerProvider: 2})
	first := manager.providerRefreshSem("codex")
	if first == nil || cap(first) != 2 {
		t.Fatalf("providerRefreshSem cap = %d, want 2", cap(first))
	}

	manager.SetConfig(&internalconfig.Config{AuthMaxConcurrentRefreshPerProvider: 4})
	second := manager.providerRefreshSem("codex")
	if second == nil || cap(second) != 4 {
		t.Fatalf("providerRefreshSem cap = %d, want 4", cap(second))
	}
	if second == first {
		t.Fatal("expected config reload to rebuild provider refresh semaphore")
	}
}

func TestManager_RefreshAuthUnauthorizedFailureStopsAutoRefreshRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	auth := &Auth{
		ID:       "unauthorized-refresh",
		Provider: "codex",
		Metadata: map[string]any{
			"email": "x@example.com",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected unauthorized refresh failure to be recorded")
	}
	if got := updated.LastError.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("LastError.StatusCode() = %d, want %d", got, http.StatusUnauthorized)
	}
	if updated.LastError.Code != "unauthorized" {
		t.Fatalf("LastError.Code = %q, want unauthorized", updated.LastError.Code)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %s, want zero for unauthorized refresh failure", updated.NextRefreshAfter)
	}
	now := time.Now()
	if manager.shouldRefresh(updated, now) {
		t.Fatal("expected unauthorized auth to stop refresh attempts")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second, 0); shouldSchedule {
		t.Fatal("expected unauthorized auth to be removed from the auto-refresh schedule")
	}
}

func TestManager_RefreshSchedulerEntry_RebuildsSupportedModelSetAfterModelRegistration(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name  string
		prime func(*Manager, *Auth) error
	}{
		{
			name: "register",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				return errRegister
			},
		},
		{
			name: "update",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				if errRegister != nil {
					return errRegister
				}
				updated := auth.Clone()
				updated.Metadata = map[string]any{"updated": true}
				_, errUpdate := manager.Update(ctx, updated)
				return errUpdate
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{
				ID:       "refresh-entry-" + testCase.name,
				Provider: "gemini",
			}
			if errPrime := testCase.prime(manager, auth); errPrime != nil {
				t.Fatalf("prime auth %s: %v", testCase.name, errPrime)
			}

			registerSchedulerModels(t, "gemini", "scheduler-refresh-model", auth.ID)

			got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr == nil {
				t.Fatalf("pickSingle() before refresh error = %v, want auth_not_found", errPick)
			}
			if authErr.Code != "auth_not_found" {
				t.Fatalf("pickSingle() before refresh code = %q, want %q", authErr.Code, "auth_not_found")
			}
			if got != nil {
				t.Fatalf("pickSingle() before refresh auth = %v, want nil", got)
			}

			manager.RefreshSchedulerEntry(auth.ID)

			got, errPick = manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickSingle() after refresh error = %v", errPick)
			}
			if got == nil || got.ID != auth.ID {
				t.Fatalf("pickSingle() after refresh auth = %v, want %q", got, auth.ID)
			}
		})
	}
}

func TestManager_PickNext_RebuildsSchedulerAfterModelCooldownError(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	registerSchedulerModels(t, "gemini", "scheduler-cooldown-rebuild-model", "cooldown-stale-old")

	oldAuth := &Auth{
		ID:       "cooldown-stale-old",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, oldAuth); errRegister != nil {
		t.Fatalf("register old auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   oldAuth.ID,
		Provider: "gemini",
		Model:    "scheduler-cooldown-rebuild-model",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	newAuth := &Auth{
		ID:       "cooldown-stale-new",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, newAuth); errRegister != nil {
		t.Fatalf("register new auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(newAuth.ID, "gemini", []*registry.ModelInfo{{ID: "scheduler-cooldown-rebuild-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(newAuth.ID)
	})

	got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errPick, &cooldownErr) {
		t.Fatalf("pickSingle() before sync error = %v, want modelCooldownError", errPick)
	}
	if got != nil {
		t.Fatalf("pickSingle() before sync auth = %v, want nil", got)
	}

	got, executor, errPick := manager.pickNext(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if executor == nil {
		t.Fatal("pickNext() executor = nil")
	}
	if got == nil || got.ID != newAuth.ID {
		t.Fatalf("pickNext() auth = %v, want %q", got, newAuth.ID)
	}
}
