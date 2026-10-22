package cliproxy

import (
	"context"
	"errors"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// pluginModelRegistrationRetryInterval bounds automatic rediscovery of plugin
	// auth models after a retryable discovery failure (network, 429, 5xx). Auth
	// rejections surfaced by plugins (401/403 style credentials) are terminal and
	// never scheduled here; they recover through explicit refresh, file changes,
	// or restart after the credential itself is fixed.
	pluginModelRegistrationRetryInterval = 5 * time.Minute
)

// pluginModelRegistrationRetry schedules one future rediscovery for auths whose
// plugin model discovery failed with a retryable error. It only exists so a
// recovered upstream re-enters the registry and scheduler without a manual
// refresh. Credential rejection still requires an explicit recovery action.
type pluginModelRegistrationRetry struct {
	mu      sync.Mutex
	service *Service
	ctx     context.Context
	cancel  context.CancelFunc
	pending map[string]time.Time
	timer   *time.Timer
	stopped bool
}

func newPluginModelRegistrationRetry(service *Service) *pluginModelRegistrationRetry {
	return &pluginModelRegistrationRetry{service: service, pending: make(map[string]time.Time)}
}

// start enables scheduling. Safe to call multiple times; without start, schedule
// calls are no-ops (used by short-lived service snapshots in tests).
func (r *pluginModelRegistrationRetry) start(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.ctx != nil {
		return
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
}

func (r *pluginModelRegistrationRetry) stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.stopped = true
	if r.cancel != nil {
		r.cancel()
	}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.pending = make(map[string]time.Time)
}

// schedule records a retryable discovery failure. Terminal rejections are
// ignored by the caller before reaching here.
func (r *pluginModelRegistrationRetry) schedule(authID string) {
	if r == nil || authID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || r.ctx == nil || r.ctx.Err() != nil {
		return
	}
	if _, exists := r.pending[authID]; exists {
		return
	}
	r.pending[authID] = time.Now().Add(pluginModelRegistrationRetryInterval)
	r.armTimerLocked()
}

// clear drops a pending retry after successful discovery or terminal rejection.
func (r *pluginModelRegistrationRetry) clear(authID string) {
	if r == nil || authID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, authID)
	if len(r.pending) == 0 && r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

func (r *pluginModelRegistrationRetry) armTimerLocked() {
	if r.stopped || r.ctx == nil || len(r.pending) == 0 {
		return
	}
	earliest := time.Now()
	first := true
	for _, at := range r.pending {
		if first || at.Before(earliest) {
			earliest = at
			first = false
		}
	}
	delay := time.Until(earliest)
	if delay < 0 {
		delay = 0
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(delay, r.drain)
}

// drain retries due auths on background goroutines and re-arms itself for the
// rest. It never blocks the timer thread with registration work.
func (r *pluginModelRegistrationRetry) drain() {
	r.mu.Lock()
	if r.stopped || r.ctx == nil {
		r.mu.Unlock()
		return
	}
	ctx := r.ctx
	now := time.Now()
	due := make([]string, 0, len(r.pending))
	for authID, at := range r.pending {
		if !now.Before(at) {
			due = append(due, authID)
			delete(r.pending, authID)
		}
	}
	r.timer = nil
	r.mu.Unlock()

	if len(due) == 0 {
		r.mu.Lock()
		r.armTimerLocked()
		r.mu.Unlock()
		return
	}
	go func() {
		for _, authID := range due {
			r.retryOne(ctx, authID)
		}
		r.mu.Lock()
		r.armTimerLocked()
		r.mu.Unlock()
	}()
}

func (r *pluginModelRegistrationRetry) retryOne(ctx context.Context, authID string) {
	s := r.service
	if s == nil || s.coreManager == nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	current, ok := s.coreManager.GetByID(authID)
	if !ok || current == nil || current.Disabled {
		return
	}
	auth := current.Clone()
	if _, err := s.refreshModelRegistrationForAuthWithContextResult(ctx, auth, nil, true); err != nil {
		if pluginModelDiscoveryRetryable(err) {
			r.schedule(authID)
		}
		return
	}
	log.Infof("plugin model registration retry succeeded for auth %s", authID)
}

// pluginModelDiscoveryRetryable reports whether a plugin model discovery error
// should get a bounded automatic retry. Plugin RPC errors keep only their code
// and HTTP status; 401/403 mean the credential itself was rejected and retrying
// would just probe the vendor with a dead key, so they are terminal here.
func pluginModelDiscoveryRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var coded interface{ PluginCode() string }
	if errors.As(err, &coded) {
		var status interface{ StatusCode() int }
		if errors.As(err, &status) {
			switch status.StatusCode() {
			case 401, 403:
				return false
			}
		}
		switch coded.PluginCode() {
		case "invalid_auth", "catalog_rejected", "auth_rejected", "catalog_auth_rejected", "unsupported_model", "catalog_empty":
			return false
		}
	}
	return true
}
