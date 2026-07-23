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
		AuthID:   authID,
		Provider: "codex",
		Model:    model,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Code:       transientClass,
			Message:    "rate_limit_error",
		},
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

func TestManagerMarkResultCodexTransientRateLimitDoesNotEraseActiveQuota(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	const (
		authID         = "codex-transient-after-quota-auth"
		model          = "codex-transient-after-quota-model"
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
		AuthID: authID,
		Model:  model,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Code:       "usage-limit",
			Message:    "usage_limit_reached",
		},
	})
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count after quota = %d, want 0", count)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: authID,
		Model:  model,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Code:       transientClass,
			Message:    "rate_limit_error",
		},
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
