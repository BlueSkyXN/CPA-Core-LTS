package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type codexRateLimitContinuityKey struct {
	authID string
	model  string
}

type codexRateLimitContinuityState struct {
	suspect              bool
	confirmed            bool
	generation           uint64
	observeUntil         time.Time
	establishedSuccesses int
	establishedSessions  map[string]time.Time
	canaryToken          uint64
}

type codexRateLimitContinuityStore struct {
	mu        sync.Mutex
	states    map[codexRateLimitContinuityKey]*codexRateLimitContinuityState
	nextToken uint64
	now       func() time.Time
}

type codexRateLimitContinuityAttempt struct {
	key         codexRateLimitContinuityKey
	sessionID   string
	generation  uint64
	established bool
	canaryToken uint64
}

type codexRateLimitContinuityAttemptContextKey struct{}

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

func newCodexRateLimitContinuityStore() *codexRateLimitContinuityStore {
	return &codexRateLimitContinuityStore{
		states: make(map[codexRateLimitContinuityKey]*codexRateLimitContinuityState),
		now:    time.Now,
	}
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
	s.mu.Unlock()
}

func codexRateLimitObservationWindow(cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) time.Duration {
	return time.Duration(cfg.ObservationWindowSeconds) * time.Second
}

func codexRateLimitEstablishedSessionTTL(cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) time.Duration {
	return time.Duration(cfg.EstablishedSessionTTLSeconds) * time.Second
}

func pruneCodexRateLimitContinuityState(state *codexRateLimitContinuityState, now time.Time) {
	if state == nil {
		return
	}
	for sessionID, expiresAt := range state.establishedSessions {
		if !expiresAt.After(now) {
			delete(state.establishedSessions, sessionID)
		}
	}
}

func codexRateLimitCanaryEligible(state *codexRateLimitContinuityState, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig, now time.Time) bool {
	if state == nil || !state.suspect || state.canaryToken != 0 {
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
	pruneCodexRateLimitContinuityState(state, now)
	if state.confirmed {
		return true, false, true
	}
	if !state.suspect {
		return true, false, false
	}
	if expiresAt := state.establishedSessions[sessionID]; expiresAt.After(now) {
		return true, true, false
	}
	return codexRateLimitCanaryEligible(state, cfg, now), false, false
}

func (s *codexRateLimitContinuityStore) candidateAllowed(key codexRateLimitContinuityKey, sessionID string, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) bool {
	allowed, _, _ := s.candidateDisposition(key, sessionID, cfg)
	return allowed
}

func (s *codexRateLimitContinuityStore) begin(key codexRateLimitContinuityKey, sessionID string, cfg internalconfig.EffectiveCodexRateLimitContinuityConfig) (codexRateLimitContinuityAttempt, bool) {
	attempt := codexRateLimitContinuityAttempt{key: key, sessionID: sessionID}
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
		}
		s.states[key] = state
	}
	if state.generation == 0 {
		state.generation = 1
	}
	pruneCodexRateLimitContinuityState(state, now)
	if state.confirmed {
		state.confirmed = false
		state.suspect = false
		state.observeUntil = time.Time{}
		state.establishedSuccesses = 0
		state.canaryToken = 0
		state.establishedSessions = make(map[string]time.Time)
	}
	attempt.generation = state.generation
	if expiresAt := state.establishedSessions[sessionID]; expiresAt.After(now) {
		attempt.established = true
		return attempt, true
	}
	if !state.suspect {
		return attempt, true
	}
	if !codexRateLimitCanaryEligible(state, cfg, now) {
		return attempt, false
	}
	s.nextToken++
	if s.nextToken == 0 {
		s.nextToken++
	}
	state.canaryToken = s.nextToken
	attempt.canaryToken = state.canaryToken
	return attempt, true
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
	if state.establishedSessions == nil {
		state.establishedSessions = make(map[string]time.Time)
	}
	pruneCodexRateLimitContinuityState(state, now)
	if attempt.generation != 0 && attempt.generation != state.generation {
		return codexRateLimitContinuityRecordOnly
	}
	matchingCanary := attempt.canaryToken != 0 && attempt.canaryToken == state.canaryToken

	if success {
		state.establishedSessions[attempt.sessionID] = now.Add(codexRateLimitEstablishedSessionTTL(cfg))
		switch {
		case matchingCanary:
			advanceCodexRateLimitContinuityGeneration(state)
			state.confirmed = false
			state.suspect = false
			state.observeUntil = time.Time{}
			state.establishedSuccesses = 0
			state.canaryToken = 0
		case state.suspect && attempt.established:
			if state.establishedSuccesses < cfg.EstablishedSuccessThreshold {
				state.establishedSuccesses++
			}
		case state.suspect && !attempt.established && attempt.canaryToken == 0:
			// A fresh request that was already in flight when suspicion began is
			// equivalent evidence that fresh sessions can still execute.
			advanceCodexRateLimitContinuityGeneration(state)
			state.confirmed = false
			state.suspect = false
			state.observeUntil = time.Time{}
			state.establishedSuccesses = 0
			state.canaryToken = 0
		}
		return codexRateLimitContinuityNormal
	}

	if usageLimit {
		if attempt.established || matchingCanary {
			advanceCodexRateLimitContinuityGeneration(state)
			state.confirmed = true
			state.suspect = false
			state.observeUntil = time.Time{}
			state.establishedSuccesses = 0
			state.canaryToken = 0
			state.establishedSessions = make(map[string]time.Time)
			return codexRateLimitContinuityNormal
		}
		if !state.suspect {
			state.suspect = true
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
	if s == nil || attempt.canaryToken == 0 {
		return
	}
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[attempt.key]
	if state == nil || state.canaryToken != attempt.canaryToken || (attempt.generation != 0 && attempt.generation != state.generation) {
		return
	}
	state.canaryToken = 0
	state.observeUntil = now.Add(codexRateLimitObservationWindow(cfg))
	state.establishedSuccesses = 0
}

func codexRateLimitStableSessionID(opts cliproxyexecutor.Options) string {
	sessionID := strings.TrimSpace(ExtractSessionID(opts.Headers, opts.OriginalRequest, opts.Metadata))
	for _, prefix := range []string{"execution:", "claude:", "header:", "codex:", "amp:", "conv:"} {
		if strings.HasPrefix(sessionID, prefix) && len(sessionID) > len(prefix) {
			return sessionID
		}
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
			checkModel := m.selectionModelForAuth(auth, routeModel)
			if blocked, _, _ := isAuthBlockedForModel(auth, checkModel, now); blocked {
				removedCodex = true
				removedConfirmed = true
				continue
			}
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
	if auth == nil || strings.ToLower(strings.TrimSpace(provider)) != "codex" || m.codexRateLimitContinuity == nil {
		return ctx, true
	}
	effective, sessionID, ok := m.codexRateLimitContinuityRequest(opts)
	if !ok {
		return ctx, true
	}
	modelKey := m.selectionModelKeyForAuth(auth, routeModel)
	if modelKey == "" {
		return ctx, true
	}
	attempt, allowed := m.codexRateLimitContinuity.begin(codexRateLimitContinuityKey{authID: auth.ID, model: modelKey}, sessionID, effective)
	if !allowed {
		return ctx, false
	}
	if attempt.canaryToken != 0 {
		logEntryWithRequestID(ctx).
			WithField("auth_id", auth.ID).
			WithField("model", modelKey).
			WithField("session", truncateSessionID(sessionID)).
			Info("codex rate-limit continuity: reserved fresh-session canary")
	}
	return context.WithValue(ctx, codexRateLimitContinuityAttemptContextKey{}, attempt), true
}

func codexRateLimitContinuityAttemptFromContext(ctx context.Context) (codexRateLimitContinuityAttempt, bool) {
	if ctx == nil {
		return codexRateLimitContinuityAttempt{}, false
	}
	attempt, ok := ctx.Value(codexRateLimitContinuityAttemptContextKey{}).(codexRateLimitContinuityAttempt)
	return attempt, ok && attempt.key.authID != "" && attempt.key.model != "" && attempt.sessionID != ""
}

func (m *Manager) observeCodexRateLimitContinuityResult(ctx context.Context, result Result) codexRateLimitContinuityDisposition {
	if m == nil || m.codexRateLimitContinuity == nil || strings.ToLower(strings.TrimSpace(result.Provider)) != "codex" {
		return codexRateLimitContinuityNormal
	}
	effective, ok := m.codexRateLimitContinuityPolicy()
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
		WithField("session", truncateSessionID(attempt.sessionID))
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

func (m *Manager) clearCodexRateLimitContinuityIfDisabled(cfg *internalconfig.Config) {
	if m == nil || m.codexRateLimitContinuity == nil {
		return
	}
	if cfg == nil || cfg.Home.Enabled || !cfg.Routing.SessionAffinity || !cfg.Codex.RateLimitContinuity.Effective().Enabled {
		m.codexRateLimitContinuity.clear()
	}
}
