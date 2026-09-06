package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type codexRateLimitContinuityKey struct {
	authID string
	model  string
}

type codexRateLimitContinuityState struct {
	// phase is the authoritative admission state. suspect/confirmed are kept in
	// sync for compatibility with the initial implementation and its tests.
	phase                codexRateLimitContinuityPhase
	suspect              bool
	confirmed            bool
	generation           uint64
	observeUntil         time.Time
	establishedSuccesses int
	establishedSessions  map[string]time.Time
	inFlight             map[string]int
	nextLeaseExpiry      time.Time
	leasePruneScans      uint64
	canaryToken          uint64
}

type codexRateLimitContinuityPhase uint8

const (
	codexRateLimitContinuityHealthy codexRateLimitContinuityPhase = iota
	codexRateLimitContinuityFreshBlocked
	codexRateLimitContinuityConfirmedCooldown
)

type codexRateLimitContinuityStore struct {
	mu             sync.Mutex
	states         map[codexRateLimitContinuityKey]*codexRateLimitContinuityState
	nextToken      uint64
	nextAttempt    uint64
	activeAttempts map[uint64]codexRateLimitContinuityAttempt
	now            func() time.Time
}

type codexRateLimitContinuityAttempt struct {
	key          codexRateLimitContinuityKey
	sessionID    string
	generation   uint64
	established  bool
	incumbent    bool
	canaryToken  uint64
	attemptToken uint64
	leaseTTL     time.Duration
}

type codexRateLimitContinuityAttemptContextKey struct{}
type codexRateLimitContinuityLifecycleContextKey struct{}

type codexRateLimitContinuityDisposition uint8

const (
	codexRateLimitContinuityNormal codexRateLimitContinuityDisposition = iota
	codexRateLimitContinuityObserveOnly
	codexRateLimitContinuityRecordOnly
)

type codexRateLimitObservationPendingError struct {
	confirmed bool
}

func (e codexRateLimitObservationPendingError) Error() string {
	if e.confirmed {
		return "codex auth/model has a confirmed usage-limit cooldown"
	}
	return "codex auth/model is under fresh-session rate-limit observation"
}

func (codexRateLimitObservationPendingError) StatusCode() int { return http.StatusTooManyRequests }

func (codexRateLimitObservationPendingError) ModelFallbackReason() string {
	return internalconfig.CodexModelFallbackTriggerUsageLimit
}

// ModelFallbackZeroDispatch lets fallback orchestration distinguish local
// continuity admission from an upstream attempt. It must not consume usage,
// cooldown, selected-auth callback, or ordered-target budget.
func (codexRateLimitObservationPendingError) ModelFallbackZeroDispatch() bool { return true }

func newCodexRateLimitContinuityStore() *codexRateLimitContinuityStore {
	return &codexRateLimitContinuityStore{
		states:         make(map[codexRateLimitContinuityKey]*codexRateLimitContinuityState),
		activeAttempts: make(map[uint64]codexRateLimitContinuityAttempt),
		now:            time.Now,
	}
}

func (m *Manager) advanceCodexRateLimitContinuityLifecycleLocked() {
	if m == nil {
		return
	}
	m.codexRateLimitContinuityLifecycle++
	if m.codexRateLimitContinuityLifecycle == 0 {
		m.codexRateLimitContinuityLifecycle++
	}
}

func (m *Manager) withCodexRateLimitContinuityLifecycle(ctx context.Context) context.Context {
	if m == nil {
		return ctx
	}
	m.mu.RLock()
	generation := m.codexRateLimitContinuityLifecycle
	m.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexRateLimitContinuityLifecycleContextKey{}, generation)
}

func codexRateLimitContinuityLifecycleFromContext(ctx context.Context) (uint64, bool) {
	if ctx == nil {
		return 0, false
	}
	switch value := ctx.Value(codexRateLimitContinuityLifecycleContextKey{}).(type) {
	case uint64:
		return value, true
	case uint:
		return uint64(value), true
	case int:
		if value >= 0 {
			return uint64(value), true
		}
	}
	return 0, false
}

func (s *codexRateLimitContinuityStore) currentTime() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (s *codexRateLimitContinuityStore) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.states = make(map[codexRateLimitContinuityKey]*codexRateLimitContinuityState)
	s.activeAttempts = make(map[uint64]codexRateLimitContinuityAttempt)
	s.mu.Unlock()
}

func (s *codexRateLimitContinuityStore) removeAuth(authID string) {
	if s == nil || strings.TrimSpace(authID) == "" {
		return
	}
	s.mu.Lock()
	for key := range s.states {
		if key.authID == authID {
			delete(s.states, key)
		}
	}
	for token, attempt := range s.activeAttempts {
		if attempt.key.authID == authID {
			delete(s.activeAttempts, token)
		}
	}
	s.mu.Unlock()
}

func codexRateLimitObservationWindow(cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) time.Duration {
	return time.Duration(cfg.ObservationWindowSeconds) * time.Second
}

func codexRateLimitEstablishedSessionTTL(cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) time.Duration {
	return time.Duration(cfg.EstablishedSessionTTLSeconds) * time.Second
}

func codexRateLimitEstablishedSessionTTLWithAffinity(cfg internalconfig.EffectiveCodexRateLimitContinuityConfig, affinityTTL string) time.Duration {
	ttl := codexRateLimitEstablishedSessionTTL(cfg)
	parsed, err := time.ParseDuration(strings.TrimSpace(affinityTTL))
	if err != nil || parsed <= 0 {
		parsed = time.Hour
	}
	if parsed < ttl {
		return parsed
	}
	return ttl
}

func setCodexRateLimitContinuityPhase(state *codexRateLimitContinuityState, phase codexRateLimitContinuityPhase) {
	if state == nil {
		return
	}
	state.phase = phase
	state.suspect = phase == codexRateLimitContinuityFreshBlocked
	state.confirmed = phase == codexRateLimitContinuityConfirmedCooldown
}

func ensureCodexRateLimitContinuityState(state *codexRateLimitContinuityState) {
	if state == nil {
		return
	}
	if state.establishedSessions == nil {
		state.establishedSessions = make(map[string]time.Time)
	}
	if state.inFlight == nil {
		state.inFlight = make(map[string]int)
	}
	// Old in-memory states were represented by these booleans.
	if state.confirmed {
		setCodexRateLimitContinuityPhase(state, codexRateLimitContinuityConfirmedCooldown)
	} else if state.suspect {
		setCodexRateLimitContinuityPhase(state, codexRateLimitContinuityFreshBlocked)
	}
}

func pruneCodexRateLimitContinuityState(state *codexRateLimitContinuityState, now time.Time) {
	if state == nil || (state.nextLeaseExpiry.After(now) && !state.nextLeaseExpiry.IsZero()) {
		return
	}
	state.leasePruneScans++
	state.nextLeaseExpiry = time.Time{}
	for sessionID, expiresAt := range state.establishedSessions {
		if !expiresAt.After(now) {
			delete(state.establishedSessions, sessionID)
			continue
		}
		if state.nextLeaseExpiry.IsZero() || expiresAt.Before(state.nextLeaseExpiry) {
			state.nextLeaseExpiry = expiresAt
		}
	}
}

func codexRateLimitCanaryEligible(state *codexRateLimitContinuityState, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig, now time.Time) bool {
	if state == nil || state.phase != codexRateLimitContinuityFreshBlocked || state.canaryToken != 0 {
		return false
	}
	return !state.observeUntil.After(now) || state.establishedSuccesses >= cfg.EstablishedSuccessThreshold
}

func advanceCodexRateLimitContinuityGeneration(state *codexRateLimitContinuityState) {
	if state == nil {
		return
	}
	state.generation++
	if state.generation == 0 {
		state.generation = 1
	}
}

func (s *codexRateLimitContinuityStore) candidateDisposition(key codexRateLimitContinuityKey, sessionID string, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) (allowed, establishedSuspect, confirmed bool) {
	if s == nil {
		return true, false, false
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[key]
	if state == nil {
		return true, false, false
	}
	ensureCodexRateLimitContinuityState(state)
	pruneCodexRateLimitContinuityState(state, now)
	if state.phase == codexRateLimitContinuityConfirmedCooldown {
		return true, false, true
	}
	if state.phase == codexRateLimitContinuityHealthy {
		return true, false, false
	}
	if expiresAt := state.establishedSessions[sessionID]; expiresAt.After(now) || state.inFlight[sessionID] > 0 {
		return true, true, false
	}
	return codexRateLimitCanaryEligible(state, cfg, now), false, false
}

func (s *codexRateLimitContinuityStore) candidateAllowed(key codexRateLimitContinuityKey, sessionID string, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) bool {
	allowed, _, _ := s.candidateDisposition(key, sessionID, cfg)
	return allowed
}

func (s *codexRateLimitContinuityStore) begin(key codexRateLimitContinuityKey, sessionID string, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) (codexRateLimitContinuityAttempt, bool) {
	return s.beginWithLeaseTTL(key, sessionID, cfg, codexRateLimitEstablishedSessionTTL(cfg))
}

func (s *codexRateLimitContinuityStore) beginWithLeaseTTL(key codexRateLimitContinuityKey, sessionID string, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig, leaseTTL time.Duration) (codexRateLimitContinuityAttempt, bool) {
	attempt := codexRateLimitContinuityAttempt{key: key, sessionID: sessionID, leaseTTL: leaseTTL}
	if s == nil {
		return attempt, true
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[key]
	if state == nil {
		state = &codexRateLimitContinuityState{
			generation:          1,
			establishedSessions: make(map[string]time.Time),
			inFlight:            make(map[string]int),
		}
		s.states[key] = state
	}
	ensureCodexRateLimitContinuityState(state)
	if state.generation == 0 {
		state.generation = 1
	}
	pruneCodexRateLimitContinuityState(state, now)
	if state.phase == codexRateLimitContinuityConfirmedCooldown {
		return attempt, false
	}
	attempt.generation = state.generation
	s.nextAttempt++
	if s.nextAttempt == 0 {
		s.nextAttempt++
	}
	attempt.attemptToken = s.nextAttempt
	s.activeAttempts[attempt.attemptToken] = attempt
	if expiresAt := state.establishedSessions[sessionID]; expiresAt.After(now) {
		attempt.established = true
		state.inFlight[sessionID]++
		return attempt, true
	}
	if state.inFlight[sessionID] > 0 {
		// A second request on a session that was already dispatched before the
		// fresh-session observation is an incumbent continuation, not a canary.
		attempt.incumbent = true
		state.inFlight[sessionID]++
		return attempt, true
	}
	if state.phase == codexRateLimitContinuityHealthy {
		attempt.incumbent = true
		state.inFlight[sessionID]++
		return attempt, true
	}
	if !codexRateLimitCanaryEligible(state, cfg, now) {
		delete(s.activeAttempts, attempt.attemptToken)
		attempt.attemptToken = 0
		return attempt, false
	}
	s.nextToken++
	if s.nextToken == 0 {
		s.nextToken++
	}
	state.canaryToken = s.nextToken
	attempt.canaryToken = state.canaryToken
	state.inFlight[sessionID]++
	return attempt, true
}

func (s *codexRateLimitContinuityStore) attemptCurrentLocked(attempt codexRateLimitContinuityAttempt) bool {
	// token zero is reserved for direct synthetic state-machine tests. Every
	// real dispatch is live only while its bounded active token remains present.
	if attempt.attemptToken == 0 {
		return true
	}
	active, ok := s.activeAttempts[attempt.attemptToken]
	return ok && active.key == attempt.key && active.sessionID == attempt.sessionID
}

func (s *codexRateLimitContinuityStore) releaseAttemptLocked(state *codexRateLimitContinuityState, attempt codexRateLimitContinuityAttempt) bool {
	if attempt.attemptToken == 0 {
		return false
	}
	if activeAttempt, active := s.activeAttempts[attempt.attemptToken]; !active || activeAttempt.key != attempt.key || activeAttempt.sessionID != attempt.sessionID {
		return false
	}
	delete(s.activeAttempts, attempt.attemptToken)
	if state.inFlight[attempt.sessionID] > 1 {
		state.inFlight[attempt.sessionID]--
	} else {
		delete(state.inFlight, attempt.sessionID)
	}
	return true
}

func (s *codexRateLimitContinuityStore) observe(attempt codexRateLimitContinuityAttempt, success bool, fallbackReason string, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) codexRateLimitContinuityDisposition {
	if s == nil || attempt.key.authID == "" || attempt.key.model == "" || attempt.sessionID == "" {
		return codexRateLimitContinuityNormal
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[attempt.key]
	usageLimit := strings.EqualFold(strings.TrimSpace(fallbackReason), internalconfig.CodexModelFallbackTriggerUsageLimit)
	if !s.attemptCurrentLocked(attempt) {
		if success || usageLimit {
			return codexRateLimitContinuityRecordOnly
		}
		return codexRateLimitContinuityNormal
	}
	if state == nil && !success && !usageLimit {
		return codexRateLimitContinuityNormal
	}
	if state == nil {
		generation := attempt.generation
		if generation == 0 {
			generation = 1
		}
		state = &codexRateLimitContinuityState{
			generation:          generation,
			establishedSessions: make(map[string]time.Time),
		}
		s.states[attempt.key] = state
	}
	if state.generation == 0 {
		state.generation = 1
	}
	ensureCodexRateLimitContinuityState(state)
	pruneCodexRateLimitContinuityState(state, now)
	s.releaseAttemptLocked(state, attempt)
	if attempt.generation != 0 && attempt.generation != state.generation {
		if success || usageLimit {
			return codexRateLimitContinuityRecordOnly
		}
		return codexRateLimitContinuityNormal
	}
	matchingCanary := attempt.canaryToken != 0 && attempt.canaryToken == state.canaryToken

	if success {
		leaseTTL := attempt.leaseTTL
		if leaseTTL <= 0 {
			leaseTTL = codexRateLimitEstablishedSessionTTL(cfg)
		}
		expiresAt := now.Add(leaseTTL)
		state.establishedSessions[attempt.sessionID] = expiresAt
		if state.nextLeaseExpiry.IsZero() || expiresAt.Before(state.nextLeaseExpiry) {
			state.nextLeaseExpiry = expiresAt
		}
		switch {
		case matchingCanary:
			advanceCodexRateLimitContinuityGeneration(state)
			setCodexRateLimitContinuityPhase(state, codexRateLimitContinuityHealthy)
			state.observeUntil = time.Time{}
			state.establishedSuccesses = 0
			state.canaryToken = 0
		case state.phase == codexRateLimitContinuityFreshBlocked && (attempt.established || attempt.incumbent):
			if state.establishedSuccesses < cfg.EstablishedSuccessThreshold {
				state.establishedSuccesses++
			}
		}
		return codexRateLimitContinuityNormal
	}

	if usageLimit {
		// The request that first observes a healthy fresh-session 429 is not an
		// incumbent. Only a request that started before another request moved the
		// state into FreshBlocked may confirm the shared limit.
		incumbentUsageLimit := attempt.established || (attempt.incumbent && state.phase == codexRateLimitContinuityFreshBlocked)
		if incumbentUsageLimit || matchingCanary {
			if matchingCanary && (len(state.establishedSessions) > 0 || len(state.inFlight) > 0) {
				state.canaryToken = 0
				state.observeUntil = now.Add(codexRateLimitObservationWindow(cfg))
				state.establishedSuccesses = 0
				return codexRateLimitContinuityObserveOnly
			}
			advanceCodexRateLimitContinuityGeneration(state)
			setCodexRateLimitContinuityPhase(state, codexRateLimitContinuityConfirmedCooldown)
			state.observeUntil = time.Time{}
			state.establishedSuccesses = 0
			state.canaryToken = 0
			// A confirmed shared limit invalidates all old continuity evidence.
			state.establishedSessions = make(map[string]time.Time)
			state.inFlight = make(map[string]int)
			state.nextLeaseExpiry = time.Time{}
			for token, activeAttempt := range s.activeAttempts {
				if activeAttempt.key == attempt.key {
					delete(s.activeAttempts, token)
				}
			}
			return codexRateLimitContinuityNormal
		}
		if state.phase != codexRateLimitContinuityFreshBlocked {
			setCodexRateLimitContinuityPhase(state, codexRateLimitContinuityFreshBlocked)
			state.observeUntil = now.Add(codexRateLimitObservationWindow(cfg))
			state.establishedSuccesses = 0
			state.canaryToken = 0
		}
		return codexRateLimitContinuityObserveOnly
	}

	if matchingCanary {
		state.canaryToken = 0
		state.observeUntil = now.Add(codexRateLimitObservationWindow(cfg))
		state.establishedSuccesses = 0
	}
	return codexRateLimitContinuityNormal
}

func (s *codexRateLimitContinuityStore) abandon(attempt codexRateLimitContinuityAttempt, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) {
	if s == nil {
		return
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[attempt.key]
	if state == nil {
		return
	}
	if !s.attemptCurrentLocked(attempt) {
		return
	}
	ensureCodexRateLimitContinuityState(state)
	if !s.releaseAttemptLocked(state, attempt) {
		return
	}
	if attempt.canaryToken != 0 && state.canaryToken == attempt.canaryToken && (attempt.generation == 0 || attempt.generation == state.generation) {
		state.canaryToken = 0
		state.observeUntil = now.Add(codexRateLimitObservationWindow(cfg))
		state.establishedSuccesses = 0
	}
}

func codexRateLimitStableSessionID(opts cliproxyexecutor.Options) string {
	// Do not call ExtractSessionID and reject its answer wholesale: it can select
	// a request-id or generic user-id before a later conversation_id. Continuity
	// only accepts durable conversation/session identities.
	if raw := contextStringValue(opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]); raw != "" {
		return "execution:" + raw
	}
	if raw := contextStringValue(opts.Metadata[codexCanonicalSessionMetadataKey]); raw != "" {
		return "codex:" + raw
	}
	// Claude is the next selector priority. Parse only its explicit stable form;
	// generic user/request IDs can never hide the permitted sources below.
	if primary, _ := extractSessionIDs(nil, opts.OriginalRequest, opts.Metadata); strings.HasPrefix(primary, "claude:") && len(primary) > len("claude:") {
		return primary
	}
	// Check each permitted header directly. In particular this must happen even
	// when X-Client-Request-Id (which is intentionally not stable here) is also
	// present.
	if opts.Headers != nil {
		if sessionID := strings.TrimSpace(opts.Headers.Get("X-Session-ID")); sessionID != "" {
			return "header:" + sessionID
		}
		if sessionID := strings.TrimSpace(opts.Headers.Get("Session-Id")); sessionID != "" {
			return "codex:" + sessionID
		}
		if sessionID := strings.TrimSpace(opts.Headers.Get("Session_id")); sessionID != "" {
			return "codex:" + sessionID
		}
		if threadID := strings.TrimSpace(opts.Headers.Get("X-Amp-Thread-Id")); threadID != "" {
			return "amp:" + threadID
		}
	}
	if conversationID := strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "conversation_id").String()); conversationID != "" {
		return "conv:" + conversationID
	}
	return ""
}

func (m *Manager) codexRateLimitContinuityPolicy() (internalconfig.EffectiveCodexRateLimitContinuityConfig, bool) {
	if m == nil {
		return internalconfig.EffectiveCodexRateLimitContinuityConfig{}, false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || cfg.Home.Enabled || !cfg.Routing.SessionAffinity {
		return internalconfig.EffectiveCodexRateLimitContinuityConfig{}, false
	}
	effective := cfg.Codex.RateLimitContinuity.Effective()
	if !effective.Enabled {
		return effective, false
	}
	m.mu.RLock()
	_, selectorOK := m.selector.(*SessionAffinitySelector)
	m.mu.RUnlock()
	return effective, selectorOK
}

// codexRateLimitContinuityPolicyLocked is the MarkResult variant. The caller
// owns manager.mu, preserving the lock order manager -> continuity store.
func (m *Manager) codexRateLimitContinuityPolicyLocked() (internalconfig.EffectiveCodexRateLimitContinuityConfig, bool) {
	if m == nil {
		return internalconfig.EffectiveCodexRateLimitContinuityConfig{}, false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || cfg.Home.Enabled || !cfg.Routing.SessionAffinity {
		return internalconfig.EffectiveCodexRateLimitContinuityConfig{}, false
	}
	effective := cfg.Codex.RateLimitContinuity.Effective()
	if !effective.Enabled {
		return effective, false
	}
	_, selectorOK := m.selector.(*SessionAffinitySelector)
	return effective, selectorOK
}

func codexRateLimitContinuitySettingsChanged(oldCfg, newCfg *internalconfig.Config) bool {
	if oldCfg == nil {
		oldCfg = &internalconfig.Config{}
	}
	if newCfg == nil {
		newCfg = &internalconfig.Config{}
	}
	old := oldCfg.Codex.RateLimitContinuity.Effective()
	new := newCfg.Codex.RateLimitContinuity.Effective()
	return old.Enabled != new.Enabled ||
		old.ObservationWindowSeconds != new.ObservationWindowSeconds ||
		old.EstablishedSuccessThreshold != new.EstablishedSuccessThreshold ||
		old.EstablishedSessionTTLSeconds != new.EstablishedSessionTTLSeconds ||
		oldCfg.Home.Enabled != newCfg.Home.Enabled ||
		oldCfg.Routing.SessionAffinity != newCfg.Routing.SessionAffinity ||
		strings.TrimSpace(oldCfg.Routing.SessionAffinityTTL) != strings.TrimSpace(newCfg.Routing.SessionAffinityTTL)
}

func (s *codexRateLimitContinuityStore) removeSession(sessionID string) {
	s.removeSessionPreservingKey(sessionID, nil)
}

func (s *codexRateLimitContinuityStore) removeSessionPreservingKey(sessionID string, preserve *codexRateLimitContinuityKey) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, attempt := range s.activeAttempts {
		if attempt.sessionID != sessionID || (preserve != nil && attempt.key == *preserve) {
			continue
		}
		if state := s.states[attempt.key]; state != nil && attempt.canaryToken != 0 && state.canaryToken == attempt.canaryToken {
			state.canaryToken = 0
			state.establishedSuccesses = 0
		}
		delete(s.activeAttempts, token)
	}
	for key, state := range s.states {
		if state == nil {
			continue
		}
		ensureCodexRateLimitContinuityState(state)
		delete(state.establishedSessions, sessionID)
		if preserve == nil || key != *preserve {
			delete(state.inFlight, sessionID)
		}
		state.nextLeaseExpiry = time.Time{}
	}
}

// reopenConfirmed converts an expired persisted cooldown into FreshBlocked.
// It is only called after the caller has verified the auth/model is available,
// so a single canary, rather than a fresh-session stampede, performs recovery.
func (s *codexRateLimitContinuityStore) reopenConfirmed(key codexRateLimitContinuityKey, expectedGeneration uint64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[key]
	if state == nil {
		return false
	}
	ensureCodexRateLimitContinuityState(state)
	if state.phase != codexRateLimitContinuityConfirmedCooldown || (expectedGeneration != 0 && state.generation != expectedGeneration) {
		return false
	}
	advanceCodexRateLimitContinuityGeneration(state)
	setCodexRateLimitContinuityPhase(state, codexRateLimitContinuityFreshBlocked)
	state.observeUntil = time.Time{}
	state.canaryToken = 0
	state.establishedSuccesses = 0
	state.establishedSessions = make(map[string]time.Time)
	state.inFlight = make(map[string]int)
	state.nextLeaseExpiry = time.Time{}
	return true
}

func (s *codexRateLimitContinuityStore) dispatchAllowed(attempt codexRateLimitContinuityAttempt) (allowed, confirmed bool) {
	if s == nil || attempt.key.authID == "" {
		return true, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[attempt.key]
	if state == nil {
		return false, false
	}
	if !s.attemptCurrentLocked(attempt) {
		return false, false
	}
	ensureCodexRateLimitContinuityState(state)
	confirmed = state.phase == codexRateLimitContinuityConfirmedCooldown
	return !confirmed && (attempt.generation == 0 || attempt.generation == state.generation), confirmed
}

func (m *Manager) codexRateLimitContinuityRequest(opts cliproxyexecutor.Options) (internalconfig.EffectiveCodexRateLimitContinuityConfig, string, bool) {
	effective, ok := m.codexRateLimitContinuityPolicy()
	if !ok {
		return effective, "", false
	}
	sessionID := codexRateLimitStableSessionID(opts)
	return effective, sessionID, sessionID != ""
}

func (m *Manager) filterCodexRateLimitContinuityCandidates(candidates []*Auth, routeModel string, opts cliproxyexecutor.Options) ([]*Auth, error) {
	effective, sessionID, ok := m.codexRateLimitContinuityRequest(opts)
	if !ok || m.codexRateLimitContinuity == nil {
		return candidates, nil
	}
	filtered := make([]*Auth, 0, len(candidates))
	establishedSuspect := make([]*Auth, 0, 1)
	removedCodex := false
	removedConfirmed := false
	now := time.Now()
	for _, auth := range candidates {
		if auth == nil || executorKeyFromAuth(auth) != "codex" {
			filtered = append(filtered, auth)
			continue
		}
		modelKey := m.selectionModelKeyForAuth(auth, routeModel)
		key := codexRateLimitContinuityKey{authID: auth.ID, model: modelKey}
		allowed, established, confirmed := m.codexRateLimitContinuity.candidateDisposition(key, sessionID, effective)
		if confirmed {
			// Reopen only while holding manager -> continuity locks and only from
			// canonical auth state. A stale candidate clone must not revive a
			// confirmed cooldown after Load/remove/another result changes auth.
			m.mu.Lock()
			canonical := m.auths[auth.ID]
			checkModel := ""
			blocked := true
			if canonical != nil {
				checkModel = m.selectionModelForAuth(canonical, routeModel)
				blocked, _, _ = isAuthBlockedForModel(canonical, checkModel, now)
			}
			if !blocked {
				stateGeneration := uint64(0)
				m.codexRateLimitContinuity.mu.Lock()
				if state := m.codexRateLimitContinuity.states[key]; state != nil {
					stateGeneration = state.generation
				}
				m.codexRateLimitContinuity.mu.Unlock()
				m.codexRateLimitContinuity.reopenConfirmed(key, stateGeneration)
			}
			m.mu.Unlock()
			if blocked {
				removedCodex = true
				removedConfirmed = true
				continue
			}
			allowed, established, _ = m.codexRateLimitContinuity.candidateDisposition(key, sessionID, effective)
		}
		if modelKey == "" || allowed {
			filtered = append(filtered, auth)
			if established {
				checkModel := m.selectionModelForAuth(auth, routeModel)
				if blocked, _, _ := isAuthBlockedForModel(auth, checkModel, now); !blocked {
					establishedSuspect = append(establishedSuspect, auth)
				}
			}
			continue
		}
		removedCodex = true
	}
	if len(filtered) == 0 && removedCodex {
		return nil, codexRateLimitObservationPendingError{confirmed: removedConfirmed}
	}
	if len(establishedSuspect) > 0 {
		return establishedSuspect, nil
	}
	return filtered, nil
}

func (m *Manager) beginCodexRateLimitContinuityAttempt(ctx context.Context, auth *Auth, provider, routeModel string, opts cliproxyexecutor.Options) (context.Context, bool) {
	if m != nil && m.flowControl != nil && m.flowControl.Enabled() {
		if ctx == nil {
			ctx = context.Background()
		}
		return context.WithValue(ctx, flowContinuityContextKey{}, &flowDeferredContinuity{provider: provider, routeModel: routeModel, opts: opts}), true
	}
	return m.beginCodexRateLimitContinuityNow(ctx, auth, provider, routeModel, opts)
}

// beginCodexRateLimitContinuityNow is the pre-existing continuity admission.
// Flow-control delays only its timing; the state machine and decisions stay here.
func (m *Manager) beginCodexRateLimitContinuityNow(ctx context.Context, auth *Auth, provider, routeModel string, opts cliproxyexecutor.Options) (context.Context, bool) {
	if auth == nil || strings.ToLower(strings.TrimSpace(provider)) != "codex" || m.codexRateLimitContinuity == nil {
		return ctx, true
	}
	sessionID := codexRateLimitStableSessionID(opts)
	if sessionID == "" {
		return ctx, true
	}
	// Hold manager -> continuity locks through active-policy validation,
	// canonical auth recheck, selector TTL, and admission. Home/default-off and
	// requests without stable identity must bypass continuity before canonical
	// Core auth checks because their auth may not live in m.auths at all.
	m.mu.RLock()
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	selector, selectorOK := m.selector.(*SessionAffinitySelector)
	if cfg == nil || cfg.Home.Enabled || !cfg.Routing.SessionAffinity || !selectorOK {
		m.mu.RUnlock()
		return ctx, true
	}
	effective := cfg.Codex.RateLimitContinuity.Effective()
	if !effective.Enabled {
		m.mu.RUnlock()
		return ctx, true
	}
	requestLifecycle, hasLifecycle := codexRateLimitContinuityLifecycleFromContext(ctx)
	canonical := m.auths[auth.ID]
	if (hasLifecycle && requestLifecycle != m.codexRateLimitContinuityLifecycle) || canonical == nil {
		m.mu.RUnlock()
		return ctx, false
	}
	modelKey := m.selectionModelKeyForAuth(canonical, routeModel)
	if modelKey == "" {
		m.mu.RUnlock()
		return ctx, true
	}
	affinityTTL := ""
	affinityTTL = cfg.Routing.SessionAffinityTTL
	leaseTTL := codexRateLimitEstablishedSessionTTLWithAffinity(effective, affinityTTL)
	if selector != nil && selector.cache != nil && selector.cache.ttl > 0 && selector.cache.ttl < leaseTTL {
		leaseTTL = selector.cache.ttl
	}
	attempt, allowed := m.codexRateLimitContinuity.beginWithLeaseTTL(codexRateLimitContinuityKey{authID: auth.ID, model: modelKey}, sessionID, effective, leaseTTL)
	m.mu.RUnlock()
	if !allowed {
		return ctx, false
	}
	if attempt.canaryToken != 0 {
		logEntryWithRequestID(ctx).
			WithField("auth_id", auth.ID).
			WithField("model", modelKey).
			WithField("session", sessionLogIdentity(sessionID)).
			Info("codex rate-limit continuity: reserved fresh-session canary")
	}
	return context.WithValue(ctx, codexRateLimitContinuityAttemptContextKey{}, attempt), true
}

func (m *Manager) codexRateLimitContinuityDispatchDisposition(ctx context.Context) (allowed, confirmed bool) {
	if m == nil || m.codexRateLimitContinuity == nil {
		return true, false
	}
	attempt, ok := codexRateLimitContinuityAttemptFromContext(ctx)
	if !ok {
		return true, false
	}
	return m.codexRateLimitContinuity.dispatchAllowed(attempt)
}

func codexRateLimitContinuityAttemptFromContext(ctx context.Context) (codexRateLimitContinuityAttempt, bool) {
	if ctx == nil {
		return codexRateLimitContinuityAttempt{}, false
	}
	attempt, ok := ctx.Value(codexRateLimitContinuityAttemptContextKey{}).(codexRateLimitContinuityAttempt)
	if !ok {
		if deferred, found := ctx.Value(flowContinuityContextKey{}).(*flowDeferredContinuity); found {
			deferred.mu.Lock()
			attempt = deferred.attempt
			ok = deferred.activated
			deferred.mu.Unlock()
		}
	}
	return attempt, ok && attempt.key.authID != "" && attempt.key.model != "" && attempt.sessionID != ""
}

func (m *Manager) observeCodexRateLimitContinuityResult(ctx context.Context, result Result) codexRateLimitContinuityDisposition {
	if m == nil || m.codexRateLimitContinuity == nil || strings.ToLower(strings.TrimSpace(result.Provider)) != "codex" {
		return codexRateLimitContinuityNormal
	}
	effective, ok := m.codexRateLimitContinuityPolicyLocked()
	if !ok {
		return codexRateLimitContinuityNormal
	}
	attempt, ok := codexRateLimitContinuityAttemptFromContext(ctx)
	if !ok || attempt.key.authID != result.AuthID {
		return codexRateLimitContinuityNormal
	}
	disposition := m.codexRateLimitContinuity.observe(attempt, result.Success, result.ModelFallbackReason, effective)
	entry := logEntryWithRequestID(ctx).
		WithField("auth_id", result.AuthID).
		WithField("model", attempt.key.model).
		WithField("session", sessionLogIdentity(attempt.sessionID))
	switch {
	case disposition == codexRateLimitContinuityObserveOnly:
		entry.Info("codex rate-limit continuity: observing fresh-session usage limit")
	case disposition == codexRateLimitContinuityRecordOnly:
		entry.Debug("codex rate-limit continuity: ignored stale result after state generation advanced")
	case result.Success && attempt.canaryToken != 0:
		entry.Info("codex rate-limit continuity: fresh-session canary recovered auth/model")
	case !result.Success && strings.EqualFold(strings.TrimSpace(result.ModelFallbackReason), internalconfig.CodexModelFallbackTriggerUsageLimit) && (attempt.established || attempt.canaryToken != 0):
		entry.Info("codex rate-limit continuity: confirmed auth/model usage limit")
	case !result.Success && attempt.canaryToken != 0:
		entry.Info("codex rate-limit continuity: canary result was inconclusive")
	}
	return disposition
}

func (m *Manager) abandonCodexRateLimitContinuityAttempt(ctx context.Context) {
	if m == nil || m.codexRateLimitContinuity == nil {
		return
	}
	effective, ok := m.codexRateLimitContinuityPolicy()
	if !ok {
		return
	}
	attempt, ok := codexRateLimitContinuityAttemptFromContext(ctx)
	if !ok {
		return
	}
	m.codexRateLimitContinuity.abandon(attempt, effective)
}
