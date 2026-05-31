package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
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

func TestIsRequestInvalidError_AllowsClaudeExtraUsageRotation(t *testing.T) {
	t.Parallel()

	extraUsageErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"type":"error","error":{"type":"invalid_request_error","message":"You're out of extra usage. Add more at claude.ai/settings/usage and keep going."}}`,
	}
	if isRequestInvalidError(extraUsageErr) {
		t.Fatal("extra usage 400 should be treated as credential quota, not request-shape failure")
	}

	malformedErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    "invalid_request_error: malformed payload",
	}
	if !isRequestInvalidError(malformedErr) {
		t.Fatal("malformed invalid_request_error should remain request-scoped")
	}
}

func TestManagerMarkResult_PromotesClaudeExtraUsageToQuotaCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "claude-extra-usage-auth",
		Provider: "claude",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "claude-sonnet-4-5"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusBadRequest,
			Message:    `{"type":"error","error":{"type":"invalid_request_error","message":"You're out of extra usage. Add more at claude.ai/settings/usage and keep going."}}`,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable || state.Status != StatusError {
		t.Fatalf("model state = %+v, want unavailable error state", state)
	}
	if !state.Quota.Exceeded || state.Quota.Reason != "quota" {
		t.Fatalf("model quota = %+v, want quota exceeded", state.Quota)
	}
	if state.NextRetryAfter.IsZero() || state.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("expected quota cooldown times, state = %+v", state)
	}
	if updated.LastError == nil || updated.LastError.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("last error = %+v, want original 400 error recorded", updated.LastError)
	}
}
