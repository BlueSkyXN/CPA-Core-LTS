package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestIsXaiBadCredentialsError(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		err      error
		want     bool
	}{
		{name: "bad credentials", provider: "xai", err: &Error{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"}, want: true},
		{name: "token not validated", provider: " XAI ", err: &Error{HTTPStatus: http.StatusForbidden, Message: "The OAuth2 access token could not be validated."}, want: true},
		{name: "other provider", provider: "claude", err: &Error{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"}},
		{name: "other status", provider: "xai", err: &Error{HTTPStatus: http.StatusPaymentRequired, Message: "unauthenticated:bad-credentials"}},
		{name: "generic forbidden", provider: "xai", err: &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"}},
		{name: "nil error", provider: "xai"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isXaiBadCredentialsError(tt.provider, tt.err); got != tt.want {
				t.Fatalf("isXaiBadCredentialsError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsXaiBadCredentialsResultErrorReadsCodeAndMessage(t *testing.T) {
	for _, errResult := range []*Error{
		{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"},
		{HTTPStatus: http.StatusForbidden, Code: "unauthenticated:bad-credentials"},
		{HTTPStatus: http.StatusForbidden, Message: "The OAuth2 access token could not be validated."},
	} {
		if !isXaiBadCredentialsResultError("xai", errResult) {
			t.Fatalf("expected xAI credential error for %+v", errResult)
		}
	}
	if isXaiBadCredentialsResultError("xai", &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"}) {
		t.Fatal("generic xAI 403 must not be classified as bad credentials")
	}
}

func TestManager_MarkResult_XaiBadCredentialsMarksModelUnauthorized(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-xai-403", Provider: "xai"}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	model := "test-model-xai-403"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "xai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "xai", Model: model,
		Error: &Error{HTTPStatus: http.StatusForbidden, Message: "unauthenticated:bad-credentials"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected unavailable model state, got %+v", state)
	}
	if diff := time.Until(state.NextRetryAfter); diff < 29*time.Minute || diff > 31*time.Minute {
		t.Fatalf("expected about 30 minute cooldown, got %v", diff)
	}
	if state.LastError == nil || state.LastError.Code != "unauthorized" {
		t.Fatalf("expected model LastError.Code = unauthorized, got %+v", state.LastError)
	}
	if updated.LastError == nil || updated.LastError.Code != "unauthorized" {
		t.Fatalf("expected auth LastError.Code = unauthorized, got %+v", updated.LastError)
	}
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("expected registry model suspension, count = %d", count)
	}
}

func TestManager_MarkResult_XaiBadCredentialsMarksAuthUnauthorized(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-xai-403-auth-level", Provider: "xai"}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "xai",
		Error: &Error{HTTPStatus: http.StatusForbidden, Message: "The OAuth2 access token could not be validated."},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	if updated.StatusMessage != "unauthorized" {
		t.Fatalf("StatusMessage = %q, want unauthorized", updated.StatusMessage)
	}
	if updated.LastError == nil || updated.LastError.Code != "unauthorized" {
		t.Fatalf("expected auth LastError.Code = unauthorized, got %+v", updated.LastError)
	}
	if diff := time.Until(updated.NextRetryAfter); diff < 29*time.Minute || diff > 31*time.Minute {
		t.Fatalf("expected about 30 minute cooldown, got %v", diff)
	}
}
