package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type codexContinuityTestClock struct {
	mu  sync.Mutex
	now time.Time
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

func TestCodexRateLimitContinuityFirstFreshUsageLimitBecomesSuspect(t *testing.T) {
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
	if state == nil || !state.suspect {
		t.Fatalf("state = %+v, want suspect", state)
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

func TestCodexRateLimitContinuityEstablishedOrCanaryUsageLimitConfirmsCooldown(t *testing.T) {
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
			if got := store.observe(probe, false, internalconfig.CodexModelFallbackTriggerUsageLimit, cfg); got != codexRateLimitContinuityNormal {
				t.Fatalf("observe() = %v, want normal cooldown", got)
			}
			store.mu.Lock()
			state := store.states[key]
			store.mu.Unlock()
			if state == nil || !state.confirmed || state.generation <= probe.generation {
				t.Fatalf("confirmed state = %+v, probe generation=%d", state, probe.generation)
			}
		})
	}
}

func TestCodexRateLimitContinuityConfirmedGenerationRejectsStaleResult(t *testing.T) {
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
	if got := store.observe(staleEstablished, true, "", cfg); got != codexRateLimitContinuityRecordOnly {
		t.Fatalf("stale observe() = %v, want record-only", got)
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
	return contextStringValue(opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey])
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
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, codexContinuityOptions("fresh"))
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
	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-source"}, codexContinuityOptions("fresh"))
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
