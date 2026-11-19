package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRefreshFailureSurvivesConcurrentRequestResult(t *testing.T) {
	for _, expired := range []bool{false, true} {
		for _, terminal := range []bool{false, true} {
			for _, quota := range []bool{false, true} {
				name := "valid"
				if expired {
					name = "expired"
				}
				if terminal {
					name += "/terminal"
				} else {
					name += "/transient"
				}
				if quota {
					name += "/quota-result"
				} else {
					name += "/success-result"
				}
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					m := NewManager(nil, &RoundRobinSelector{}, nil)
					errRefresh := errors.New("token refresh request failed: connection reset by peer")
					if terminal {
						errRefresh = errors.New(`token refresh failed with status 401: {"error":{"code":"invalid_refresh_token"}}`)
					}
					exec := &unauthorizedRefreshExecutor{id: "codex", refreshErr: errRefresh}
					m.RegisterExecutor(exec)
					expiry := time.Now().Add(8 * time.Hour)
					if expired {
						expiry = time.Now().Add(-time.Hour)
					}
					a, err := m.Register(ctx, &Auth{
						ID: "refresh-result.json", Provider: "codex", Status: StatusActive,
						Metadata: map[string]any{
							"access_token": "test-access-token", "refresh_token": "test-refresh-token",
							"expires_at": expiry.Format(time.RFC3339),
						},
					})
					if err != nil {
						t.Fatal(err)
					}
					// Inference results change Generation without replacing credentials.
					exec.onRefresh = func() {
						result := Result{AuthID: a.ID, Provider: "codex", Model: "test-model", Success: !quota}
						if quota {
							result.Error = &Error{HTTPStatus: 429, Message: "quota exceeded"}
						}
						m.MarkResult(ctx, result)
					}
					now := time.Now()
					job := m.markRefreshPending(nil, a.ID, a.RegistrationEpoch, a.CredentialVersion, now)
					if job == nil || !m.beginRefreshJob(ctx, job) {
						t.Fatal("refresh job did not begin")
					}
					_, err = m.refreshAuthForRequestAtEpoch(ctx, a.ID, "", job.registrationEpoch)
					if err == nil {
						t.Fatal("expected refresh failure")
					}
					m.finishRefreshJob(job, time.Time{}, false)
					current, _ := m.GetByID(a.ID)
					if current.RefreshError == nil {
						t.Fatal("concurrent request result discarded the refresh failure")
					}
					if m.shouldRefresh(current, time.Now()) {
						t.Fatal("failed refresh became immediately due after job completion")
					}
					if terminal && expired {
						if !hasUnauthorizedAuthFailure(current) {
							t.Fatal("expired credential with rejected refresh token was not stopped")
						}
					} else if !current.NextRefreshAfter.After(now) {
						t.Fatal("refresh outcome lost its future retry/check time")
					}
					if !expired && !current.HasValidAccessToken(time.Now()) {
						t.Fatal("refresh failure discarded the still-valid access token")
					}
					if quota {
						state := current.ModelStates["test-model"]
						if current.Failed != 1 || state == nil || !state.Quota.Exceeded || !state.Unavailable {
							t.Fatal("refresh failure overwrote the concurrent model quota result")
						}
					} else if current.Success != 1 {
						t.Fatal("refresh failure overwrote the concurrent success count")
					}
					if exec.RefreshCalls() != 1 {
						t.Fatalf("refresh calls = %d, want 1", exec.RefreshCalls())
					}
				})
			}
		}
	}
}
