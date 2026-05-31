package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestManagerMarkResult_CircuitBreakerExtendsModelCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		AuthCircuitBreakerThreshold:       2,
		AuthCircuitBreakerCooldownMinutes: 7,
	})
	auth := &Auth{ID: "auth-circuit", Provider: "gemini"}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	model := "gemini-test"
	start := time.Now()
	for i := 0; i < 2; i++ {
		manager.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    model,
			Success:  false,
			Error:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream unavailable"},
		})
	}

	got, ok := manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatalf("GetByID() ok=%v auth=%v", ok, got)
	}
	if got.ConsecutiveTransientFailures != 2 {
		t.Fatalf("ConsecutiveTransientFailures = %d, want 2", got.ConsecutiveTransientFailures)
	}
	state := got.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	minRetry := start.Add(6 * time.Minute)
	if !state.NextRetryAfter.After(minRetry) {
		t.Fatalf("model NextRetryAfter = %s, want after %s", state.NextRetryAfter, minRetry)
	}
	if !got.NextRetryAfter.Equal(state.NextRetryAfter) {
		t.Fatalf("auth NextRetryAfter = %s, want model retry %s", got.NextRetryAfter, state.NextRetryAfter)
	}
	blocked, reason, next := isAuthBlockedForModel(got, "another-model", time.Now())
	if !blocked || reason != blockReasonOther || !next.Equal(got.NextRetryAfter) {
		t.Fatalf("isAuthBlockedForModel() = blocked %v reason %v next %s, want circuit block until %s", blocked, reason, next, got.NextRetryAfter)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  true,
	})
	got, ok = manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatalf("GetByID() after success ok=%v auth=%v", ok, got)
	}
	if got.ConsecutiveTransientFailures != 0 {
		t.Fatalf("ConsecutiveTransientFailures after success = %d, want 0", got.ConsecutiveTransientFailures)
	}
	if state := got.ModelStates[model]; state == nil || !state.NextRetryAfter.IsZero() {
		t.Fatalf("model state after success = %+v, want cleared retry", state)
	}
}

func TestManagerMarkResult_CircuitBreakerResetsOnNonTransientError(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		AuthCircuitBreakerThreshold:       2,
		AuthCircuitBreakerCooldownMinutes: 7,
	})
	auth := &Auth{ID: "auth-circuit-reset", Provider: "claude"}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream unavailable"},
	})
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})

	got, ok := manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatalf("GetByID() ok=%v auth=%v", ok, got)
	}
	if got.ConsecutiveTransientFailures != 0 {
		t.Fatalf("ConsecutiveTransientFailures = %d, want 0", got.ConsecutiveTransientFailures)
	}
}

func TestManagerMarkResult_CircuitBreakerCanBeDisabled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{AuthCircuitBreakerThreshold: -1})
	auth := &Auth{ID: "auth-circuit-disabled", Provider: "codex"}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	for i := 0; i < 3; i++ {
		manager.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Success:  false,
			Error:    &Error{HTTPStatus: http.StatusGatewayTimeout, Message: "timeout"},
		})
	}

	got, ok := manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatalf("GetByID() ok=%v auth=%v", ok, got)
	}
	if got.ConsecutiveTransientFailures != 0 {
		t.Fatalf("ConsecutiveTransientFailures = %d, want 0", got.ConsecutiveTransientFailures)
	}
}
