package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type codexContinuityTestClock struct {
	mu  sync.Mutex
	now time.Time
}

type codexContinuityReloadStore struct {
	auth *Auth
}

func (s *codexContinuityReloadStore) List(context.Context) ([]*Auth, error) {
	if s == nil || s.auth == nil {
		return nil, nil
	}
	return []*Auth{s.auth.Clone()}, nil
}

func (*codexContinuityReloadStore) Save(context.Context, *Auth) (string, error) { return "", nil }
func (*codexContinuityReloadStore) Delete(context.Context, string) error        { return nil }

type codexContinuityUsageLimitError struct {
	retryAfter time.Duration
}

func (*codexContinuityUsageLimitError) Error() string { return "usage limit reached" }
func (*codexContinuityUsageLimitError) StatusCode() int {
	return http.StatusTooManyRequests
}
func (*codexContinuityUsageLimitError) ModelFallbackReason() string {
	return internalconfig.CodexModelFallbackTriggerUsageLimit
}
func (e *codexContinuityUsageLimitError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	retryAfter := e.retryAfter
	return &retryAfter
}

func (c *codexContinuityTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *codexContinuityTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func codexContinuityTestPolicy(threshold int) internalconfig.EffectiveCodexRateLimitContinuityConfig {
	if threshold <= 0 {
		threshold = 2
	}
	return internalconfig.EffectiveCodexRateLimitContinuityConfig{
		Enabled:                      true,
		ObservationWindowSeconds:     10,
		EstablishedSuccessThreshold:  threshold,
		EstablishedSessionTTLSeconds: 3600,
	}
}

func newCodexContinuityTestStore(clock *codexContinuityTestClock) *codexRateLimitContinuityStore {
	store := newCodexRateLimitContinuityStore()
	store.now = clock.Now
	return store
}

func TestCodexRateLimitContinuityFirstFreshUsageLimitEntersFreshBlocked(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	attempt, allowed := store.begin(key, "execution:fresh", cfg)
	if !allowed {
		t.Fatal("begin() denied healthy auth")
	}
	if got := store.observe(attempt, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg); got != codexRateLimitContinuityObserveOnly {
		t.Fatalf("observe() = %v, want observe-only", got)
	}
	if store.candidateAllowed(key, "execution:another-fresh", cfg) {
		t.Fatal("fresh session was allowed before observation window")
	}
	store.mu.Lock()
	state := store.states[key]
	store.mu.Unlock()
	if state == nil || state.phase != codexRateLimitContinuityFreshBlocked {
		t.Fatalf("state = %+v, want FreshBlocked", state)
	}
}

func TestCodexRateLimitContinuityEstablishedSessionObservesThenCanaryRecovers(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(1)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}

	established, _ := store.begin(key, "execution:established", cfg)
	store.observe(established, true, "", cfg)
	fresh, _ := store.begin(key, "execution:fresh", cfg)
	store.observe(fresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)

	established, allowed := store.begin(key, "execution:established", cfg)
	if !allowed || !established.established {
		t.Fatalf("established begin = %+v allowed=%t", established, allowed)
	}
	store.observe(established, true, "", cfg)
	canary, allowed := store.begin(key, "execution:canary", cfg)
	if !allowed || canary.canaryToken == 0 {
		t.Fatalf("canary begin = %+v allowed=%t", canary, allowed)
	}
	store.observe(canary, true, "", cfg)
	if !store.candidateAllowed(key, "execution:new", cfg) {
		t.Fatal("healthy state did not allow a new session after canary success")
	}
}

func TestCodexRateLimitContinuityEstablishedFailureConfirmsButCanaryWithIncumbentObserves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		canary bool
	}{
		{name: "established"},
		{name: "canary", canary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
			store := newCodexContinuityTestStore(clock)
			cfg := codexContinuityTestPolicy(2)
			key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
			established, _ := store.begin(key, "execution:established", cfg)
			store.observe(established, true, "", cfg)
			fresh, _ := store.begin(key, "execution:fresh", cfg)
			store.observe(fresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)

			var probe codexRateLimitContinuityAttempt
			if tc.canary {
				clock.Advance(11 * time.Second)
				probe, _ = store.begin(key, "execution:canary", cfg)
				if probe.canaryToken == 0 {
					t.Fatal("canary token = 0")
				}
			} else {
				probe, _ = store.begin(key, "execution:established", cfg)
			}
			got := store.observe(probe, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)
			if tc.canary {
				// A failed fresh canary cannot globally cool an auth while the
				// established lease is still healthy.
				if got != codexRateLimitContinuityObserveOnly {
					t.Fatalf("canary observe() = %v, want observe-only", got)
				}
			} else if got != codexRateLimitContinuityNormal {
				t.Fatalf("observe() = %v, want normal cooldown", got)
			}
			store.mu.Lock()
			state := store.states[key]
			store.mu.Unlock()
			if tc.canary {
				if state == nil || state.phase != codexRateLimitContinuityFreshBlocked || len(state.establishedSessions) == 0 {
					t.Fatalf("fresh-blocked state = %+v", state)
				}
			} else if state == nil || !state.confirmed || state.generation <= probe.generation {
				t.Fatalf("confirmed state = %+v, probe generation=%d", state, probe.generation)
			}
		})
	}
}

func TestCodexRateLimitContinuityCanaryFailureWithIncumbentKeepsGeneration(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	established, _ := store.begin(key, "execution:established", cfg)
	store.observe(established, true, "", cfg)
	fresh, _ := store.begin(key, "execution:fresh", cfg)
	store.observe(fresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)
	staleEstablished, _ := store.begin(key, "execution:established", cfg)
	clock.Advance(11 * time.Second)
	canary, _ := store.begin(key, "execution:canary", cfg)
	store.observe(canary, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)
	if got := store.observe(staleEstablished, true, "", cfg); got != codexRateLimitContinuityNormal {
		t.Fatalf("stale observe() = %v, want normal; canary failure with incumbent must not advance generation", got)
	}
}

func TestCodexRateLimitContinuityRecoveredGenerationRejectsStaleFailure(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	staleFresh, _ := store.begin(key, "execution:stale", cfg)
	failingFresh, _ := store.begin(key, "execution:failing", cfg)
	store.observe(failingFresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)
	clock.Advance(11 * time.Second)
	canary, _ := store.begin(key, "execution:canary", cfg)
	store.observe(canary, true, "", cfg)
	if got := store.observe(staleFresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg); got != codexRateLimitContinuityRecordOnly {
		t.Fatalf("stale failure observe() = %v, want record-only", got)
	}
	store.mu.Lock()
	state := store.states[key]
	store.mu.Unlock()
	if state == nil || state.suspect || state.confirmed {
		t.Fatalf("recovered state = %+v", state)
	}
}

func TestCodexRateLimitContinuityOnlyOneConcurrentCanary(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	store.states[key] = &codexRateLimitContinuityState{
		suspect:             true,
		observeUntil:        clock.Now().Add(-time.Second),
		establishedSessions: make(map[string]time.Time),
	}

	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			attempt, ok := store.begin(key, "execution:canary-"+time.Unix(int64(index), 0).Format(time.RFC3339), cfg)
			if ok && attempt.canaryToken != 0 {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := allowed.Load(); got != 1 {
		t.Fatalf("allowed canaries = %d, want 1", got)
	}
}

func TestCodexRateLimitContinuityAbandonedCanaryRequiresNewObservationWindow(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	store.states[key] = &codexRateLimitContinuityState{
		suspect:             true,
		generation:          1,
		observeUntil:        clock.Now().Add(-time.Second),
		establishedSessions: make(map[string]time.Time),
	}
	canary, allowed := store.begin(key, "execution:canary", cfg)
	if !allowed || canary.canaryToken == 0 {
		t.Fatalf("canary = %+v allowed=%t", canary, allowed)
	}
	store.abandon(canary, cfg)
	if store.candidateAllowed(key, "execution:next", cfg) {
		t.Fatal("next canary was allowed immediately after abandonment")
	}
	clock.Advance(11 * time.Second)
	if !store.candidateAllowed(key, "execution:next", cfg) {
		t.Fatal("next canary was not allowed after the new observation window")
	}
}

func TestCodexRateLimitContinuityActiveAttemptsDrainAfterChurn(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}

	for i := 0; i < 512; i++ {
		sessionID := "execution:churn-" + time.Unix(int64(i), 0).Format(time.RFC3339Nano)
		attempt, allowed := store.begin(key, sessionID, cfg)
		if !allowed || attempt.attemptToken == 0 {
			t.Fatalf("begin %d = %+v allowed=%t", i, attempt, allowed)
		}
		if i%2 == 0 {
			store.observe(attempt, true, "", cfg)
		} else {
			store.abandon(attempt, cfg)
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if got := len(store.activeAttempts); got != 0 {
		t.Fatalf("active attempts after churn = %d, want 0", got)
	}
	state := store.states[key]
	if state == nil {
		t.Fatal("continuity state missing after churn")
	}
	if got := len(state.inFlight); got != 0 {
		t.Fatalf("in-flight sessions after churn = %d, want 0", got)
	}
}

func TestCodexRateLimitContinuitySameSessionDifferentModelIsFresh(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	modelA := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-a"}
	modelB := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-b"}
	attemptA, _ := store.begin(modelA, "execution:same", cfg)
	store.observe(attemptA, true, "", cfg)
	attemptB, allowed := store.begin(modelB, "execution:same", cfg)
	if !allowed || attemptB.established {
		t.Fatalf("model B attempt = %+v allowed=%t, want fresh", attemptB, allowed)
	}
}

func TestCodexRateLimitContinuityFirstFreshFailureOnlyStartsObservation(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	fresh, allowed := store.begin(key, "execution:fresh", cfg)
	if !allowed || !fresh.incumbent {
		t.Fatalf("healthy fresh begin = %+v allowed=%t", fresh, allowed)
	}
	if got := store.observe(fresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg); got != codexRateLimitContinuityObserveOnly {
		t.Fatalf("first fresh failure = %v, want observe-only", got)
	}
	store.mu.Lock()
	state := store.states[key]
	store.mu.Unlock()
	if state == nil || state.phase != codexRateLimitContinuityFreshBlocked || state.confirmed {
		t.Fatalf("state after first fresh failure = %+v, want FreshBlocked", state)
	}
}

func TestCodexRateLimitContinuityCanaryFailureWithoutIncumbentConfirms(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	fresh, _ := store.begin(key, "execution:fresh", cfg)
	store.observe(fresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)
	clock.Advance(11 * time.Second)
	canary, allowed := store.begin(key, "execution:canary", cfg)
	if !allowed || canary.canaryToken == 0 {
		t.Fatalf("canary = %+v allowed=%t", canary, allowed)
	}
	store.observe(canary, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)
	store.mu.Lock()
	state := store.states[key]
	store.mu.Unlock()
	if state == nil || state.phase != codexRateLimitContinuityConfirmedCooldown || !state.confirmed {
		t.Fatalf("state after no-incumbent canary failure = %+v", state)
	}
	if _, allowed := store.begin(key, "execution:after-confirm", cfg); allowed {
		t.Fatal("confirmed cooldown allowed a dispatch")
	}
}

func TestCodexRateLimitContinuityPreBlockSuccessKeepsFreshBlocked(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	preBlock, _ := store.begin(key, "execution:pre-block", cfg)
	fresh, _ := store.begin(key, "execution:fresh", cfg)
	store.observe(fresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)
	store.observe(preBlock, true, "", cfg)
	store.mu.Lock()
	state := store.states[key]
	store.mu.Unlock()
	if state == nil || state.phase != codexRateLimitContinuityFreshBlocked {
		t.Fatalf("pre-block success recovered Healthy: %+v", state)
	}
	if expiresAt := state.establishedSessions["execution:pre-block"]; !expiresAt.After(clock.Now()) {
		t.Fatalf("pre-block success did not create lease: %+v", state)
	}
}

func TestCodexRateLimitContinuityPreBlockUsageLimitConfirmsCooldown(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	preBlock, _ := store.begin(key, "execution:pre-block", cfg)
	fresh, _ := store.begin(key, "execution:fresh", cfg)
	store.observe(fresh, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg)
	if got := store.observe(preBlock, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg); got != codexRateLimitContinuityNormal {
		t.Fatalf("pre-block usage-limit disposition = %v, want formal cooldown", got)
	}
	store.mu.Lock()
	state := store.states[key]
	store.mu.Unlock()
	if state == nil || state.phase != codexRateLimitContinuityConfirmedCooldown {
		t.Fatalf("pre-block usage-limit state = %+v, want ConfirmedCooldown", state)
	}
}

func TestCodexRateLimitContinuityFreshBlockedAllowsSameSessionInFlightContinuation(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	store.states[key] = &codexRateLimitContinuityState{
		phase: codexRateLimitContinuityFreshBlocked, suspect: true, generation: 1,
		observeUntil: clock.Now().Add(time.Hour), establishedSessions: make(map[string]time.Time),
		inFlight: map[string]int{"execution:incumbent": 1},
	}
	if allowed, established, _ := store.candidateDisposition(key, "execution:incumbent", cfg); !allowed || !established {
		t.Fatalf("candidate disposition = allowed=%t incumbent=%t", allowed, established)
	}
	attempt, allowed := store.begin(key, "execution:incumbent", cfg)
	if !allowed || !attempt.incumbent {
		t.Fatalf("same-session begin = %+v allowed=%t", attempt, allowed)
	}
	store.mu.Lock()
	count := store.states[key].inFlight["execution:incumbent"]
	store.mu.Unlock()
	if count != 2 {
		t.Fatalf("in-flight count = %d, want 2", count)
	}
	store.abandon(attempt, cfg)
}

func TestCodexRateLimitStableSessionIDPrefersAllowedConversationOverRequestID(t *testing.T) {
	opts := cliproxyexecutor.Options{
		Headers:         http.Header{"X-Client-Request-Id": []string{"ephemeral"}},
		OriginalRequest: []byte(`{"metadata":{"user_id":"generic"},"conversation_id":"conversation-1"}`),
	}
	if got := codexRateLimitStableSessionID(opts); got != "conv:conversation-1" {
		t.Fatalf("stable session id = %q, want conversation ID", got)
	}
	opts.Headers.Set("Session-Id", "codex-session")
	if got := codexRateLimitStableSessionID(opts); got != "codex:codex-session" {
		t.Fatalf("stable session id = %q, want Codex session header", got)
	}
}

func TestCodexRateLimitStableSessionIDMatchesSelectorClaudePriority(t *testing.T) {
	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Session-ID":        []string{"header-session"},
			"X-Client-Request-Id": []string{"ephemeral"},
		},
		OriginalRequest: []byte(`{"metadata":{"user_id":"user_hash_account__session_123e4567-e89b-12d3-a456-426614174000"},"conversation_id":"conversation-1"}`),
	}
	if got := codexRateLimitStableSessionID(opts); got != "claude:123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("stable session id = %q, want Claude session before header", got)
	}
}

func TestCodexRateLimitLeaseTTLClampsToEffectiveAffinityTTL(t *testing.T) {
	cfg := codexContinuityTestPolicy(2)
	cfg.EstablishedSessionTTLSeconds = 7200
	for _, tc := range []struct {
		name     string
		affinity string
		want     time.Duration
	}{
		{name: "empty uses selector default", want: time.Hour},
		{name: "invalid uses selector default", affinity: "not-a-duration", want: time.Hour},
		{name: "lower affinity", affinity: "30m", want: 30 * time.Minute},
		{name: "sub-second preserved", affinity: "500ms", want: 500 * time.Millisecond},
		{name: "larger affinity", affinity: "3h", want: 2 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexRateLimitEstablishedSessionTTLWithAffinity(cfg, tc.affinity); got != tc.want {
				t.Fatalf("lease TTL = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodexRateLimitContinuitySettingsChangedCoversLeaseContract(t *testing.T) {
	base := &internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true, SessionAffinityTTL: "1h"},
		Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
			Enabled:                      true,
			ObservationWindowSeconds:     10,
			EstablishedSuccessThreshold:  2,
			EstablishedSessionTTLSeconds: 3600,
		}},
	}
	if codexRateLimitContinuitySettingsChanged(base, base) {
		t.Fatal("unchanged config reported a continuity contract change")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*internalconfig.Config)
	}{
		{name: "enabled", mutate: func(cfg *internalconfig.Config) { cfg.Codex.RateLimitContinuity.Enabled = false }},
		{name: "observation window", mutate: func(cfg *internalconfig.Config) { cfg.Codex.RateLimitContinuity.ObservationWindowSeconds++ }},
		{name: "success threshold", mutate: func(cfg *internalconfig.Config) { cfg.Codex.RateLimitContinuity.EstablishedSuccessThreshold++ }},
		{name: "lease TTL", mutate: func(cfg *internalconfig.Config) { cfg.Codex.RateLimitContinuity.EstablishedSessionTTLSeconds++ }},
		{name: "home mode", mutate: func(cfg *internalconfig.Config) { cfg.Home.Enabled = true }},
		{name: "affinity enabled", mutate: func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinity = false }},
		{name: "affinity TTL", mutate: func(cfg *internalconfig.Config) { cfg.Routing.SessionAffinityTTL = "30m" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := *base
			tc.mutate(&changed)
			if !codexRateLimitContinuitySettingsChanged(base, &changed) {
				t.Fatalf("%s change did not invalidate continuity state", tc.name)
			}
		})
	}
}

func TestManagerCodexRateLimitContinuityPreservesSubSecondAffinityLeaseTTL(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true, SessionAffinityTTL: "500ms"},
		Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
			Enabled: true, ObservationWindowSeconds: 10, EstablishedSuccessThreshold: 2, EstablishedSessionTTLSeconds: 3600,
		}},
	})
	auth, _ := manager.GetByID("auth-a")
	ctx, allowed := manager.beginCodexRateLimitContinuityAttempt(context.Background(), auth, "codex", "gpt-5", codexContinuityOptions("subsecond"))
	if !allowed {
		t.Fatal("begin rejected")
	}
	attempt, ok := codexRateLimitContinuityAttemptFromContext(ctx)
	if !ok || attempt.leaseTTL != 500*time.Millisecond {
		t.Fatalf("attempt lease TTL = %+v, want 500ms", attempt)
	}
	manager.abandonCodexRateLimitContinuityAttempt(ctx)
}

func TestManagerCodexRateLimitContinuityUsesActualSelectorTTL(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	previous, _ := manager.selector.(*SessionAffinitySelector)
	manager.SetSelector(NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &FillFirstSelector{}, TTL: 125 * time.Millisecond}))
	if previous != nil {
		previous.Stop()
	}
	auth, _ := manager.GetByID("auth-a")
	ctx, allowed := manager.beginCodexRateLimitContinuityAttempt(context.Background(), auth, "codex", "gpt-5", codexContinuityOptions("selector-ttl"))
	if !allowed {
		t.Fatal("begin rejected")
	}
	attempt, _ := codexRateLimitContinuityAttemptFromContext(ctx)
	if attempt.leaseTTL != 125*time.Millisecond {
		t.Fatalf("actual selector TTL = %v, want 125ms", attempt.leaseTTL)
	}
	manager.abandonCodexRateLimitContinuityAttempt(ctx)
}

func TestManagerCodexRateLimitContinuityShorterSelectorReplacementClearsOldLease(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	sessionID := "selector-replacement"
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions(sessionID)); err != nil {
		t.Fatalf("establish Execute() error = %v", err)
	}

	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.mu.Lock()
	leaseBefore := manager.codexRateLimitContinuity.states[key].establishedSessions["execution:"+sessionID]
	manager.codexRateLimitContinuity.mu.Unlock()
	if leaseBefore.IsZero() {
		t.Fatal("expected established lease before selector replacement")
	}

	previous, _ := manager.selector.(*SessionAffinitySelector)
	manager.SetSelector(NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &FillFirstSelector{},
		TTL:      100 * time.Millisecond,
	}))
	if previous != nil {
		previous.Stop()
	}

	manager.codexRateLimitContinuity.mu.Lock()
	stateCount := len(manager.codexRateLimitContinuity.states)
	activeCount := len(manager.codexRateLimitContinuity.activeAttempts)
	manager.codexRateLimitContinuity.mu.Unlock()
	if stateCount != 0 || activeCount != 0 {
		t.Fatalf("selector replacement retained continuity state: states=%d active=%d", stateCount, activeCount)
	}
}

func TestCodexRateLimitContinuityLifecycleClearDoesNotResurrectFromInFlightResult(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	for _, tc := range []struct {
		name  string
		clear func(*codexRateLimitContinuityStore)
	}{
		{name: "global clear", clear: func(s *codexRateLimitContinuityStore) { s.clear() }},
		{name: "auth removal", clear: func(s *codexRateLimitContinuityStore) { s.removeAuth("auth-a") }},
		{name: "session close", clear: func(s *codexRateLimitContinuityStore) { s.removeSession("execution:in-flight") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newCodexContinuityTestStore(clock)
			attempt, allowed := store.begin(key, "execution:in-flight", cfg)
			if !allowed || attempt.attemptToken == 0 {
				t.Fatalf("begin = %+v allowed=%t", attempt, allowed)
			}
			tc.clear(store)
			if got := store.observe(attempt, true, "", cfg); got != codexRateLimitContinuityRecordOnly {
				t.Fatalf("in-flight completion disposition = %v, want record-only", got)
			}
			store.mu.Lock()
			state, exists := store.states[key]
			leaseExists := false
			if state != nil {
				_, leaseExists = state.establishedSessions["execution:in-flight"]
			}
			store.mu.Unlock()
			if tc.name == "session close" {
				if leaseExists {
					t.Fatal("session-close stale completion recreated continuity lease")
				}
			} else if exists {
				t.Fatal("stale in-flight completion recreated continuity state")
			}
		})
	}
}

func TestManagerCodexRateLimitContinuityLifecycleEntrypointsDoNotResurrectInFlightLease(t *testing.T) {
	newAttempt := func(t *testing.T) (*Manager, context.Context, codexRateLimitContinuityKey) {
		t.Helper()
		executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
		manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
		auth, _ := manager.GetByID("auth-a")
		ctx, allowed := manager.beginCodexRateLimitContinuityAttempt(context.Background(), auth, "codex", "gpt-5", codexContinuityOptions("entrypoint"))
		if !allowed {
			t.Fatal("begin rejected")
		}
		return manager, ctx, codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	}
	for _, tc := range []struct {
		name  string
		clear func(*Manager)
	}{
		{name: "session close", clear: func(m *Manager) { m.CloseExecutionSession("entrypoint") }},
		{name: "hot reload", clear: func(m *Manager) {
			m.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SessionAffinity: true, SessionAffinityTTL: "10m"}, Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{Enabled: true, ObservationWindowSeconds: 11, EstablishedSuccessThreshold: 2, EstablishedSessionTTLSeconds: 3600}}})
		}},
		{name: "load", clear: func(m *Manager) {
			m.SetStore(&countingStore{})
			if err := m.Load(context.Background()); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
		}},
		{name: "auth removal", clear: func(m *Manager) { m.invalidateSessionAffinity("auth-a") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, ctx, key := newAttempt(t)
			tc.clear(manager)
			manager.MarkResult(ctx, Result{AuthID: "auth-a", Provider: "codex", Model: "gpt-5", Success: true})
			manager.codexRateLimitContinuity.mu.Lock()
			state := manager.codexRateLimitContinuity.states[key]
			lease := time.Time{}
			if state != nil {
				lease = state.establishedSessions["execution:entrypoint"]
			}
			manager.codexRateLimitContinuity.mu.Unlock()
			if !lease.IsZero() {
				t.Fatalf("%s stale completion recreated lease: %v", tc.name, lease)
			}
		})
	}
}

func TestManagerCodexRateLimitContinuityLifecycleResetAfterAdmissionBlocksDispatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reset func(*testing.T, *Manager, string)
	}{
		{
			name: "load",
			reset: func(t *testing.T, manager *Manager, _ string) {
				t.Helper()
				auth, ok := manager.GetByID("auth-a")
				if !ok || auth == nil {
					t.Fatal("auth-a missing before Load")
				}
				manager.SetStore(&codexContinuityReloadStore{auth: auth})
				if err := manager.Load(context.Background()); err != nil {
					t.Fatalf("Load() error = %v", err)
				}
			},
		},
		{
			name: "remove",
			reset: func(_ *testing.T, manager *Manager, _ string) {
				manager.Remove(context.Background(), "auth-a")
			},
		},
		{
			name: "close execution session",
			reset: func(_ *testing.T, manager *Manager, sessionID string) {
				manager.CloseExecutionSession(sessionID)
			},
		},
		{
			name: "changed config",
			reset: func(_ *testing.T, manager *Manager, _ string) {
				manager.SetConfig(&internalconfig.Config{
					Routing: internalconfig.RoutingConfig{SessionAffinity: true},
					Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
						Enabled:                      true,
						ObservationWindowSeconds:     11,
						EstablishedSuccessThreshold:  2,
						EstablishedSessionTTLSeconds: 3600,
					}},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
			manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
			sessionID := "lifecycle-race"
			if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions(sessionID)); err != nil {
				t.Fatalf("establish Execute() error = %v", err)
			}

			beforeCalls := len(executor.snapshot())
			var callbackCalls atomic.Int32
			opts := codexContinuityOptions(sessionID)
			opts.Metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(string) {
				callbackCalls.Add(1)
			}
			var once sync.Once
			manager.continuityBeforeDispatchHook = func() {
				once.Do(func() { tc.reset(t, manager, sessionID) })
			}
			_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, opts)
			manager.continuityBeforeDispatchHook = nil
			if err == nil {
				t.Fatal("Execute() error = nil after lifecycle reset")
			}
			if got := len(executor.snapshot()); got != beforeCalls {
				t.Fatalf("executor calls after lifecycle reset = %d, want %d", got, beforeCalls)
			}
			if got := callbackCalls.Load(); got != 0 {
				t.Fatalf("selected-auth callback calls = %d, want 0", got)
			}
			if _, published := opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; published {
				t.Fatal("selected auth metadata was published for zero-dispatch request")
			}

			key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
			manager.codexRateLimitContinuity.mu.Lock()
			state := manager.codexRateLimitContinuity.states[key]
			lease := time.Time{}
			inFlight := 0
			if state != nil {
				lease = state.establishedSessions["execution:"+sessionID]
				inFlight = state.inFlight["execution:"+sessionID]
			}
			activeAttempts := len(manager.codexRateLimitContinuity.activeAttempts)
			manager.codexRateLimitContinuity.mu.Unlock()
			if !lease.IsZero() || inFlight != 0 || activeAttempts != 0 {
				t.Fatalf("lifecycle reset resurrected continuity state: lease=%v in-flight=%d active=%d", lease, inFlight, activeAttempts)
			}
		})
	}
}

func TestManagerCodexRateLimitContinuityInactiveScopesBypassLifecycleAndCanonicalGuards(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *internalconfig.Config
		opts cliproxyexecutor.Options
		want bool
	}{
		{
			name: "default off",
			cfg:  &internalconfig.Config{Routing: internalconfig.RoutingConfig{SessionAffinity: true}},
			opts: codexContinuityOptions("default-off"),
			want: true,
		},
		{
			name: "home runtime auth",
			cfg: &internalconfig.Config{
				Home:    internalconfig.HomeConfig{Enabled: true},
				Routing: internalconfig.RoutingConfig{SessionAffinity: true},
				Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
					Enabled: true,
				}},
			},
			opts: codexContinuityOptions("home"),
			want: true,
		},
		{
			name: "no stable session",
			cfg: &internalconfig.Config{
				Routing: internalconfig.RoutingConfig{SessionAffinity: true},
				Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
					Enabled: true,
				}},
			},
			opts: cliproxyexecutor.Options{},
			want: true,
		},
		{
			name: "active continuity requires canonical auth",
			cfg: &internalconfig.Config{
				Routing: internalconfig.RoutingConfig{SessionAffinity: true},
				Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
					Enabled: true,
				}},
			},
			opts: codexContinuityOptions("active"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector := NewSessionAffinitySelector(&FillFirstSelector{})
			manager := NewManager(nil, selector, nil)
			t.Cleanup(selector.Stop)
			manager.SetConfig(tc.cfg)
			ctx := manager.withCodexRateLimitContinuityLifecycle(context.Background())
			manager.mu.Lock()
			manager.advanceCodexRateLimitContinuityLifecycleLocked()
			manager.mu.Unlock()
			_, allowed := manager.beginCodexRateLimitContinuityAttempt(ctx, &Auth{ID: "runtime-only", Provider: "codex"}, "codex", "gpt-5", tc.opts)
			if allowed != tc.want {
				t.Fatalf("begin allowed = %t, want %t", allowed, tc.want)
			}
		})
	}
}

func TestManagerCodexRateLimitContinuityHomeCodexDispatchBypassesContinuity(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := NewManager(nil, NewSessionAffinitySelector(&FillFirstSelector{}), nil)
	t.Cleanup(func() {
		if selector, ok := manager.selector.(*SessionAffinitySelector); ok {
			selector.Stop()
		}
	})
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		Home:    internalconfig.HomeConfig{Enabled: true},
		Routing: internalconfig.RoutingConfig{SessionAffinity: true},
		Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
			Enabled: true,
		}},
	})
	manager.RegisterExecutor(executor)
	sessionID := "home-codex"
	homeAuth := &Auth{
		ID:       "home-codex-auth",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"websockets":                  "true",
			homeUpstreamModelAttributeKey: "gpt-5",
		},
	}
	homeAuth.EnsureIndex()
	manager.rememberHomeRuntimeAuth(sessionID, homeAuth)

	opts := codexContinuityOptions(sessionID)
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = homeAuth.ID
	resp, err := manager.Execute(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, opts)
	if err != nil || string(resp.Payload) != "home-codex-auth|gpt-5|home-codex" {
		t.Fatalf("Execute() response/error = %q/%v", resp.Payload, err)
	}
	if calls := executor.snapshot(); len(calls) != 1 || calls[0] != "home-codex-auth|gpt-5|home-codex" {
		t.Fatalf("executor calls = %#v, want one Home Codex dispatch", calls)
	}
}

func TestManagerCodexRateLimitContinuityUnrelatedSessionCloseDoesNotBlockDispatch(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	var once sync.Once
	manager.continuityBeforeDispatchHook = func() {
		once.Do(func() { manager.CloseExecutionSession("unrelated") })
	}
	defer func() { manager.continuityBeforeDispatchHook = nil }()
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("session-b"))
	if err != nil || string(resp.Payload) != "auth-a|gpt-5|session-b" {
		t.Fatalf("Execute() response/error = %q/%v", resp.Payload, err)
	}
	if calls := executor.snapshot(); len(calls) != 1 {
		t.Fatalf("executor calls = %#v, want one dispatch", calls)
	}
}

func TestManagerCodexRateLimitContinuityUnrelatedAuthRemovalDoesNotBlockDispatch(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a", "auth-b"}, []string{"gpt-5"}, 2)
	var once sync.Once
	manager.continuityBeforeDispatchHook = func() {
		once.Do(func() { manager.Remove(context.Background(), "auth-a") })
	}
	defer func() { manager.continuityBeforeDispatchHook = nil }()
	opts := codexContinuityOptions("session-b")
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = "auth-b"
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, opts)
	if err != nil || string(resp.Payload) != "auth-b|gpt-5|session-b" {
		t.Fatalf("Execute() response/error = %q/%v", resp.Payload, err)
	}
	if calls := executor.snapshot(); len(calls) != 1 || calls[0] != "auth-b|gpt-5|session-b" {
		t.Fatalf("executor calls = %#v, want auth-b only", calls)
	}
}

func TestCodexRateLimitContinuitySessionCloseRemovesLease(t *testing.T) {
	store := newCodexRateLimitContinuityStore()
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	store.states[key] = &codexRateLimitContinuityState{
		phase:               codexRateLimitContinuityFreshBlocked,
		generation:          1,
		establishedSessions: map[string]time.Time{"execution:one": time.Now().Add(time.Hour)},
		inFlight:            map[string]int{"execution:one": 1},
		canaryToken:         7,
	}
	store.activeAttempts[9] = codexRateLimitContinuityAttempt{
		key: key, sessionID: "execution:one", generation: 1, canaryToken: 7, attemptToken: 9,
	}
	store.removeSession("execution:one")
	store.mu.Lock()
	state := store.states[key]
	_, exists := state.establishedSessions["execution:one"]
	inFlight := state.inFlight["execution:one"]
	canaryToken := state.canaryToken
	activeAttempts := len(store.activeAttempts)
	store.mu.Unlock()
	if exists || inFlight != 0 || canaryToken != 0 || activeAttempts != 0 {
		t.Fatalf("CloseExecutionSession retained state: lease=%t in-flight=%d canary=%d active=%d", exists, inFlight, canaryToken, activeAttempts)
	}
}

func TestCodexRateLimitContinuityStaleSuccessAndUsageLimitAreRecordOnly(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	store.states[key] = &codexRateLimitContinuityState{
		phase:               codexRateLimitContinuityConfirmedCooldown,
		confirmed:           true,
		generation:          2,
		establishedSessions: make(map[string]time.Time),
	}
	for _, result := range []struct {
		name    string
		success bool
		reason  string
	}{
		{name: "success", success: true},
		{name: "usage-limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit},
	} {
		t.Run(result.name, func(t *testing.T) {
			attempt := codexRateLimitContinuityAttempt{key: key, sessionID: "execution:stale-" + result.name, generation: 1}
			if got := store.observe(attempt, result.success, result.reason, cfg); got != codexRateLimitContinuityRecordOnly {
				t.Fatalf("stale %s = %v, want record-only", result.name, got)
			}
		})
	}
}

func TestCodexRateLimitContinuityStaleNonQuotaFailureStillMutatesAvailability(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		phase:               codexRateLimitContinuityConfirmedCooldown,
		confirmed:           true,
		generation:          2,
		establishedSessions: make(map[string]time.Time),
	}
	ctx := context.WithValue(context.Background(), codexRateLimitContinuityAttemptContextKey{}, codexRateLimitContinuityAttempt{
		key: key, sessionID: "execution:stale-401", generation: 1,
	})
	manager.MarkResult(ctx, Result{AuthID: "auth-a", Provider: "codex", Model: "gpt-5", Success: false, Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired"}})
	auth, _ := manager.GetByID("auth-a")
	state := auth.ModelStates["gpt-5"]
	if state == nil || state.NextRetryAfter.IsZero() || !state.Unavailable {
		t.Fatalf("stale 401 did not mutate availability: %+v", state)
	}
}

func TestCodexRateLimitContinuityStaleInvalidGrantStillMutatesAvailability(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		phase:               codexRateLimitContinuityConfirmedCooldown,
		confirmed:           true,
		generation:          2,
		establishedSessions: make(map[string]time.Time),
	}
	ctx := context.WithValue(context.Background(), codexRateLimitContinuityAttemptContextKey{}, codexRateLimitContinuityAttempt{
		key: key, sessionID: "execution:stale-invalid-grant", generation: 1,
	})
	manager.MarkResult(ctx, Result{
		AuthID: "auth-a", Provider: "codex", Model: "gpt-5", Success: false,
		Error: &Error{HTTPStatus: http.StatusBadRequest, Code: "invalid_grant", Message: "invalid_grant"},
	})
	auth, _ := manager.GetByID("auth-a")
	state := auth.ModelStates["gpt-5"]
	if state == nil || state.NextRetryAfter.IsZero() || !state.Unavailable || !strings.Contains(strings.ToLower(state.StatusMessage), "invalid_grant") {
		t.Fatalf("stale invalid_grant did not mutate availability: %+v", state)
	}
}

func TestCodexRateLimitContinuityConfirmedRecoveryReopensAsSingleCanary(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	store.states[key] = &codexRateLimitContinuityState{
		phase:      codexRateLimitContinuityConfirmedCooldown,
		confirmed:  true,
		generation: 4,
		establishedSessions: map[string]time.Time{
			"execution:old-one": clock.Now().Add(time.Hour),
			"execution:old-two": clock.Now().Add(time.Hour),
		},
		inFlight: map[string]int{"execution:old-three": 1},
	}
	store.reopenConfirmed(key, 4)
	store.mu.Lock()
	state := store.states[key]
	oldLeases := len(state.establishedSessions)
	oldInflight := len(state.inFlight)
	store.mu.Unlock()
	if oldLeases != 0 || oldInflight != 0 {
		t.Fatalf("confirmed recovery retained stale continuity evidence: leases=%d in-flight=%d", oldLeases, oldInflight)
	}
	first, allowed := store.begin(key, "execution:recover-1", cfg)
	if !allowed || first.canaryToken == 0 {
		t.Fatalf("recovery attempt = %+v allowed=%t, want one canary", first, allowed)
	}
	if _, allowed := store.begin(key, "execution:recover-2", cfg); allowed {
		t.Fatal("confirmed recovery admitted a second fresh request")
	}
}

func TestCodexRateLimitContinuityDispatchRecheckRejectsConfirmedBarrier(t *testing.T) {
	store := newCodexRateLimitContinuityStore()
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	attempt := codexRateLimitContinuityAttempt{key: key, sessionID: "execution:barrier", generation: 1}
	store.states[key] = &codexRateLimitContinuityState{
		phase:               codexRateLimitContinuityConfirmedCooldown,
		confirmed:           true,
		generation:          2,
		establishedSessions: make(map[string]time.Time),
	}
	if allowed, _ := store.dispatchAllowed(attempt); allowed {
		t.Fatal("dispatch recheck admitted request after confirmation")
	}
}

func TestCodexRateLimitContinuityDispatchRecheckReleasesInFlightAttempt(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	attempt, allowed := store.begin(key, "execution:recheck", cfg)
	if !allowed {
		t.Fatal("healthy begin rejected")
	}
	store.mu.Lock()
	state := store.states[key]
	setCodexRateLimitContinuityPhase(state, codexRateLimitContinuityConfirmedCooldown)
	state.generation++
	store.mu.Unlock()
	if allowed, confirmed := store.dispatchAllowed(attempt); allowed || !confirmed {
		t.Fatalf("dispatch disposition = allowed=%t confirmed=%t, want rejected confirmed", allowed, confirmed)
	}
	store.abandon(attempt, cfg)
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.states[key].inFlight["execution:recheck"]; got != 0 {
		t.Fatalf("in-flight after rejected dispatch = %d, want 0", got)
	}
	if got := len(store.activeAttempts); got != 0 {
		t.Fatalf("active attempts after rejected dispatch = %d, want 0", got)
	}
}

func TestManagerCodexRateLimitContinuityConfirmationBarrierBlocksDispatchAndStaleSuccess(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		phase: codexRateLimitContinuityFreshBlocked, suspect: true, generation: 1,
		establishedSessions: map[string]time.Time{"execution:established": time.Now().Add(time.Hour)},
	}
	confirmCtx := context.WithValue(context.Background(), codexRateLimitContinuityAttemptContextKey{}, codexRateLimitContinuityAttempt{
		key: key, sessionID: "execution:established", generation: 1, established: true,
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	manager.continuityTransitionHook = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	marked := make(chan struct{})
	go func() {
		defer close(marked)
		manager.MarkResult(confirmCtx, Result{
			AuthID: "auth-a", Provider: "codex", Model: "gpt-5", Success: false,
			Error:               &Error{HTTPStatus: http.StatusTooManyRequests, Message: "limit"},
			ModelFallbackReason: internalconfig.CodexModelFallbackTriggerUsageLimit,
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation did not reach manager-lock barrier")
	}
	competingDone := make(chan error, 1)
	go func() {
		_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("racing-fresh"))
		competingDone <- err
	}()
	select {
	case err := <-competingDone:
		t.Fatalf("competing Execute returned before confirmation barrier release: %v", err)
	case <-time.After(100 * time.Millisecond):
		if got := len(executor.snapshot()); got != 0 {
			t.Fatalf("competing request dispatched during confirmation barrier: %#v", executor.snapshot())
		}
	}
	close(release)
	<-marked
	if err := <-competingDone; err == nil {
		t.Fatal("competing Execute unexpectedly succeeded after confirmed cooldown")
	}
	if got := len(executor.snapshot()); got != 0 {
		t.Fatalf("competing request dispatched after confirmed cooldown: %#v", executor.snapshot())
	}
	manager.continuityTransitionHook = nil
	manager.MarkResult(confirmCtx, Result{AuthID: "auth-a", Provider: "codex", Model: "gpt-5", Success: true})
	auth, _ := manager.GetByID("auth-a")
	state := auth.ModelStates["gpt-5"]
	if state == nil || !state.Quota.Exceeded || state.NextRetryAfter.IsZero() {
		t.Fatalf("stale success cleared confirmed cooldown: %+v", state)
	}
}

func TestCodexRateLimitContinuityPrunesOnlyAtNextLeaseExpiry(t *testing.T) {
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	store := newCodexContinuityTestStore(clock)
	cfg := codexContinuityTestPolicy(2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	firstExpiry := clock.Now().Add(time.Hour)
	sessions := make(map[string]time.Time, 2048)
	for i := 0; i < 2048; i++ {
		sessions["execution:lease-"+time.Unix(int64(i), 0).Format(time.RFC3339Nano)] = firstExpiry.Add(time.Duration(i) * time.Second)
	}
	store.states[key] = &codexRateLimitContinuityState{
		establishedSessions: sessions,
		nextLeaseExpiry:     firstExpiry,
	}
	for i := 0; i < 32; i++ {
		store.candidateAllowed(key, "execution:fresh", cfg)
	}
	store.mu.Lock()
	scans := store.states[key].leasePruneScans
	store.mu.Unlock()
	if scans != 0 {
		t.Fatalf("lease pruning scanned before next expiry %d times", scans)
	}
	clock.Advance(time.Hour + 500*time.Millisecond)
	store.candidateAllowed(key, "execution:fresh", cfg)
	store.mu.Lock()
	state := store.states[key]
	scans = state.leasePruneScans
	remaining := len(state.establishedSessions)
	nextExpiry := state.nextLeaseExpiry
	store.mu.Unlock()
	if scans != 1 {
		t.Fatalf("lease pruning scans after expiry = %d, want 1", scans)
	}
	if remaining != 2047 || !nextExpiry.Equal(firstExpiry.Add(time.Second)) {
		t.Fatalf("lease pruning result: remaining=%d next=%v", remaining, nextExpiry)
	}
}

func TestManagerCodexRateLimitContinuityCloseLoadAndAuthRemovalClearState(t *testing.T) {
	store := &countingStore{}
	manager := NewManager(store, NewSessionAffinitySelector(&FillFirstSelector{}), nil)
	t.Cleanup(func() {
		if selector, ok := manager.selector.(*SessionAffinitySelector); ok {
			selector.Stop()
		}
	})
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	putState := func() {
		manager.codexRateLimitContinuity.mu.Lock()
		manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{establishedSessions: map[string]time.Time{"execution:close": time.Now().Add(time.Hour)}}
		manager.codexRateLimitContinuity.mu.Unlock()
	}
	putState()
	manager.CloseExecutionSession("close")
	manager.codexRateLimitContinuity.mu.Lock()
	_, leased := manager.codexRateLimitContinuity.states[key].establishedSessions["execution:close"]
	manager.codexRateLimitContinuity.mu.Unlock()
	if leased {
		t.Fatal("CloseExecutionSession did not clear continuity lease")
	}
	putState()
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	manager.codexRateLimitContinuity.mu.Lock()
	count := len(manager.codexRateLimitContinuity.states)
	manager.codexRateLimitContinuity.mu.Unlock()
	if count != 0 {
		t.Fatalf("Load retained %d continuity states", count)
	}
	putState()
	manager.invalidateSessionAffinity("auth-a")
	manager.codexRateLimitContinuity.mu.Lock()
	count = len(manager.codexRateLimitContinuity.states)
	manager.codexRateLimitContinuity.mu.Unlock()
	if count != 0 {
		t.Fatalf("auth removal retained %d continuity states", count)
	}
}

type codexContinuityTestExecutor struct {
	mu              sync.Mutex
	calls           []string
	failures        map[string]error
	streamPayloads  map[string][]cliproxyexecutor.StreamChunk
	streamStarted   chan struct{}
	streamRelease   chan struct{}
	blockExecuteKey string
	executeStarted  chan struct{}
	executeRelease  chan struct{}
}

func (e *codexContinuityTestExecutor) Identifier() string { return "codex" }

func codexContinuitySession(opts cliproxyexecutor.Options) string {
	if sessionID := contextStringValue(opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "conversation_id").String())
}

func (e *codexContinuityTestExecutor) callKey(auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	return authID + "|" + req.Model + "|" + codexContinuitySession(opts)
}

func (e *codexContinuityTestExecutor) record(key string) {
	e.mu.Lock()
	e.calls = append(e.calls, key)
	e.mu.Unlock()
}

func (e *codexContinuityTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	key := e.callKey(auth, req, opts)
	e.record(key)
	if key == e.blockExecuteKey && e.executeRelease != nil {
		if e.executeStarted != nil {
			select {
			case e.executeStarted <- struct{}{}:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return cliproxyexecutor.Response{}, ctx.Err()
		case <-e.executeRelease:
		}
	}
	if err := e.failures[key]; err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(key)}, nil
}

func (e *codexContinuityTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	key := e.callKey(auth, req, opts)
	e.record(key)
	if chunks, ok := e.streamPayloads[key]; ok {
		out := make(chan cliproxyexecutor.StreamChunk)
		go func() {
			defer close(out)
			if e.streamStarted != nil {
				select {
				case e.streamStarted <- struct{}{}:
				default:
				}
			}
			for index, chunk := range chunks {
				if index == 1 && e.streamRelease != nil {
					select {
					case <-ctx.Done():
						return
					case <-e.streamRelease:
					}
				}
				select {
				case <-ctx.Done():
					return
				case out <- chunk:
				}
			}
		}()
		return &cliproxyexecutor.StreamResult{Chunks: out}, nil
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if err := e.failures[key]; err != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: err}
	} else {
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(key)}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *codexContinuityTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *codexContinuityTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *codexContinuityTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *codexContinuityTestExecutor) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func newCodexContinuityManager(t *testing.T, executor *codexContinuityTestExecutor, authIDs, models []string, threshold int) *Manager {
	t.Helper()
	manager := NewManager(nil, NewSessionAffinitySelector(&FillFirstSelector{}), nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true},
		Codex: internalconfig.CodexConfig{RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
			Enabled:                      true,
			ObservationWindowSeconds:     10,
			EstablishedSuccessThreshold:  threshold,
			EstablishedSessionTTLSeconds: 3600,
		}},
	})
	manager.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	for _, authID := range authIDs {
		infos := make([]*registry.ModelInfo, 0, len(models))
		for _, model := range models {
			infos = append(infos, &registry.ModelInfo{ID: model})
		}
		reg.RegisterClient(authID, "codex", infos)
		if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatalf("Register(%s) error = %v", authID, err)
		}
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			reg.UnregisterClient(authID)
		}
		if selector, ok := manager.selector.(*SessionAffinitySelector); ok {
			selector.Stop()
		}
	})
	return manager
}

func codexContinuityOptions(sessionID string) cliproxyexecutor.Options {
	metadata := map[string]any{}
	if sessionID != "" {
		metadata[cliproxyexecutor.ExecutionSessionMetadataKey] = sessionID
	}
	return cliproxyexecutor.Options{Metadata: metadata}
}

func codexContinuityConversationOptions(conversationID string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation_id":"` + conversationID + `"}`)}
}

func TestManagerSelectAuthForRequestReturnsContinuityContext(t *testing.T) {
	manager := newCodexContinuityManager(
		t,
		&codexContinuityTestExecutor{},
		[]string{"auth-a"},
		[]string{"gpt-5"},
		1,
	)
	selected, resultCtx, err := manager.SelectAuthForRequest(
		context.Background(),
		"codex",
		"gpt-5",
		codexContinuityOptions("search-session"),
	)
	if err != nil {
		t.Fatalf("SelectAuthForRequest() error = %v", err)
	}
	if selected == nil || selected.ID != "auth-a" {
		t.Fatalf("selected = %#v, want auth-a", selected)
	}
	attempt, ok := codexRateLimitContinuityAttemptFromContext(resultCtx)
	if !ok {
		t.Fatal("SelectAuthForRequest() did not return a continuity attempt context")
	}
	lifecycle, hasLifecycle := codexRateLimitContinuityLifecycleFromContext(resultCtx)
	if !hasLifecycle || lifecycle != manager.codexRateLimitContinuityLifecycle {
		t.Fatalf("request lifecycle = %d present=%t, want current lifecycle %d", lifecycle, hasLifecycle, manager.codexRateLimitContinuityLifecycle)
	}
	if attempt.key.authID != selected.ID || attempt.key.model != "gpt-5" || attempt.sessionID != "execution:search-session" {
		t.Fatalf("attempt = %#v, want auth/model/session binding", attempt)
	}
	manager.MarkResult(resultCtx, Result{
		AuthID:   selected.ID,
		Provider: "codex",
		Model:    "gpt-5",
		Success:  true,
	})
}

func TestManagerConfirmSelectedAuthDispatchRejectsConfirmedBarrierAndAbandons(t *testing.T) {
	manager := newCodexContinuityManager(
		t,
		&codexContinuityTestExecutor{},
		[]string{"auth-a"},
		[]string{"gpt-5"},
		1,
	)
	_, resultCtx, err := manager.SelectAuthForRequest(
		context.Background(),
		"codex",
		"gpt-5",
		codexContinuityOptions("search-session"),
	)
	if err != nil {
		t.Fatalf("SelectAuthForRequest() error = %v", err)
	}
	attempt, ok := codexRateLimitContinuityAttemptFromContext(resultCtx)
	if !ok {
		t.Fatal("SelectAuthForRequest() did not return a continuity attempt")
	}
	manager.codexRateLimitContinuity.mu.Lock()
	state := manager.codexRateLimitContinuity.states[attempt.key]
	setCodexRateLimitContinuityPhase(state, codexRateLimitContinuityConfirmedCooldown)
	manager.codexRateLimitContinuity.mu.Unlock()

	err = manager.ConfirmSelectedAuthDispatch(resultCtx)
	if err == nil {
		t.Fatal("ConfirmSelectedAuthDispatch() succeeded after confirmed cooldown")
	}
	statusError, ok := err.(interface{ StatusCode() int })
	if !ok || statusError.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("ConfirmSelectedAuthDispatch() error = %#v, want HTTP 429", err)
	}
	manager.codexRateLimitContinuity.mu.Lock()
	defer manager.codexRateLimitContinuity.mu.Unlock()
	if got := len(manager.codexRateLimitContinuity.activeAttempts); got != 0 {
		t.Fatalf("active attempts after rejected dispatch = %d, want 0", got)
	}
	if got := state.inFlight[attempt.sessionID]; got != 0 {
		t.Fatalf("in-flight after rejected dispatch = %d, want 0", got)
	}
}

func TestManagerConfirmSelectedAuthDispatchRejectsLifecycleReset(t *testing.T) {
	manager := newCodexContinuityManager(
		t,
		&codexContinuityTestExecutor{},
		[]string{"auth-a"},
		[]string{"gpt-5"},
		1,
	)
	_, resultCtx, err := manager.SelectAuthForRequest(
		context.Background(),
		"codex",
		"gpt-5",
		codexContinuityOptions("search-session"),
	)
	if err != nil {
		t.Fatalf("SelectAuthForRequest() error = %v", err)
	}
	manager.mu.Lock()
	manager.advanceCodexRateLimitContinuityLifecycleLocked()
	manager.codexRateLimitContinuity.clear()
	manager.mu.Unlock()

	if err = manager.ConfirmSelectedAuthDispatch(resultCtx); err == nil {
		t.Fatal("ConfirmSelectedAuthDispatch() succeeded after lifecycle reset")
	}
}

func TestManagerCodexRateLimitContinuityKeepsEstablishedSessionAndExcludesFresh(t *testing.T) {
	usageLimit := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexContinuityTestExecutor{failures: map[string]error{
		"auth-a|gpt-5|fresh-b": usageLimit,
	}}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a", "auth-b"}, []string{"gpt-5"}, 2)

	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established-a")); err != nil {
		t.Fatalf("establish Execute() error = %v", err)
	}
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("fresh-b")); err != nil {
		t.Fatalf("fresh-b Execute() error = %v", err)
	}
	authA, _ := manager.GetByID("auth-a")
	if state := authA.ModelStates["gpt-5"]; state != nil && state.Quota.Exceeded {
		t.Fatalf("auth-a entered formal cooldown: %+v", state)
	}
	manager.mu.Lock()
	manager.auths["auth-a"].Attributes = map[string]string{"priority": "0"}
	manager.auths["auth-b"].Attributes = map[string]string{"priority": "10"}
	manager.mu.Unlock()
	before := len(executor.snapshot())
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("fresh-c")); err != nil {
		t.Fatalf("fresh-c Execute() error = %v", err)
	}
	calls := executor.snapshot()[before:]
	if len(calls) != 1 || calls[0] != "auth-b|gpt-5|fresh-c" {
		t.Fatalf("fresh-c calls = %#v, want auth-b only", calls)
	}
	before = len(executor.snapshot())
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established-a")); err != nil {
		t.Fatalf("established Execute() error = %v", err)
	}
	calls = executor.snapshot()[before:]
	if len(calls) != 1 || calls[0] != "auth-a|gpt-5|established-a" {
		t.Fatalf("established calls = %#v, want sticky auth-a", calls)
	}
}

func TestManagerCodexRateLimitContinuityRepeatedCanaryFailuresPreserveEstablishedSession(t *testing.T) {
	usageLimit := &codexContinuityUsageLimitError{}
	executor := &codexContinuityTestExecutor{failures: map[string]error{
		"auth-a|gpt-5|fresh-initial": usageLimit,
		"auth-a|gpt-5|canary-0":      usageLimit,
		"auth-a|gpt-5|canary-1":      usageLimit,
		"auth-a|gpt-5|canary-2":      usageLimit,
	}}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	manager.codexRateLimitContinuity.now = clock.Now

	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established")); err != nil {
		t.Fatalf("establish Execute() error = %v", err)
	}
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("fresh-initial")); err == nil {
		t.Fatal("initial fresh usage-limit error = nil")
	}

	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	for i := 0; i < 3; i++ {
		clock.Advance(11 * time.Second)
		canarySession := "canary-" + string(rune('0'+i))
		if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions(canarySession)); err == nil {
			t.Fatalf("canary %d usage-limit error = nil", i)
		}

		auth, _ := manager.GetByID("auth-a")
		if modelState := auth.ModelStates["gpt-5"]; modelState != nil && (modelState.Quota.Exceeded || !modelState.NextRetryAfter.IsZero()) {
			t.Fatalf("canary %d wrote formal cooldown: %+v", i, modelState)
		}
		manager.codexRateLimitContinuity.mu.Lock()
		state := manager.codexRateLimitContinuity.states[key]
		phase := codexRateLimitContinuityHealthy
		lease := time.Time{}
		if state != nil {
			phase = state.phase
			lease = state.establishedSessions["execution:established"]
		}
		manager.codexRateLimitContinuity.mu.Unlock()
		if state == nil || phase != codexRateLimitContinuityFreshBlocked || lease.IsZero() {
			t.Fatalf("canary %d state = %+v lease=%v, want FreshBlocked with incumbent lease", i, state, lease)
		}

		if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established")); err != nil {
			t.Fatalf("established Execute() after canary %d error = %v", i, err)
		}
	}
}

func TestManagerCodexRateLimitContinuityEstablishedFailureConfirmsCooldown(t *testing.T) {
	usageLimit := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established")); err != nil {
		t.Fatalf("establish error = %v", err)
	}
	executor.failures["auth-a|gpt-5|fresh"] = usageLimit
	_, _ = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("fresh"))
	executor.failures["auth-a|gpt-5|established"] = usageLimit
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established"))
	if err == nil {
		t.Fatal("established failure error = nil")
	}
	authA, _ := manager.GetByID("auth-a")
	state := authA.ModelStates["gpt-5"]
	if state == nil || !state.Quota.Exceeded || state.Quota.BackoffLevel != 1 {
		t.Fatalf("confirmed state = %+v", state)
	}
}

func TestManagerCodexRateLimitContinuityCanaryFailureBacksOffOnce(t *testing.T) {
	usageLimit := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexContinuityTestExecutor{failures: map[string]error{"auth-a|gpt-5|canary": usageLimit}}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	manager.codexRateLimitContinuity.now = clock.Now
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		suspect:             true,
		observeUntil:        clock.Now().Add(-time.Second),
		establishedSessions: make(map[string]time.Time),
	}
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("canary"))
	if err == nil {
		t.Fatal("canary error = nil")
	}
	authA, _ := manager.GetByID("auth-a")
	state := authA.ModelStates["gpt-5"]
	if state == nil || state.Quota.BackoffLevel != 1 {
		t.Fatalf("canary state = %+v, want backoff level 1", state)
	}
	if got := len(executor.snapshot()); got != 1 {
		t.Fatalf("executor calls = %d, want 1", got)
	}
}

func TestManagerCodexRateLimitContinuityStaleSuccessDoesNotClearConfirmedCooldown(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	now := time.Now()
	manager.mu.Lock()
	authA := manager.auths["auth-a"]
	authA.ModelStates = map[string]*ModelState{
		"gpt-5": {
			Status:         StatusError,
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Minute),
			StatusMessage:  "quota",
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute), BackoffLevel: 1},
		},
	}
	updateAggregatedAvailability(authA, now)
	manager.mu.Unlock()
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		confirmed:           true,
		generation:          2,
		establishedSessions: make(map[string]time.Time),
	}
	staleAttempt := codexRateLimitContinuityAttempt{
		key:         key,
		sessionID:   "execution:established",
		generation:  1,
		established: true,
	}
	ctx := context.WithValue(context.Background(), codexRateLimitContinuityAttemptContextKey{}, staleAttempt)
	manager.MarkResult(ctx, Result{AuthID: "auth-a", Provider: "codex", Model: "gpt-5", Success: true})
	updated, _ := manager.GetByID("auth-a")
	state := updated.ModelStates["gpt-5"]
	if state == nil || !state.Quota.Exceeded || !state.Unavailable {
		t.Fatalf("stale success cleared cooldown: %+v", state)
	}
}

func TestManagerCodexRateLimitContinuityStaleUsageLimitDoesNotIncreaseBackoff(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	now := time.Now()
	deadline := now.Add(time.Minute)
	manager.mu.Lock()
	authA := manager.auths["auth-a"]
	authA.ModelStates = map[string]*ModelState{
		"gpt-5": {
			Status:         StatusError,
			Unavailable:    true,
			NextRetryAfter: deadline,
			StatusMessage:  "quota",
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline, BackoffLevel: 1},
		},
	}
	updateAggregatedAvailability(authA, now)
	manager.mu.Unlock()
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		phase:               codexRateLimitContinuityConfirmedCooldown,
		confirmed:           true,
		generation:          2,
		establishedSessions: make(map[string]time.Time),
	}
	staleAttempt := codexRateLimitContinuityAttempt{
		key:         key,
		sessionID:   "execution:established",
		generation:  1,
		established: true,
	}
	ctx := context.WithValue(context.Background(), codexRateLimitContinuityAttemptContextKey{}, staleAttempt)
	retryAfter := 10 * time.Minute
	manager.MarkResult(ctx, Result{
		AuthID: "auth-a", Provider: "codex", Model: "gpt-5", Success: false,
		Error:               &Error{HTTPStatus: http.StatusTooManyRequests, Message: "stale usage limit"},
		RetryAfter:          &retryAfter,
		ModelFallbackReason: internalconfig.CodexModelFallbackTriggerUsageLimit,
	})
	updated, _ := manager.GetByID("auth-a")
	state := updated.ModelStates["gpt-5"]
	if state == nil || state.Quota.BackoffLevel != 1 || !state.NextRetryAfter.Equal(deadline) || !state.Quota.NextRecoverAt.Equal(deadline) {
		t.Fatalf("stale usage-limit changed cooldown: %+v", state)
	}
}

func TestManagerCodexRateLimitContinuityClearsProcessStateWhenDisabled(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{generation: 1, establishedSessions: make(map[string]time.Time)}
	manager.SetConfig(&internalconfig.Config{})
	manager.codexRateLimitContinuity.mu.Lock()
	stateCount := len(manager.codexRateLimitContinuity.states)
	manager.codexRateLimitContinuity.mu.Unlock()
	if stateCount != 0 {
		t.Fatalf("state count after config disable = %d", stateCount)
	}

	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{generation: 1, establishedSessions: make(map[string]time.Time)}
	oldSelector, _ := manager.selector.(*SessionAffinitySelector)
	manager.SetSelector(&RoundRobinSelector{})
	if oldSelector != nil {
		oldSelector.Stop()
	}
	manager.codexRateLimitContinuity.mu.Lock()
	stateCount = len(manager.codexRateLimitContinuity.states)
	manager.codexRateLimitContinuity.mu.Unlock()
	if stateCount != 0 {
		t.Fatalf("state count after selector disable = %d", stateCount)
	}
}

func TestManagerCodexRateLimitContinuityWithoutStableSessionUsesNormalCooldown(t *testing.T) {
	usageLimit := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	executor := &codexContinuityTestExecutor{failures: map[string]error{"auth-a|gpt-5|": usageLimit}}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hash-only session"}]}`)}
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, opts)
	if err == nil {
		t.Fatal("Execute() error = nil")
	}
	authA, _ := manager.GetByID("auth-a")
	state := authA.ModelStates["gpt-5"]
	if state == nil || !state.Quota.Exceeded {
		t.Fatalf("state = %+v, want normal cooldown", state)
	}
}

func TestManagerCodexRateLimitContinuityIgnoresNonUsageLimit429(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
	}{
		{name: "transient"},
		{name: "capacity", reason: internalconfig.CodexModelFallbackTriggerCapacity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err429 := &codexFallbackTestError{message: tc.name, reason: tc.reason}
			executor := &codexContinuityTestExecutor{failures: map[string]error{"auth-a|gpt-5|fresh": err429}}
			manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
			_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("fresh"))
			if err == nil {
				t.Fatal("Execute() error = nil")
			}
			authA, _ := manager.GetByID("auth-a")
			state := authA.ModelStates["gpt-5"]
			if state == nil || !state.Quota.Exceeded {
				t.Fatalf("state = %+v, want normal cooldown", state)
			}
		})
	}
}

func TestManagerCodexRateLimitContinuityPendingFreshSessionUsesModelFallback(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-source", "gpt-target"}, 2)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true},
		Codex: internalconfig.CodexConfig{
			RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
				Enabled:                      true,
				ObservationWindowSeconds:     10,
				EstablishedSuccessThreshold:  2,
				EstablishedSessionTTLSeconds: 3600,
			},
			ModelFallback: internalconfig.CodexModelFallbackConfig{
				Enabled: true,
				Mappings: []internalconfig.CodexModelFallbackMapping{
					{From: "gpt-source", To: []string{"gpt-target"}},
				},
			},
		},
	})
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-source"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		suspect:             true,
		observeUntil:        time.Now().Add(time.Minute),
		establishedSessions: make(map[string]time.Time),
	}
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, codexContinuityConversationOptions("fresh"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "auth-a|gpt-target|fresh" {
		t.Fatalf("response payload = %q", got)
	}
	if calls := executor.snapshot(); len(calls) != 1 || calls[0] != "auth-a|gpt-target|fresh" {
		t.Fatalf("calls = %#v, want target only", calls)
	}
}

func TestManagerCodexRateLimitContinuityFreshBlockedDoesNotUseGlobalFallback(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-source", "gpt-global"}, 2)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true},
		Codex: internalconfig.CodexConfig{
			RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{
				Enabled:                      true,
				ObservationWindowSeconds:     10,
				EstablishedSuccessThreshold:  2,
				EstablishedSessionTTLSeconds: 3600,
			},
			ModelFallback: internalconfig.CodexModelFallbackConfig{
				Enabled:       true,
				GlobalTargets: []string{"gpt-global"},
			},
		},
	})
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-source"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		suspect:             true,
		observeUntil:        time.Now().Add(time.Minute),
		establishedSessions: make(map[string]time.Time),
		inFlight:            make(map[string]int),
	}

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, codexContinuityConversationOptions("fresh"))
	if err == nil {
		t.Fatal("Execute() error = nil, want continuity observation pending")
	}
	if calls := executor.snapshot(); len(calls) != 0 {
		t.Fatalf("calls = %#v, want zero dispatch while FreshBlocked", calls)
	}
}

func TestManagerCodexRateLimitContinuityConfirmedCooldownUsesModelFallback(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-source", "gpt-target"}, 2)
	manager.SetConfig(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{SessionAffinity: true},
		Codex: internalconfig.CodexConfig{
			RateLimitContinuity: internalconfig.CodexRateLimitContinuityConfig{Enabled: true},
			ModelFallback: internalconfig.CodexModelFallbackConfig{
				Enabled: true,
				Mappings: []internalconfig.CodexModelFallbackMapping{
					{From: "gpt-source", To: []string{"gpt-target"}},
				},
			},
		},
	})
	now := time.Now()
	manager.mu.Lock()
	manager.auths["auth-a"].ModelStates = map[string]*ModelState{
		"gpt-source": {
			Status:         StatusError,
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Minute),
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute), BackoffLevel: 1},
		},
	}
	manager.mu.Unlock()
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-source"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		confirmed:           true,
		generation:          2,
		establishedSessions: make(map[string]time.Time),
	}
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, codexContinuityConversationOptions("fresh"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "auth-a|gpt-target|fresh" {
		t.Fatalf("response payload = %q", got)
	}
}

func TestManagerCodexRateLimitContinuityConcurrentCanaryCallsSelectorCallbackOnce(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	executor := &codexContinuityTestExecutor{
		failures:        make(map[string]error),
		blockExecuteKey: "auth-a|gpt-5|canary-0",
		executeStarted:  started,
		executeRelease:  release,
	}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	manager.codexRateLimitContinuity.now = clock.Now
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		suspect:             true,
		observeUntil:        clock.Now().Add(-time.Second),
		establishedSessions: make(map[string]time.Time),
	}
	var callbackCalls atomic.Int32
	callback := func(string) { callbackCalls.Add(1) }
	firstDone := make(chan error, 1)
	go func() {
		opts := codexContinuityOptions("canary-0")
		opts.Metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = callback
		_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, opts)
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("canary did not start")
	}

	var wg sync.WaitGroup
	var pendingErrors atomic.Int32
	for i := 1; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			opts := codexContinuityOptions("canary-" + time.Unix(int64(index), 0).Format("150405"))
			opts.Metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = callback
			_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, opts)
			if modelFallbackReasonFromError(err) == internalconfig.CodexModelFallbackTriggerUsageLimit {
				pendingErrors.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := pendingErrors.Load(); got != 31 {
		t.Fatalf("pending errors = %d, want 31", got)
	}
	if got := callbackCalls.Load(); got != 1 {
		t.Fatalf("callback calls before release = %d, want 1", got)
	}
	if got := len(executor.snapshot()); got != 1 {
		t.Fatalf("executor calls before release = %d, want 1", got)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("canary Execute() error = %v", err)
	}
}

func TestManagerCodexRateLimitContinuityCancelledBeforeDispatchReleasesCanary(t *testing.T) {
	executor := &codexContinuityTestExecutor{failures: make(map[string]error)}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	clock := &codexContinuityTestClock{now: time.Unix(1_700_000_000, 0)}
	manager.codexRateLimitContinuity.now = clock.Now
	key := codexRateLimitContinuityKey{authID: "auth-a", model: "gpt-5"}
	manager.codexRateLimitContinuity.states[key] = &codexRateLimitContinuityState{
		suspect:             true,
		generation:          1,
		observeUntil:        clock.Now().Add(-time.Second),
		establishedSessions: make(map[string]time.Time),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("cancelled-canary"))
	if err == nil {
		t.Fatal("Execute() error = nil")
	}
	manager.codexRateLimitContinuity.mu.Lock()
	state := manager.codexRateLimitContinuity.states[key]
	manager.codexRateLimitContinuity.mu.Unlock()
	if state == nil || state.canaryToken != 0 {
		t.Fatalf("state after cancellation = %+v", state)
	}
	if got := len(executor.snapshot()); got != 0 {
		t.Fatalf("executor calls = %d, want 0", got)
	}
}

func TestManagerCodexRateLimitContinuityLongStreamIsNotCancelledByFresh429(t *testing.T) {
	usageLimit := &codexFallbackTestError{message: "usage limit", reason: internalconfig.CodexModelFallbackTriggerUsageLimit}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	executor := &codexContinuityTestExecutor{
		failures: map[string]error{"auth-a|gpt-5|fresh": usageLimit},
		streamPayloads: map[string][]cliproxyexecutor.StreamChunk{
			"auth-a|gpt-5|established": {{Payload: []byte("first")}, {Payload: []byte("second")}},
		},
		streamStarted: started,
		streamRelease: release,
	}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established")); err != nil {
		t.Fatalf("establish error = %v", err)
	}
	stream, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established"))
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	first := <-stream.Chunks
	if string(first.Payload) != "first" {
		t.Fatalf("first payload = %q", first.Payload)
	}
	select {
	case <-started:
	default:
	}
	_, _ = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("fresh"))
	close(release)
	second, ok := <-stream.Chunks
	if !ok || second.Err != nil || string(second.Payload) != "second" {
		t.Fatalf("second chunk = %+v ok=%t", second, ok)
	}
}

func TestManagerCodexRateLimitContinuityEstablishedStreamTerminalUsageLimitPreservesRetryAfter(t *testing.T) {
	retryAfter := 90 * time.Second
	usageLimit := &codexContinuityUsageLimitError{retryAfter: retryAfter}
	executor := &codexContinuityTestExecutor{
		failures: map[string]error{"auth-a|gpt-5|fresh": usageLimit},
		streamPayloads: map[string][]cliproxyexecutor.StreamChunk{
			"auth-a|gpt-5|established": {
				{Payload: []byte("partial")},
				{Err: usageLimit},
			},
		},
	}
	manager := newCodexContinuityManager(t, executor, []string{"auth-a"}, []string{"gpt-5"}, 2)
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established")); err != nil {
		t.Fatalf("establish Execute() error = %v", err)
	}
	if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("fresh")); err == nil {
		t.Fatal("fresh usage-limit error = nil")
	}

	startedAt := time.Now()
	stream, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5"}, codexContinuityOptions("established"))
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	first, ok := <-stream.Chunks
	if !ok || first.Err != nil || string(first.Payload) != "partial" {
		t.Fatalf("first chunk = %+v ok=%t", first, ok)
	}
	terminal, ok := <-stream.Chunks
	if !ok || terminal.Err == nil {
		t.Fatalf("terminal chunk = %+v ok=%t", terminal, ok)
	}
	if got := retryAfterFromError(terminal.Err); got == nil || *got != retryAfter {
		t.Fatalf("terminal RetryAfter = %v, want %v", got, retryAfter)
	}
	if _, ok := <-stream.Chunks; ok {
		t.Fatal("stream remained open after terminal error")
	}

	auth, _ := manager.GetByID("auth-a")
	state := auth.ModelStates["gpt-5"]
	if state == nil || !state.Quota.Exceeded || state.NextRetryAfter.IsZero() || state.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("formal cooldown state = %+v", state)
	}
	if !state.NextRetryAfter.Equal(state.Quota.NextRecoverAt) {
		t.Fatalf("retry time mismatch: model=%v quota=%v", state.NextRetryAfter, state.Quota.NextRecoverAt)
	}
	remaining := state.NextRetryAfter.Sub(startedAt)
	if remaining < retryAfter-2*time.Second || remaining > retryAfter+2*time.Second {
		t.Fatalf("formal cooldown remaining = %v, want about %v", remaining, retryAfter)
	}
}
