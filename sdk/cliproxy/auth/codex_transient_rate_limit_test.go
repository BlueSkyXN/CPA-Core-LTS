package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type classifiedCodexRateLimitError struct {
	class string
}

func (e classifiedCodexRateLimitError) Error() string               { return "codex rate limit" }
func (e classifiedCodexRateLimitError) StatusCode() int             { return http.StatusTooManyRequests }
func (e classifiedCodexRateLimitError) CodexRateLimitClass() string { return e.class }

func TestResultErrorFromErrorPreservesCodexRateLimitClass(t *testing.T) {
	const transientClass = "transient-rate-limit"
	resultErr := resultErrorFromError(classifiedCodexRateLimitError{class: transientClass})
	if resultErr == nil || resultErr.Code != transientClass {
		t.Fatalf("result error = %+v, want code %q", resultErr, transientClass)
	}
	if resultErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("HTTP status = %d, want 429", resultErr.HTTPStatus)
	}
}

func TestManagerMarkResultCodexTransientRateLimitDoesNotSetQuota(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	const (
		authID         = "codex-transient-rate-limit-auth"
		model          = "codex-transient-rate-limit-model"
		transientClass = "transient-rate-limit"
	)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	m.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "codex", Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: transientClass, Message: "rate_limit_error"},
	})

	updated, ok := m.GetByID(authID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatalf("missing updated model state: %+v", updated)
	}
	state := updated.ModelStates[model]
	if state.Quota.Exceeded || state.Quota.Reason != "" {
		t.Fatalf("transient rate limit was recorded as quota: %+v", state.Quota)
	}
	if !state.Unavailable || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("transient cooldown was not recorded: %+v", state)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count = %d, want transient error not to suspend it", count)
	}
}

func TestManagerMarkResultCodexRawTransientRateLimitDoesNotSetQuota(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	for _, tt := range []struct {
		name string
		code string
	}{
		{name: "rate-limit-error", code: "rate_limit_error"},
		{name: "rate-limit-exceeded", code: "rate_limit_exceeded"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			authID := "codex-raw-transient-" + tt.name
			model := "codex-raw-transient-model-" + tt.name
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })

			m := NewManager(nil, nil, nil)
			if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
				t.Fatalf("register auth: %v", err)
			}
			m.MarkResult(context.Background(), Result{
				AuthID: authID, Provider: "codex", Model: model,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: tt.code, Message: tt.code},
			})

			updated, ok := m.GetByID(authID)
			if !ok || updated == nil || updated.ModelStates[model] == nil {
				t.Fatalf("missing updated model state: %+v", updated)
			}
			state := updated.ModelStates[model]
			if state.Quota.Exceeded || state.Quota.Reason != "" {
				t.Fatalf("raw transient rate limit was recorded as quota: %+v", state.Quota)
			}
			if !state.Unavailable || !state.NextRetryAfter.After(time.Now()) {
				t.Fatalf("raw transient cooldown was not recorded: %+v", state)
			}
			if count := reg.GetModelCount(model); count != 1 {
				t.Fatalf("registry model count = %d, want transient error not to suspend it", count)
			}
		})
	}
}

func TestManagerMarkResultCodexTransientRateLimitWithoutModelDoesNotSetQuota(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	for _, tt := range []struct {
		name string
		code string
	}{
		{name: "synthetic", code: "transient-rate-limit"},
		{name: "rate-limit-error", code: "rate_limit_error"},
		{name: "rate-limit-exceeded", code: "rate_limit_exceeded"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			authID := "codex-auth-level-transient-" + tt.name
			m := NewManager(nil, nil, nil)
			if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
				t.Fatalf("register auth: %v", err)
			}
			m.MarkResult(context.Background(), Result{
				AuthID: authID, Provider: "codex",
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: tt.code, Message: tt.code},
			})

			updated, ok := m.GetByID(authID)
			if !ok || updated == nil {
				t.Fatalf("missing updated auth: %+v", updated)
			}
			if updated.Quota.Exceeded || updated.Quota.Reason != "" {
				t.Fatalf("auth-level transient rate limit was recorded as quota: %+v", updated.Quota)
			}
			if !updated.Unavailable || !updated.NextRetryAfter.After(time.Now()) {
				t.Fatalf("auth-level transient cooldown was not recorded: %+v", updated)
			}
		})
	}
}

func TestManagerMarkResultCodexTransientRateLimitDoesNotEraseActiveQuota(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	const (
		authID = "codex-transient-after-quota-auth"
		model  = "codex-transient-after-quota-model"
	)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	m.MarkResult(context.Background(), Result{
		AuthID: authID, Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: "usage-limit", Message: "usage_limit_reached"},
	})
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count after quota = %d, want 0", count)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: authID, Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: "transient-rate-limit", Message: "rate_limit_error"},
	})

	updated, ok := m.GetByID(authID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatalf("missing updated model state: %+v", updated)
	}
	state := updated.ModelStates[model]
	if !state.Quota.Exceeded || state.Quota.Reason != "quota" || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("active quota was erased by an older transient result: %+v", state)
	}
	if state.LastError == nil || state.LastError.Code != "usage-limit" {
		t.Fatalf("active quota error was replaced by transient state: %+v", state.LastError)
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count = %d, want manager and registry to retain active quota", count)
	}
}

func TestManagerMarkResultCodexRawTransientRateLimitDoesNotEraseActiveQuota(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	const (
		authID = "codex-raw-transient-after-quota-auth"
		model  = "codex-raw-transient-after-quota-model"
	)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	m.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "codex", Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: "usage-limit", Message: "usage_limit_reached"},
	})
	m.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "codex", Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: "rate_limit_exceeded", Message: "rate_limit_exceeded"},
	})

	updated, ok := m.GetByID(authID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatalf("missing updated model state: %+v", updated)
	}
	state := updated.ModelStates[model]
	if !state.Quota.Exceeded || state.Quota.Reason != "quota" || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("active quota was erased by raw transient result: %+v", state)
	}
	if state.LastError == nil || state.LastError.Code != "usage-limit" {
		t.Fatalf("active quota error was replaced by raw transient state: %+v", state.LastError)
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count = %d, want active quota retained", count)
	}
}

func TestManagerMarkResultCodexRawTransientRateLimitWithoutModelPreservesActiveQuota(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	const authID = "codex-auth-level-raw-transient-after-quota"
	retryAfter := 30 * time.Minute
	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	m.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "codex",
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Code: "usage-limit", Message: "usage_limit_reached"},
		RetryAfter: &retryAfter,
	})
	before, _ := m.GetByID(authID)
	m.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "codex",
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: "rate_limit_error", Message: "rate_limit_error"},
	})

	updated, ok := m.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("missing updated auth: %+v", updated)
	}
	if !updated.Quota.Exceeded || updated.Quota.Reason != "quota" || !updated.NextRetryAfter.Equal(before.NextRetryAfter) {
		t.Fatalf("active auth-level quota was erased by raw transient result: before=%+v after=%+v", before, updated)
	}
	if updated.LastError == nil || updated.LastError.Code != "usage-limit" {
		t.Fatalf("active auth-level quota error was replaced: %+v", updated.LastError)
	}
}

func TestManagerMarkResultNonCodexRawRateLimitRemainsQuota(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	const (
		authID = "xai-raw-rate-limit-auth"
		model  = "xai-raw-rate-limit-model"
	)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "xai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	m := NewManager(nil, nil, nil)
	if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "xai", Status: StatusActive}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	m.MarkResult(context.Background(), Result{
		AuthID: authID, Provider: "xai", Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: "rate_limit_error", Message: "rate_limit_error"},
	})

	updated, ok := m.GetByID(authID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatalf("missing updated model state: %+v", updated)
	}
	if !updated.ModelStates[model].Quota.Exceeded || updated.ModelStates[model].Quota.Reason != "quota" {
		t.Fatalf("non-Codex raw rate limit did not remain quota: %+v", updated.ModelStates[model])
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count = %d, want non-Codex quota suspension", count)
	}
}
