package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_RegisterCanonicalizesThinkingSuffixModelStates(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Now()
	laterRetry := now.Add(2 * time.Hour)

	registered, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "auth-thinking-states",
		Provider: "gemini",
		ModelStates: map[string]*ModelState{
			"gemini-3.1-pro-preview(high)": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(time.Hour),
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: now.Add(time.Hour),
					BackoffLevel:  1,
				},
				UpdatedAt: now,
			},
			"gemini-3.1-pro-preview(low)": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: laterRetry,
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: laterRetry,
					BackoffLevel:  2,
				},
				UpdatedAt: now.Add(time.Minute),
			},
		},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if len(registered.ModelStates) != 1 {
		t.Fatalf("len(ModelStates) = %d, want 1: %+v", len(registered.ModelStates), registered.ModelStates)
	}
	state := registered.ModelStates["gemini-3.1-pro-preview"]
	if state == nil || !state.Unavailable || !state.NextRetryAfter.Equal(laterRetry) {
		t.Fatalf("canonical model state = %+v, want unavailable until %v", state, laterRetry)
	}
	if state.Quota.BackoffLevel != 2 || !state.Quota.NextRecoverAt.Equal(laterRetry) {
		t.Fatalf("canonical model quota = %+v, want latest cooldown", state.Quota)
	}
}

func TestManager_Update_PreservesModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "test-model"
	backoffLevel := 7

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"k": "v"},
		ModelStates: map[string]*ModelState{
			model: {
				Quota: QuotaState{BackoffLevel: backoffLevel},
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errUpdate := m.Update(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"k": "v2"},
	}); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected ModelStates to be preserved")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Quota.BackoffLevel != backoffLevel {
		t.Fatalf("expected BackoffLevel to be %d, got %d", backoffLevel, state.Quota.BackoffLevel)
	}
}

func TestManager_Update_DisabledExistingDoesNotInheritModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register a disabled auth with existing ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-disabled",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 5},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Update with empty ModelStates — should NOT inherit stale states.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-disabled",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-disabled")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected disabled auth NOT to inherit ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_ActiveToDisabledDoesNotInheritModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register an active auth with ModelStates (simulates existing live auth).
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-a2d",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 9},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// File watcher deletes config → synthesizes Disabled=true auth → Update.
	// Even though existing is active, incoming auth is disabled → skip inheritance.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-a2d",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-a2d")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected active→disabled transition NOT to inherit ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_DisabledToActiveDoesNotInheritStaleModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register a disabled auth with stale ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-d2a",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 4},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Re-enable: incoming auth is active, existing is disabled → skip inheritance.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-d2a",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-d2a")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected disabled→active transition NOT to inherit stale ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_ActiveInheritsModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "active-model"
	backoffLevel := 3

	// Register an active auth with ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-active",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {
				Quota: QuotaState{BackoffLevel: backoffLevel},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Update with empty ModelStates — both sides active → SHOULD inherit.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-active",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-active")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected active auth to inherit ModelStates")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Quota.BackoffLevel != backoffLevel {
		t.Fatalf("expected BackoffLevel to be %d, got %d", backoffLevel, state.Quota.BackoffLevel)
	}
}

func TestManager_Update_ProviderChangeDoesNotInheritRuntimeState(t *testing.T) {
	m := NewManager(nil, nil, nil)
	staleRetry := time.Now().Add(time.Hour)
	staleRuntime := &struct{ provider string }{provider: "claude"}
	if _, err := m.Register(context.Background(), &Auth{
		ID: "auth-provider-change", Provider: "claude", Status: StatusActive,
		Runtime: staleRuntime, Success: 5, Failed: 3,
		ModelStates: map[string]*ModelState{
			"shared-model": {Unavailable: true, Status: StatusError, NextRetryAfter: staleRetry},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if _, err := m.Update(context.Background(), &Auth{
		ID: "auth-provider-change", Provider: "xai", Status: StatusError,
		StatusMessage: "stale provider error", Unavailable: true,
		Quota:           QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: staleRetry},
		LastError:       &Error{Code: "stale", Message: "stale provider error"},
		LastRefreshedAt: staleRetry.Add(-2 * time.Hour), NextRefreshAfter: staleRetry,
		NextRetryAfter: staleRetry, Runtime: staleRuntime, Success: 9, Failed: 7,
		ModelStates: map[string]*ModelState{
			"incoming-stale-model": {
				Unavailable: true, Status: StatusError, NextRetryAfter: staleRetry,
				Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: staleRetry},
			},
		},
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-provider-change")
	if !ok || updated == nil {
		t.Fatal("expected updated auth")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("provider change retained ModelStates: %+v", updated.ModelStates)
	}
	if updated.Status != StatusActive || updated.Unavailable || updated.StatusMessage != "" || updated.LastError != nil {
		t.Fatalf("provider change retained auth error state: %+v", updated)
	}
	if !reflect.DeepEqual(updated.Quota, QuotaState{}) || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("provider change retained quota/retry state: quota=%+v retry=%v", updated.Quota, updated.NextRetryAfter)
	}
	if !updated.LastRefreshedAt.IsZero() || !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("provider change retained refresh state: last=%v next=%v", updated.LastRefreshedAt, updated.NextRefreshAfter)
	}
	if updated.Success != 0 || updated.Failed != 0 || updated.Runtime != nil {
		t.Fatalf("provider change retained runtime state: success=%d failed=%d runtime=%#v", updated.Success, updated.Failed, updated.Runtime)
	}
}

func TestManager_Update_ProviderChangePersistsCooldownClear(t *testing.T) {
	ctx := context.Background()
	const authID = "auth-provider-change-persisted-cooldown"
	retryAfter := 30 * time.Minute
	store := &recordingCooldownStateStore{}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	if _, err := manager.Register(WithSkipPersist(ctx), &Auth{ID: authID, Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID: authID, Provider: "claude", Model: "shared-model",
		Error:      &Error{Code: "rate_limit", Message: "quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter: &retryAfter,
	})

	store.mu.Lock()
	before := cloneCooldownStateRecords(store.records)
	store.mu.Unlock()
	if len(before) == 0 {
		t.Fatal("provider cooldown was not persisted before provider change")
	}

	if _, err := manager.Update(ctx, &Auth{ID: authID, Provider: "xai", Status: StatusActive}); err != nil {
		t.Fatalf("update auth provider: %v", err)
	}

	store.mu.Lock()
	persistedAfterUpdate := cloneCooldownStateRecords(store.records)
	store.mu.Unlock()

	restartStore := &recordingCooldownStateStore{load: persistedAfterUpdate}
	restarted := NewManager(nil, nil, nil)
	restarted.SetCooldownStateStore(restartStore)
	if _, err := restarted.Register(WithSkipPersist(ctx), &Auth{ID: authID, Provider: "xai", Status: StatusActive}); err != nil {
		t.Fatalf("register auth after restart: %v", err)
	}
	if err := restarted.RestoreCooldownStates(ctx); err != nil {
		t.Fatalf("restore cooldown states: %v", err)
	}
	restored, ok := restarted.GetByID(authID)
	if !ok || restored == nil {
		t.Fatal("restarted auth missing")
	}
	if len(persistedAfterUpdate) != 0 {
		t.Fatalf("provider change left persisted cooldown records: %#v", persistedAfterUpdate)
	}
	if len(restored.ModelStates) != 0 || restored.Unavailable || restored.Quota.Exceeded || !restored.NextRetryAfter.IsZero() {
		t.Fatalf("old provider cooldown revived after restart: %+v", restored)
	}
}

func TestManager_ClearQuotaStateClearsQuotaModelAndPreservesOtherFailures(t *testing.T) {
	ctx := context.Background()
	const (
		authID        = "auth-clear-quota-state"
		quotaModel    = "codex-quota-reset-model"
		blockedModel  = "codex-unauthorized-model"
		quotaProvider = "codex"
	)
	registry.GetGlobalRegistry().RegisterClient(authID, quotaProvider, []*registry.ModelInfo{
		{ID: quotaModel},
		{ID: blockedModel},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	m := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := m.Register(ctx, &Auth{ID: authID, Provider: quotaProvider, Status: StatusActive}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: quotaProvider,
		Model:    quotaModel,
		Success:  false,
		Error:    &Error{Code: "rate_limit", Message: "quota exceeded", HTTPStatus: http.StatusTooManyRequests},
	})
	m.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: quotaProvider,
		Model:    blockedModel,
		Success:  false,
		Error:    &Error{Code: "unauthorized", Message: "unauthorized", HTTPStatus: http.StatusUnauthorized},
	})

	if _, errPick := m.scheduler.pickSingle(ctx, quotaProvider, quotaModel, cliproxyexecutor.Options{}, nil); errPick == nil {
		t.Fatal("expected quota model to be unavailable before clearing quota state")
	}

	if !m.ClearQuotaState(ctx, authID) {
		t.Fatal("expected ClearQuotaState to report a change")
	}

	picked, errPick := m.scheduler.pickSingle(ctx, quotaProvider, quotaModel, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("expected quota model to be routable after clearing quota state: %v", errPick)
	}
	if picked == nil || picked.ID != authID {
		t.Fatalf("picked auth = %+v, want %q", picked, authID)
	}

	updated, ok := m.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("expected auth after clearing quota state")
	}
	if state := updated.ModelStates[quotaModel]; state == nil || !modelStateIsClean(state) {
		t.Fatalf("expected quota model state to be clean, got %+v", state)
	}
	blockedState := updated.ModelStates[blockedModel]
	if blockedState == nil || blockedState.LastError == nil || blockedState.LastError.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized model state to remain, got %+v", blockedState)
	}
	if clearedAgain := registry.GetGlobalRegistry().ClearClientQuotaState(authID); clearedAgain != 0 {
		t.Fatalf("expected registry quota markers to be cleared, clearedAgain=%d", clearedAgain)
	}
}
