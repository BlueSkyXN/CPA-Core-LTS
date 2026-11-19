package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestRefreshOutcomeDuringCredentialReplacementSave(t *testing.T) {
	for _, test := range []struct {
		name       string
		provider   string
		refreshErr error
		saveErr    error
	}{
		{"codex-terminal", "codex", errors.New("token refresh failed with status 401: invalid_refresh_token"), nil},
		{"claude-terminal", "claude", errors.New("token refresh failed with status 400: Refresh token expired"), nil},
		{"transient", "codex", errors.New("connection reset by peer"), nil},
		{"canceled", "codex", context.Canceled, nil},
		{"deferred", "codex", codex.ErrSIWCRefreshDeferred, nil},
		{"claude-maintenance-wake", "claude", baseauth.ErrRefreshDeferred, nil},
		{"meta-mint-rollback", "meta", errors.New("connection reset by peer"), errors.New("store unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				store := &blockingEnrichingAuthStore{entered: make(chan struct{}), release: make(chan struct{})}
				store.err = test.saveErr
				m := NewManager(store, nil, nil)
				started, finish := make(chan struct{}), make(chan struct{})
				m.RegisterExecutor(&unauthorizedRefreshExecutor{id: test.provider, refreshErr: test.refreshErr, onRefresh: func() {
					close(started)
					<-finish
				}})
				old, err := m.Register(WithSkipPersist(ctx), &Auth{
					ID: "refresh-save.json", Provider: test.provider, Status: StatusActive,
					Metadata: map[string]any{
						"access_token": "fixture-old", "refresh_token": "fixture-old-grant",
						"expired": time.Now().Add(-time.Hour).Format(time.RFC3339),
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				refreshDone := make(chan error, 1)
				go func() {
					_, err := m.refreshAuthForRequestAtEpoch(ctx, old.ID, "", old.RegistrationEpoch)
					refreshDone <- err
				}()
				<-started
				replacement := old.Clone()
				replacement.Metadata["access_token"] = "fixture-new"
				replacement.Metadata["refresh_token"] = "fixture-new-grant"
				replacement.Metadata["expired"] = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
				updateDone := make(chan error, 1)
				go func() {
					if test.provider == "meta" {
						_, err := m.UpdatePreparedAuth(ctx, old, replacement)
						updateDone <- err
						return
					}
					_, err := m.Update(ctx, replacement)
					updateDone <- err
				}()
				<-store.entered
				close(finish)
				synctest.Wait()
				if test.saveErr == nil && len(refreshDone) == 0 {
					t.Error("an obsolete refresh waited for an ordinary replacement save")
				}
				close(store.release)
				if err := <-refreshDone; !errors.Is(err, test.refreshErr) {
					t.Fatalf("refresh returned %v", err)
				}
				if err := <-updateDone; !errors.Is(err, test.saveErr) {
					t.Fatalf("replacement returned %v, want %v", err, test.saveErr)
				}
				got, _ := m.GetByID(old.ID)
				if test.saveErr != nil {
					if got.CredentialVersion != old.CredentialVersion || authAccessToken(got) != authAccessToken(old) || got.RefreshError == nil || !got.NextRefreshAfter.After(time.Now()) {
						t.Fatal("aborted mint lost the original grant or its refresh backoff")
					}
					return
				}
				if got.CredentialVersion <= old.CredentialVersion || authAccessToken(got) != authAccessToken(replacement) || got.Attributes[AttributePath] == "" {
					t.Fatal("replacement or store enrichment missing")
				}
				if got.Unavailable || got.RequiresSignIn || got.LastError != nil || got.RefreshError != nil || !got.NextRefreshAfter.IsZero() {
					t.Fatalf("new credential inherited old refresh outcome: status=%s unavailable=%v next_refresh=%v", got.Status, got.Unavailable, got.NextRefreshAfter)
				}
				if blocked, _, _ := isAuthBlockedForModel(got, "fixture-model", time.Now()); blocked {
					t.Fatal("model selection blocked the new credential")
				}
			})
		})
	}
}

func TestRefreshJobKeepsCredentialVersion(t *testing.T) {
	ctx := context.Background()
	m, executor, primary, _, _ := newUnauthorizedRefreshFixture(t, false)
	job := m.markRefreshPending(nil, primary.ID, primary.RegistrationEpoch, primary.CredentialVersion, time.Now())
	if job == nil || !m.beginRefreshJob(ctx, job) {
		t.Fatal("refresh job did not begin")
	}
	next := primary.Clone()
	next.Metadata["refresh_token"] = "fixture-replacement-grant"
	if _, err := m.Update(ctx, next); err != nil {
		t.Fatal(err)
	}
	_, err := m.refreshAuthForRequestAtVersion(ctx, job.id, "", job.registrationEpoch, job.credentialVersion)
	if err == nil || executor.RefreshCalls() != 0 {
		t.Fatal("an old job submitted the replacement credential")
	}
	m.finishRefreshJob(job, time.Time{}, false)
}

func TestCanceledRefreshAfterCredentialReplacement(t *testing.T) {
	for _, refreshErr := range []error{context.Canceled, codex.ErrSIWCRefreshDeferred, baseauth.ErrRefreshDeferred} {
		for _, registerAgain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/reregister=%t", refreshErr, registerAgain), func(t *testing.T) {
				ctx := WithSkipPersist(context.Background())
				m, executor, primary, _, _ := newUnauthorizedRefreshFixture(t, false)
				executor.refreshErr = refreshErr
				executor.onRefresh = func() {
					replacement := primary.Clone()
					var err error
					if registerAgain {
						m.Remove(ctx, primary.ID)
						_, err = m.Register(ctx, replacement)
					} else {
						replacement.Metadata["refresh_token"] = "fixture-replacement-grant"
						_, err = m.Update(ctx, replacement)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				_, err := m.refreshAuthForRequestAtEpoch(ctx, primary.ID, "", primary.RegistrationEpoch)
				if !errors.Is(err, refreshErr) {
					t.Fatalf("refresh returned %v", err)
				}
				got, _ := m.GetByID(primary.ID)
				if !got.NextRefreshAfter.IsZero() {
					t.Fatalf("old refresh changed replacement's check time to %v", got.NextRefreshAfter)
				}
			})
		}
	}
}

func TestConcurrentUnauthorizedRespectsRefreshBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)
		m.SetSelector(&FillFirstSelector{})
		executor.refreshErr = errors.New("connection reset by peer")
		arrived := make(chan struct{}, 2)
		release := make(chan struct{})
		executor.onExecute = func(id string) {
			if id == primary.ID {
				arrived <- struct{}{}
				<-release
			}
		}
		var requests sync.WaitGroup
		for range 2 {
			requests.Go(func() {
				resp, err := m.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if err != nil || string(resp.Payload) != backup.ID+":backup-access-token" {
					t.Errorf("request did not fall back to backup: %v", err)
				}
			})
		}
		<-arrived
		<-arrived
		close(release)
		requests.Wait()
		current, _ := m.GetByID(primary.ID)
		if got := executor.RefreshCalls(); got != 1 || current.RefreshError == nil || !current.NextRefreshAfter.After(time.Now()) {
			t.Fatalf("got %d refresh attempts, next retry %v", got, current.NextRefreshAfter)
		}

		// An explicit operator retry and a request after the deadline may retry the grant.
		_, _ = m.ForceRefreshAuth(ctx, primary.ID)
		if got := executor.RefreshCalls(); got != 2 {
			t.Fatalf("forced refresh attempts = %d, want 2", got)
		}
		time.Sleep(refreshFailureBackoff + time.Second)
		_, _ = m.refreshAuthForRequest(ctx, primary.ID, authAccessToken(primary))
		if got := executor.RefreshCalls(); got != 3 {
			t.Fatalf("refresh attempts after backoff = %d, want 3", got)
		}
	})
}

func TestUnauthorizedRefreshBackoffScope(t *testing.T) {
	ctx := context.Background()
	t.Run("first-401-after-background-failure", func(t *testing.T) {
		m, executor, primary, _, _ := newUnauthorizedRefreshFixture(t, false)
		executor.refreshErr = permanentRefreshTestError{}
		m.refreshAuth(ctx, primary.ID)
		before, _ := m.GetByID(primary.ID)
		if !before.NextRefreshAfter.After(time.Now()) || hasUnauthorizedAuthFailure(before) {
			t.Fatal("expected backoff with a usable access token")
		}
		_, _ = m.RefreshAfterUnauthorized(ctx, before, &Error{HTTPStatus: 401}, false)
		after, _ := m.GetByID(primary.ID)
		if executor.RefreshCalls() != 2 || !hasUnauthorizedAuthFailure(after) {
			t.Fatal("the first access-token rejection must still resolve a background refresh failure")
		}
	})
	t.Run("outer-and-siwc", func(t *testing.T) {
		m, executor, primary, _, _ := newUnauthorizedRefreshFixture(t, false)
		dual := primary.Clone()
		dual.Metadata["use_siwc"] = true
		dual.Metadata["siwc"] = map[string]any{"access_token": "fixture-siwc", "refresh_token": "fixture-siwc-grant"}
		if _, err := m.Update(ctx, dual); err != nil {
			t.Fatal(err)
		}
		partitions := []struct{ token, message string }{
			{authOuterAccessToken(dual), "fixture transport failure for outer"},
			{"fixture-siwc", "fixture transport failure for siwc"},
		}
		for _, part := range partitions {
			executor.refreshErr = errors.New(part.message)
			if _, err := m.refreshAuthForRequest(ctx, dual.ID, part.token); !errors.Is(err, executor.refreshErr) {
				t.Fatal("first rejection on each partition must attempt a refresh")
			}
		}
		for _, part := range partitions {
			_, err := m.refreshAuthForRequest(ctx, dual.ID, part.token)
			var previous *Error
			if !errors.As(err, &previous) || previous.Message != part.message {
				t.Fatal("duplicate rejection did not return the failure for its partition")
			}
		}
		if got := executor.RefreshCalls(); got != 2 {
			t.Fatalf("refresh calls = %d, want one per rejected access token", got)
		}
	})
}

func newCodexPartitionRefreshManager(t *testing.T, useSIWC bool, refresh func(*Auth) (*Auth, error)) (*Manager, *Auth) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&codexPartialRefreshExecutor{
		unauthorizedRefreshExecutor: unauthorizedRefreshExecutor{id: "codex"},
		refresh:                     refresh,
	})
	expired := time.Now().Add(-time.Hour).Format(time.RFC3339)
	auth, err := m.Register(t.Context(), &Auth{
		ID: t.Name(), Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{
			"access_token": "outer-access", "refresh_token": "outer-grant", "expired": expired, "use_siwc": useSIWC,
			"siwc": map[string]any{"access_token": "siwc-access", "refresh_token": "siwc-grant", "client_id": "fixture", "expires_at": expired},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, auth
}

func TestUnauthorizedBackoffAfterPartialCodexRefresh(t *testing.T) {
	for _, partition := range []string{"outer", "siwc"} {
		t.Run(partition, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				useSIWC := partition == "siwc"
				failure := errors.New(partition + " temporarily unavailable")
				calls := 0
				m, auth := newCodexPartitionRefreshManager(t, useSIWC, func(before *Auth) (*Auth, error) {
					calls++
					updated := before.Clone()
					healthy := updated.Metadata
					expiryKey := "expired"
					var outerErr, siwcErr error
					if useSIWC {
						siwcErr = codex.WrapSIWCRefreshError(failure)
					} else {
						outerErr = failure
						healthy = updated.Metadata["siwc"].(map[string]any)
						expiryKey = "expires_at"
					}
					healthy["access_token"] = fmt.Sprintf("rotated-%d", calls)
					healthy[expiryKey] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
					updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(before.Metadata, updated.Metadata, useSIWC, !useSIWC)
					updated.CodexRefreshPatch.SetPartitionErrors(outerErr, siwcErr)
					return updated, nil
				})
				failedToken := authAccessToken(auth)
				saved, err := m.refreshAuthForRequest(ctx, auth.ID, failedToken)
				if err != nil || saved == nil {
					t.Fatalf("partial rotation did not commit: %v", err)
				}
				healthyToken := authSIWCTokenField(saved, "access_token")
				if useSIWC {
					healthyToken = authOuterAccessToken(saved)
				}
				if healthyToken != "rotated-1" || authAccessToken(saved) != failedToken || !saved.NextRefreshAfter.After(time.Now()) {
					t.Fatal("partial refresh must publish the healthy token and schedule the failed partition")
				}
				_, err = m.refreshAuthForRequest(ctx, auth.ID, failedToken)
				var previous *Error
				if calls != 1 || !errors.As(err, &previous) || previous.Message != failure.Error() {
					t.Fatalf("duplicate 401 got %v after %d calls", err, calls)
				}
				forced, err := m.ForceRefreshAuth(ctx, auth.ID)
				if err != nil || calls != 2 || forced == nil || !forced.NextRefreshAfter.After(time.Now()) {
					t.Fatalf("explicit refresh got %v after %d calls", err, calls)
				}
				time.Sleep(time.Until(forced.NextRefreshAfter) + time.Second)
				if _, err = m.refreshAuthForRequest(ctx, auth.ID, failedToken); err != nil || calls != 3 {
					t.Fatalf("retry after backoff got %v after %d calls", err, calls)
				}
			})
		})
	}
}

func TestUnauthorizedBackoffUsesRequestedPartitionError(t *testing.T) {
	outerFailure := errors.New("outer endpoint unavailable")
	siwcFailure := codex.WrapSIWCRefreshError(errors.New("inner endpoint unavailable"))
	for _, tc := range []struct {
		name     string
		useSIWC  bool
		outerErr error
		siwcErr  error
		want     error // nil when the next request should retry immediately
	}{
		{"outer", false, outerFailure, siwcFailure, outerFailure},
		{"siwc", true, outerFailure, siwcFailure, siwcFailure},
		{"outer-canceled", false, context.Canceled, siwcFailure, nil},
		{"siwc-deferred", true, outerFailure, codex.ErrSIWCRefreshDeferred, nil},
		{"only-siwc-failed", false, nil, siwcFailure, nil},
		{"only-outer-failed", true, outerFailure, nil, nil},
		{"requested-outer-only", false, outerFailure, nil, outerFailure},
		{"requested-siwc-only", true, nil, siwcFailure, siwcFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := codex.NewPartitionRefreshError(tc.outerErr, tc.siwcErr)
			calls := 0
			m, auth := newCodexPartitionRefreshManager(t, tc.useSIWC, func(*Auth) (*Auth, error) {
				calls++
				return nil, failure
			})
			token := authAccessToken(auth)
			_, firstErr := m.refreshAuthForRequest(t.Context(), auth.ID, token)
			if !errors.Is(firstErr, failure) || (tc.outerErr != nil && !errors.Is(firstErr, tc.outerErr)) || (tc.siwcErr != nil && !errors.Is(firstErr, tc.siwcErr)) {
				t.Fatalf("first refresh lost a partition error: %v", firstErr)
			}
			current, _ := m.GetByID(auth.ID)
			if !current.NextRefreshAfter.After(time.Now()) {
				t.Fatal("expected a future retry deadline")
			}
			_, err := m.refreshAuthForRequest(t.Context(), auth.ID, token)
			if tc.want == nil {
				if calls != 2 || !errors.Is(err, failure) {
					t.Fatalf("request should retry, got %v after %d calls", err, calls)
				}
				return
			}
			var previous *Error
			if calls != 1 || !errors.As(err, &previous) || previous.Message != tc.want.Error() {
				t.Fatalf("duplicate 401 got %v after %d calls, want %v", err, calls, tc.want)
			}
		})
	}
}

func TestPartialRefreshDoesNotTransfer401AcrossReplacement(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore-original-grant=%t", restore), func(t *testing.T) {
			ctx := t.Context()
			var m *Manager
			var auth *Auth
			calls := 0
			m, auth = newCodexPartitionRefreshManager(t, false, func(before *Auth) (*Auth, error) {
				calls++
				if calls == 1 {
					replacement := before.Clone()
					replacement.Metadata["refresh_token"] = "replacement-grant"
					changed, err := m.Update(ctx, replacement)
					if err != nil {
						t.Fatal(err)
					}
					if restore {
						changed.Metadata["refresh_token"] = authRefreshToken(before)
						if _, err = m.Update(ctx, changed); err != nil {
							t.Fatal(err)
						}
					}
				}
				updated := before.Clone()
				siwc := updated.Metadata["siwc"].(map[string]any)
				siwc["access_token"] = fmt.Sprintf("rotated-%d", calls)
				siwc["expires_at"] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
				updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(before.Metadata, updated.Metadata, false, true)
				updated.CodexRefreshPatch.SetPartitionErrors(errors.New("outer endpoint unavailable"), nil)
				return updated, nil
			})
			token := authOuterAccessToken(auth)
			saved, err := m.refreshAuthForRequest(ctx, auth.ID, token)
			if err != nil || saved == nil {
				t.Fatalf("partial refresh did not commit: %v", err)
			}
			wantGrant := "replacement-grant"
			if restore {
				wantGrant = authRefreshToken(auth)
			}
			if authRefreshToken(saved) != wantGrant || authOuterAccessToken(saved) != token || authSIWCTokenField(saved, "access_token") != "rotated-1" || !saved.NextRefreshAfter.After(time.Now()) {
				t.Fatal("expected the live outer grant, the rotated SIWC token and a future refresh time")
			}
			if _, err = m.refreshAuthForRequest(ctx, auth.ID, token); err != nil || calls != 2 {
				t.Fatalf("replacement inherited old 401 failure, got %v after %d calls", err, calls)
			}
		})
	}
}

func TestRefreshReservationUsesObservedCredentialVersion(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	m, executor, primary, _, _ := newUnauthorizedRefreshFixture(t, false)
	now := time.Now()
	loop := newAuthAutoRefreshLoop(m, time.Minute, 1)
	m.refreshLoop = loop

	old := primary.Clone()
	old.LastRefreshedAt = now
	old.Metadata["refresh_interval_seconds"] = 3600
	old.Metadata["expired"] = now.Add(-time.Hour).Format(time.RFC3339)
	if _, err := m.Update(ctx, old); err != nil {
		t.Fatal(err)
	}
	epoch, version, due := loop.dueCredential(old.ID, now)
	if !due {
		t.Fatal("expired credential should be due")
	}

	fresh := old.Clone()
	fresh.Metadata["access_token"] = "fixture-renewed-access"
	fresh.Metadata["refresh_token"] = "fixture-renewed-grant"
	fresh.Metadata["expired"] = now.Add(24 * time.Hour).Format(time.RFC3339)
	saved, err := m.Update(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if saved.RegistrationEpoch != epoch || saved.CredentialVersion == version || m.shouldRefresh(saved, now) {
		t.Fatal("expected a fresh credential in the same registration")
	}
	if job := m.markRefreshPending(loop, old.ID, epoch, version, now); job != nil {
		t.Fatal("stale due observation reserved a job on the new credential")
	}
	current, _ := m.GetByID(old.ID)
	if !current.NextRefreshAfter.Equal(saved.NextRefreshAfter) || executor.RefreshCalls() != 0 {
		t.Fatal("stale reservation changed the new credential's refresh state")
	}
	loop.applyDirty(now)
	want, scheduled := m.nextRefreshCheckAt(now, saved, loop.interval)
	if got, ok := loop.peek(); !scheduled || !ok || !want.After(now) || !got.Equal(want) {
		t.Fatalf("replacement's scheduled refresh = %v, want %v", got, want)
	}
}

func TestLateRemovalKeepsReregisteredRefreshSchedule(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	m, _, primary, _, _ := newUnauthorizedRefreshFixture(t, false)
	now := time.Now()
	loop := newAuthAutoRefreshLoop(m, time.Minute, 1)
	m.refreshLoop = loop

	m.Remove(ctx, primary.ID)
	removalEpoch := m.authEpochs[primary.ID]
	fresh := primary.Clone()
	fresh.LastRefreshedAt = now
	fresh.Metadata["expired"] = now.Add(24 * time.Hour).Format(time.RFC3339)
	fresh.Metadata["refresh_interval_seconds"] = 3600
	saved, err := m.Register(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if saved.RegistrationEpoch <= removalEpoch {
		t.Fatal("expected a new registration")
	}
	want, ok := m.nextRefreshCheckAt(now, saved, loop.interval)
	if !ok || !want.After(now) {
		t.Fatal("new registration should have a future refresh")
	}

	// The old cleanup may arrive either side of the loop processing the new notification.
	m.unscheduleRemovedAuth(primary.ID, removalEpoch)
	loop.applyDirty(now)
	if got, exists := loop.peek(); !exists || !got.Equal(want) {
		t.Fatal("old removal cleared the new registration's pending notification")
	}
	m.unscheduleRemovedAuth(primary.ID, removalEpoch)
	if got, exists := loop.peek(); !exists || !got.Equal(want) {
		t.Fatal("old removal cleared the new registration's scheduled refresh")
	}
}
