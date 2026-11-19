package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClaudeExpiredRefreshSurvivesConcurrentRequestResult(t *testing.T) {
	for _, expired := range []bool{false, true} {
		for _, quota := range []bool{false, true} {
			name := "valid-access"
			if expired {
				name = "expired-access"
			}
			if quota {
				name += "/quota-result"
			} else {
				name += "/success-result"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				m := NewManager(nil, &RoundRobinSelector{}, nil)
				exec := &unauthorizedRefreshExecutor{
					id:         "claude",
					refreshErr: errors.New(`token refresh failed with status 400: {"error":"invalid_grant","error_description":"Refresh token expired"}`),
				}
				m.RegisterExecutor(exec)
				now := time.Now()
				expiry := now.Add(8 * time.Hour).Truncate(time.Second)
				if expired {
					expiry = now.Add(-time.Hour).Truncate(time.Second)
				}
				a, err := m.Register(ctx, &Auth{
					ID: "claude-expired-refresh-concurrent", Provider: "claude", Status: StatusActive,
					Metadata: map[string]any{
						"type": "claude", "access_token": "fixture-access", "refresh_token": "fixture-refresh",
						"expired": expiry.Format(time.RFC3339),
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				exec.onRefresh = func() {
					result := Result{AuthID: a.ID, Provider: "claude", Model: "test-model", Success: !quota}
					if quota {
						result.Error = &Error{HTTPStatus: 429, Message: "quota exceeded"}
					}
					m.MarkResult(ctx, result)
				}
				job := m.markRefreshPending(nil, a.ID, a.RegistrationEpoch, a.CredentialVersion, now)
				if job == nil || !m.beginRefreshJob(ctx, job) {
					t.Fatal("refresh job did not begin")
				}
				if _, err = m.refreshAuthForRequestAtEpoch(ctx, a.ID, "", job.registrationEpoch); err == nil {
					t.Fatal("expected Claude refresh token expiry error")
				}
				m.finishRefreshJob(job, time.Time{}, false)
				current, _ := m.GetByID(a.ID)
				if current.RefreshError == nil || current.RefreshError.Code != "refresh_token_expired" || !current.RequiresSignIn {
					t.Fatal("concurrent inference discarded Claude expired-refresh terminal state")
				}
				if current.RejectedRefreshToken != "fixture-refresh" {
					t.Fatal("expired Claude refresh token was not recorded as rejected")
				}
				if m.shouldRefresh(current, time.Now()) {
					t.Fatal("expired Claude refresh token became immediately due after job completion")
				}
				if expired {
					if !hasUnauthorizedAuthFailure(current) || !current.NextRefreshAfter.IsZero() {
						t.Fatal("expired access and refresh credentials must stop refreshing")
					}
					if _, scheduled := m.nextRefreshCheckAt(time.Now(), current, refreshCheckInterval); scheduled {
						t.Fatal("expired Claude credential was scheduled again")
					}
				} else if !current.HasValidAccessToken(time.Now()) || !current.NextRefreshAfter.Equal(expiry) {
					t.Fatal("valid access token must survive until its expiry without refresh retries")
				}
				if quota {
					state := current.ModelStates["test-model"]
					if current.Failed != 1 || state == nil || !state.Quota.Exceeded || !state.Unavailable {
						t.Fatal("refresh terminal outcome overwrote concurrent quota state")
					}
				} else if current.Success != 1 {
					t.Fatal("refresh terminal outcome overwrote success count")
				}
				_, _ = m.refreshAuthForRequest(ctx, a.ID, "")
				if exec.RefreshCalls() != 1 {
					t.Fatal("rejected Claude refresh token was resubmitted")
				}
			})
		}
	}
}
