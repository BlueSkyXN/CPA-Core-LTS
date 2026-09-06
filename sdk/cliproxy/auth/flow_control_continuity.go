package auth

import (
	"context"
	"sync"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type flowContinuityContextKey struct{}
type flowDeferredContinuity struct {
	mu         sync.Mutex
	startMu    sync.Mutex
	provider   string
	routeModel string
	opts       cliproxyexecutor.Options
	activated  bool
	attempt    codexRateLimitContinuityAttempt
}

// Queue waiters must not become continuity incumbents/canaries. Reserve the
// existing continuity attempt only after all local execution slots are acquired.
// The shared holder makes the old outer deferred abandon/result paths see the
// same attempt even when they retain a parent context from before admission.
func (m *Manager) activateFlowContinuity(ctx context.Context, auth *Auth) (context.Context, bool) {
	if ctx == nil || m == nil {
		return ctx, true
	}
	deferred, ok := ctx.Value(flowContinuityContextKey{}).(*flowDeferredContinuity)
	if !ok {
		return ctx, true
	}
	deferred.startMu.Lock()
	defer deferred.startMu.Unlock()
	deferred.mu.Lock()
	activated := deferred.activated
	deferred.mu.Unlock()
	if activated {
		return ctx, true
	}
	// The original admission does not read this holder, avoiding recursive locking.
	next, allowed := m.beginCodexRateLimitContinuityNow(ctx, auth, deferred.provider, deferred.routeModel, deferred.opts)
	if !allowed {
		return ctx, false
	}
	deferred.mu.Lock()
	defer deferred.mu.Unlock()
	deferred.activated = true
	if next != nil {
		deferred.attempt, _ = next.Value(codexRateLimitContinuityAttemptContextKey{}).(codexRateLimitContinuityAttempt)
	}
	return next, true
}
