package auth

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestUpdateAggregatedAvailability_UnavailableWithoutNextRetryDoesNotBlockAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if auth.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
}

func TestModelAvailabilityExpiredRequiresPastResettableDeadline(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	tests := []struct {
		name  string
		state *ModelState
		want  bool
	}{
		{name: "past generic cooldown", state: &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: past, LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"}}, want: true},
		{name: "past quota", state: &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: past, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: past}, LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"}}, want: true},
		{name: "future deadline", state: &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: future, LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"}}},
		{name: "past and future", state: &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: past, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future}}},
		{name: "zero deadline", state: &ModelState{Status: StatusError, Unavailable: true, LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}}},
		{name: "disabled model", state: &ModelState{Status: StatusDisabled, Unavailable: true, NextRetryAfter: past}},
		{name: "cloudflare challenge", state: &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: past, Quota: QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: past}, LastError: &Error{HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelAvailabilityExpired(tt.state, now); got != tt.want {
				t.Fatalf("modelAvailabilityExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthAvailabilityExpiredRequiresPastResettableDeadline(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	tests := []struct {
		name string
		auth *Auth
		want bool
	}{
		{name: "past generic cooldown", auth: &Auth{Status: StatusError, Unavailable: true, NextRetryAfter: past, LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"}}, want: true},
		{name: "future quota", auth: &Auth{Status: StatusError, Unavailable: true, NextRetryAfter: future, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future}}},
		{name: "zero deadline", auth: &Auth{Status: StatusError, Unavailable: true, Quota: QuotaState{Exceeded: true, Reason: "quota"}}},
		{name: "disabled auth", auth: &Auth{Disabled: true, Status: StatusError, Unavailable: true, NextRetryAfter: past}},
		{name: "disabled status", auth: &Auth{Status: StatusDisabled, Unavailable: true, NextRetryAfter: past}},
		{name: "cloudflare challenge", auth: &Auth{Status: StatusError, StatusMessage: "cloudflare challenge", Unavailable: true, NextRetryAfter: past, Quota: QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: past}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authAvailabilityExpired(tt.auth, now); got != tt.want {
				t.Fatalf("authAvailabilityExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestManagerPruneExpiredAvailabilityKeepsFutureModelState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cooldownStore := &recordingCooldownStateStore{}
	manager.SetCooldownStateStore(cooldownStore)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	authID := "prune-mixed-auth"
	expiredModel := "prune-expired-model"
	futureModel := "prune-future-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: expiredModel}, {ID: futureModel}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	reg.SetModelQuotaExceeded(authID, expiredModel)
	reg.SuspendClientModel(authID, expiredModel, "quota")
	reg.SetModelQuotaExceeded(authID, futureModel)
	reg.SuspendClientModel(authID, futureModel, "quota")

	auth := &Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: past,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: past},
		LastError:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
		ModelStates: map[string]*ModelState{
			expiredModel: {Status: StatusError, StatusMessage: "quota exhausted", Unavailable: true, NextRetryAfter: past, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: past}, LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}},
			futureModel:  {Status: StatusError, StatusMessage: "quota exhausted", Unavailable: true, NextRetryAfter: future, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future}, LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}},
		},
	}
	if _, err := manager.Register(WithSkipPersist(ctx), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if got := manager.PruneExpiredAvailability(ctx, now); got != 1 {
		t.Fatalf("PruneExpiredAvailability() = %d, want 1", got)
	}
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("updated auth missing")
	}
	if state := updated.ModelStates[expiredModel]; state == nil || !modelStateIsClean(state) {
		t.Fatalf("expired model state = %#v, want clean", state)
	}
	futureState := updated.ModelStates[futureModel]
	if futureState == nil || !futureState.Unavailable || !futureState.NextRetryAfter.Equal(future) || !futureState.Quota.Exceeded {
		t.Fatalf("future model state = %#v, want preserved", futureState)
	}
	if updated.Status != StatusError || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("auth status/unavailable/retry = %q/%v/%v, want error/false/zero while one model is available", updated.Status, updated.Unavailable, updated.NextRetryAfter)
	}
	if count := reg.GetModelCount(expiredModel); count != 1 {
		t.Fatalf("expired model registry count = %d, want 1", count)
	}
	if count := reg.GetModelCount(futureModel); count != 0 {
		t.Fatalf("future model registry count = %d, want 0", count)
	}
	if got := cooldownStore.saveCount.Load(); got != 1 {
		t.Fatalf("cooldown store save count = %d, want 1", got)
	}
	cooldownStore.mu.Lock()
	records := cloneCooldownStateRecords(cooldownStore.records)
	cooldownStore.mu.Unlock()
	if len(records) != 1 || records[0].Model != futureModel {
		t.Fatalf("cooldown records = %#v, want only future model", records)
	}
}

func TestManagerPruneExpiredAvailabilityFullyRecoversAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cooldownStore := &recordingCooldownStateStore{}
	manager.SetCooldownStateStore(cooldownStore)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-time.Minute)
	authID := "prune-clean-auth"
	model := "prune-clean-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	reg.SetModelQuotaExceeded(authID, model)
	reg.SuspendClientModel(authID, model, "quota")

	auth := &Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "usage_limit_reached",
		Unavailable:    true,
		NextRetryAfter: past,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: past, BackoffLevel: 2},
		LastError:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"},
		ModelStates: map[string]*ModelState{
			model: {Status: StatusError, StatusMessage: "usage_limit_reached", Unavailable: true, NextRetryAfter: past, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: past, BackoffLevel: 2}, LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"}},
		},
	}
	if _, err := manager.Register(WithSkipPersist(ctx), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if got := manager.PruneExpiredAvailability(ctx, now); got != 1 {
		t.Fatalf("PruneExpiredAvailability() = %d, want 1", got)
	}
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("updated auth missing")
	}
	if updated.Status != StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() || updated.LastError != nil || !quotaStateIsEmpty(updated.Quota) {
		t.Fatalf("updated auth = %#v, want fully active", updated)
	}
	if state := updated.ModelStates[model]; state == nil || !modelStateIsClean(state) {
		t.Fatalf("model state = %#v, want clean", state)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count = %d, want 1", count)
	}
	if got := cooldownStore.saveCount.Load(); got != 1 {
		t.Fatalf("cooldown store save count = %d, want 1", got)
	}
	cooldownStore.mu.Lock()
	defer cooldownStore.mu.Unlock()
	if len(cooldownStore.records) != 0 {
		t.Fatalf("cooldown records = %#v, want empty", cooldownStore.records)
	}
}

func TestManagerPruneExpiredAvailabilityClearsExpiredModelAggregate(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-time.Minute)
	authID := "prune-aggregate-auth"
	expiredModel := "prune-aggregate-expired"
	cleanModel := "prune-aggregate-clean"

	auth := &Auth{
		ID:            authID,
		Provider:      "claude",
		Status:        StatusError,
		StatusMessage: "EOF",
		Unavailable:   false,
		LastError:     &Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"},
		ModelStates: map[string]*ModelState{
			expiredModel: {Status: StatusError, StatusMessage: "EOF", Unavailable: true, NextRetryAfter: past, LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"}},
			cleanModel:   {Status: StatusActive},
		},
	}
	if _, err := manager.Register(WithSkipPersist(ctx), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if got := manager.PruneExpiredAvailability(ctx, now); got != 1 {
		t.Fatalf("PruneExpiredAvailability() = %d, want 1", got)
	}
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("updated auth missing")
	}
	if updated.Status != StatusActive || updated.StatusMessage != "" || updated.Unavailable || updated.LastError != nil || !updated.NextRetryAfter.IsZero() || !quotaStateIsEmpty(updated.Quota) {
		t.Fatalf("auth aggregate = %#v, want fully cleared", updated)
	}
	if state := updated.ModelStates[expiredModel]; state == nil || !modelStateIsClean(state) {
		t.Fatalf("expired model state = %#v, want clean", state)
	}
	if state := updated.ModelStates[cleanModel]; state == nil || !modelStateIsClean(state) {
		t.Fatalf("clean model state = %#v, want unchanged clean", state)
	}
}

func TestManagerPruneExpiredAvailabilityPreservesIndependentAuthState(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	tests := []struct {
		name string
		auth *Auth
	}{
		{
			name: "future cooldown",
			auth: &Auth{
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: future,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future, BackoffLevel: 2},
				LastError:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
			},
		},
		{
			name: "zero deadline unauthorized",
			auth: &Auth{
				Status:        StatusError,
				StatusMessage: "unauthorized",
				Unavailable:   true,
				LastError:     &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
			},
		},
		{
			name: "cloudflare challenge",
			auth: &Auth{
				Status:         StatusError,
				StatusMessage:  "cloudflare challenge",
				Unavailable:    true,
				NextRetryAfter: past,
				Quota:          QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: past, BackoffLevel: 3},
				LastError:      &Error{HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge"},
			},
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			cooldownStore := &recordingCooldownStateStore{}
			manager.SetCooldownStateStore(cooldownStore)
			ctx := context.Background()
			authID := fmt.Sprintf("prune-independent-%d", index)
			model := fmt.Sprintf("prune-independent-model-%d", index)
			tt.auth.ID = authID
			tt.auth.Provider = "claude"
			tt.auth.ModelStates = map[string]*ModelState{
				model: {Status: StatusError, StatusMessage: "EOF", Unavailable: true, NextRetryAfter: past, LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "EOF"}},
			}

			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			reg.SuspendClientModel(authID, model, "transient")
			if _, err := manager.Register(WithSkipPersist(ctx), tt.auth); err != nil {
				t.Fatalf("register auth: %v", err)
			}
			before, ok := manager.GetByID(authID)
			if !ok || before == nil {
				t.Fatal("registered auth missing")
			}

			if got := manager.PruneExpiredAvailability(ctx, now); got != 1 {
				t.Fatalf("PruneExpiredAvailability() = %d, want 1", got)
			}
			updated, ok := manager.GetByID(authID)
			if !ok || updated == nil {
				t.Fatal("updated auth missing")
			}
			if updated.Status != before.Status || updated.StatusMessage != before.StatusMessage || updated.Unavailable != before.Unavailable || !updated.NextRetryAfter.Equal(before.NextRetryAfter) || !reflect.DeepEqual(updated.Quota, before.Quota) || !reflect.DeepEqual(updated.LastError, before.LastError) {
				t.Fatalf("auth availability changed:\n got  %#v\n want %#v", updated, before)
			}
			if state := updated.ModelStates[model]; state == nil || !modelStateIsClean(state) {
				t.Fatalf("expired model state = %#v, want clean", state)
			}
			if count := reg.GetModelCount(model); count != 1 {
				t.Fatalf("registry model count = %d, want 1", count)
			}

			cooldownStore.mu.Lock()
			records := cloneCooldownStateRecords(cooldownStore.records)
			cooldownStore.mu.Unlock()
			if tt.name == "future cooldown" {
				if len(records) != 1 || records[0].Model != "" || !records[0].NextRetryAfter.Equal(future) {
					t.Fatalf("cooldown records = %#v, want preserved auth-level record", records)
				}
			}
		})
	}
}

func TestManagerPruneExpiredAvailabilityPreservesDisabledModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	now := time.Now()
	past := now.Add(-time.Minute)
	authID := "prune-disabled-model-auth"
	expiredModel := "prune-disabled-expired"
	disabledModel := "prune-disabled-preserved"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: expiredModel}, {ID: disabledModel}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	reg.SuspendClientModel(authID, expiredModel, "quota")
	reg.SuspendClientModel(authID, disabledModel, "quota")

	auth := &Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: past,
		ModelStates: map[string]*ModelState{
			expiredModel:  {Status: StatusError, Unavailable: true, NextRetryAfter: past, LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}},
			disabledModel: {Status: StatusDisabled, Unavailable: true, NextRetryAfter: past},
		},
	}
	if _, err := manager.Register(WithSkipPersist(ctx), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if got := manager.PruneExpiredAvailability(ctx, now); got != 1 {
		t.Fatalf("PruneExpiredAvailability() = %d, want 1", got)
	}
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("updated auth missing")
	}
	if updated.Status != StatusError {
		t.Fatalf("auth status = %q, want error while disabled model remains", updated.Status)
	}
	if state := updated.ModelStates[disabledModel]; state == nil || state.Status != StatusDisabled || !state.Unavailable || !state.NextRetryAfter.Equal(past) {
		t.Fatalf("disabled model state = %#v, want preserved", state)
	}
	if count := reg.GetModelCount(expiredModel); count != 1 {
		t.Fatalf("expired model registry count = %d, want 1", count)
	}
	if count := reg.GetModelCount(disabledModel); count != 0 {
		t.Fatalf("disabled model registry count = %d, want 0", count)
	}
}

func TestUpdateAggregatedAvailability_FutureNextRetryBlocksAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	next := now.Add(5 * time.Minute)
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = zero, want %v", next)
	}
	if auth.NextRetryAfter.Sub(next) > time.Second || next.Sub(auth.NextRetryAfter) > time.Second {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, next)
	}
}

func TestManager_AvailableProvidersAndHasProviderAuth_ExcludeDisabled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	if _, err := manager.Register(ctx, &Auth{ID: "active", Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatalf("register active auth: %v", err)
	}
	// Provider gemini only has an auth with the Disabled flag set.
	if _, err := manager.Register(ctx, &Auth{ID: "flag-disabled", Provider: "gemini", Disabled: true}); err != nil {
		t.Fatalf("register flag-disabled auth: %v", err)
	}
	// Provider codex only has an auth whose Status is StatusDisabled.
	if _, err := manager.Register(ctx, &Auth{ID: "status-disabled", Provider: "codex", Status: StatusDisabled}); err != nil {
		t.Fatalf("register status-disabled auth: %v", err)
	}

	providers := manager.AvailableProviders()
	present := make(map[string]bool, len(providers))
	for _, p := range providers {
		present[p] = true
	}
	if !present["claude"] {
		t.Errorf("AvailableProviders() = %v, want to include active provider claude", providers)
	}
	if present["gemini"] {
		t.Errorf("AvailableProviders() = %v, want to exclude Disabled provider gemini", providers)
	}
	if present["codex"] {
		t.Errorf("AvailableProviders() = %v, want to exclude StatusDisabled provider codex", providers)
	}

	if !manager.HasProviderAuth("claude") {
		t.Errorf("HasProviderAuth(claude) = false, want true")
	}
	if manager.HasProviderAuth("gemini") {
		t.Errorf("HasProviderAuth(gemini) = true, want false (only Disabled auth registered)")
	}
	if manager.HasProviderAuth("codex") {
		t.Errorf("HasProviderAuth(codex) = true, want false (only StatusDisabled auth registered)")
	}
}

func TestManager_ResetQuotaClearsRuntimeAndRegistryState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "reset-quota-auth"
	model := "reset-quota-model"
	next := time.Now().Add(time.Hour)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
				UpdatedAt:      next,
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg.SetModelQuotaExceeded(authID, model)
	reg.SuspendClientModel(authID, model, "quota")
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count before reset = %d, want 0", count)
	}

	updated, models, errReset := manager.ResetQuota(ctx, authID)
	if errReset != nil {
		t.Fatalf("ResetQuota() error = %v", errReset)
	}
	if updated == nil {
		t.Fatalf("ResetQuota() updated auth is nil")
	}
	if len(models) != 1 || models[0] != model {
		t.Fatalf("ResetQuota() models = %v, want [%s]", models, model)
	}
	if updated.Status != StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated auth state = status %q message %q unavailable %v next %v", updated.Status, updated.StatusMessage, updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() || updated.Quota.BackoffLevel != 0 {
		t.Fatalf("updated auth quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("updated model state missing")
	}
	if state.Status != StatusActive || state.StatusMessage != "" || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("updated model state = status %q message %q unavailable %v next %v", state.Status, state.StatusMessage, state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("updated model quota = %+v, want cleared", state.Quota)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count after reset = %d, want 1", count)
	}
}
