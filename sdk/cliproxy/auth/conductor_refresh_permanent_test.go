package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// permanentRefreshTestError stands in for a provider refresh error, such as a Codex refresh token
// the token endpoint reports as invalidated, without matching any invalid_grant text.
type permanentRefreshTestError struct{}

func (permanentRefreshTestError) Error() string {
	return `token refresh failed with status 401: {"error":{"code":"refresh_token_invalidated"}}`
}

func (permanentRefreshTestError) PermanentRefreshFailure() bool { return true }

func registerRefreshTestAuth(t *testing.T, manager *Manager, id string, expiresIn time.Duration) *Auth {
	t.Helper()
	auth := &Auth{
		ID:       id,
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "still-valid-token",
			"refresh_token": "refresh-1",
			"expires_at":    time.Now().Add(expiresIn).Format(time.RFC3339),
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	return auth
}

func TestRefreshAuthForRequest_PermanentRefreshFailureBacksOffWhileAccessTokenValid(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: fmt.Errorf("token refresh failed after 1 attempts: %w", permanentRefreshTestError{}),
	})
	auth := registerRefreshTestAuth(t, manager, "permanent-refresh-valid-at", 8*time.Hour)

	for attempt, want := range []time.Duration{time.Minute, 2 * time.Minute} {
		started := time.Now()
		_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
		manager.mu.RLock()
		current := manager.auths[auth.ID].Clone()
		manager.mu.RUnlock()
		if current.RefreshFailures != attempt+1 {
			t.Fatalf("attempt %d: RefreshFailures = %d, want %d", attempt+1, current.RefreshFailures, attempt+1)
		}
		if current.Unavailable {
			t.Fatalf("attempt %d: auth marked unavailable while its access token is still valid", attempt+1)
		}
		diff := current.NextRefreshAfter.Sub(started)
		if diff < want-10*time.Second || diff > want+10*time.Second {
			t.Fatalf("attempt %d: next refresh in %v, want ~%v", attempt+1, diff, want)
		}
	}
}

func TestRefreshAuthForRequest_TransientRefreshFailureKeepsFixedRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: errors.New("token refresh request failed: connection reset by peer"),
	})
	auth := registerRefreshTestAuth(t, manager, "transient-refresh-valid-at", 8*time.Hour)

	started := time.Now()
	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")
	manager.mu.RLock()
	current := manager.auths[auth.ID].Clone()
	manager.mu.RUnlock()
	if current.RefreshFailures != 0 {
		t.Fatalf("RefreshFailures = %d, want 0 for a transient failure", current.RefreshFailures)
	}
	diff := current.NextRefreshAfter.Sub(started)
	if diff < refreshFailureBackoff-10*time.Second || diff > refreshFailureBackoff+10*time.Second {
		t.Fatalf("next refresh in %v, want ~%v", diff, refreshFailureBackoff)
	}
}

func TestIsPermanentRefreshFailureUsesTypedClassification(t *testing.T) {
	if !isPermanentRefreshFailure(fmt.Errorf("wrapped: %w", permanentRefreshTestError{})) {
		t.Fatal("wrapped permanent refresh error not recognized")
	}
	if isPermanentRefreshFailure(errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_invalidated"}}`)) {
		t.Fatal("untyped error text must not be classified as a permanent refresh failure")
	}
}

// The re-login signal Management derives from RefreshFailures must outlive successful requests made
// with the still-valid access token, and must clear once the credential is replaced.
func TestPermanentRefreshFailureSurvivesSuccessUntilCredentialReplaced(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&mockOAuthErrorExecutor{
		id:          "test-provider",
		errToReturn: fmt.Errorf("token refresh failed after 1 attempts: %w", permanentRefreshTestError{}),
	})
	auth := registerRefreshTestAuth(t, manager, "permanent-refresh-relogin", 8*time.Hour)
	_, _ = manager.refreshAuthForRequest(ctx, auth.ID, "")

	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "test-provider", Model: "m", Success: true})
	current, ok := manager.GetByID(auth.ID)
	if !ok || current.RefreshFailures != 1 {
		t.Fatalf("after a successful request RefreshFailures = %d, want 1", current.RefreshFailures)
	}

	replaced := &Auth{
		ID:       auth.ID,
		Provider: "test-provider",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "refresh-2",
			"expires_at":    time.Now().Add(8 * time.Hour).Format(time.RFC3339),
		},
	}
	if _, err := manager.Update(ctx, replaced); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	current, _ = manager.GetByID(auth.ID)
	if current.RefreshFailures != 0 {
		t.Fatalf("after credential replacement RefreshFailures = %d, want 0", current.RefreshFailures)
	}
}
