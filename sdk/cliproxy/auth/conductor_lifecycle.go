package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// SetRetryConfig updates additional credential retry rounds, the per-round credential limit, and the cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration, maxRetryCredentials int) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryCredentials < 0 {
		maxRetryCredentials = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryCredentials.Store(int32(maxRetryCredentials))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	provider := strings.TrimSpace(executor.Identifier())
	if provider == "" {
		return
	}

	var replaced ProviderExecutor
	m.mu.Lock()
	replaced = m.executors[provider]
	m.executors[provider] = executor
	m.mu.Unlock()

	if replaced == nil || replaced == executor {
		return
	}
	if compatible, ok := replaced.(ExecutionSessionReplacementCompatible); ok && compatible.CompatibleExecutorReplacement(executor) {
		return
	}
	if closer, ok := replaced.(ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(CloseAllExecutionSessionsID)
	}
}

// UnregisterExecutor removes the executor associated with the provider key.
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	removed := m.executors[provider]
	delete(m.executors, provider)
	m.mu.Unlock()
	if closer, ok := removed.(ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(CloseAllExecutionSessionsID)
	}
}

// Register inserts a new auth entry into the manager.
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("register auth: %w", errWeight)
	}
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	now := time.Now()
	if auth.Generation == 0 {
		auth.Generation = 1
	}
	if auth.CreatedAt.IsZero() {
		auth.CreatedAt = now
	}
	auth.UpdatedAt = now
	cooldownStateChanged := normalizeModelStates(auth)
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	m.mu.Lock()
	auth.generation = m.nextAuthGenerationLocked()
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing, exists := m.auths[auth.ID]; exists && existing != nil && existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	if auth.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = auth.RegistrationEpoch
	}
	m.authEpochs[auth.ID]++
	auth.RegistrationEpoch = m.authEpochs[auth.ID]
	auth.Generation = 1
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone.Clone())
	}
	m.queueRefreshReschedule(auth.ID)
	_ = m.persist(ctx, auth)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return auth.Clone(), nil
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	saved, _, err := m.update(ctx, auth, 0, false)
	return saved, err
}

func (m *Manager) updateIfGeneration(ctx context.Context, auth *Auth, expectedGeneration uint64) (*Auth, bool, error) {
	return m.update(ctx, auth, expectedGeneration, true)
}

func (m *Manager) update(ctx context.Context, auth *Auth, expectedGeneration uint64, requireGeneration bool) (*Auth, bool, error) {
	return m.updateWithBase(ctx, auth, expectedGeneration, requireGeneration, nil)
}

// updateFromAsync applies only changes made by refresh/preparation since base.
// The private lifecycle generation and public registration epoch remain fences.
func (m *Manager) updateFromAsync(ctx context.Context, base, updated *Auth) (*Auth, bool, error) {
	if base == nil || updated == nil {
		return nil, false, nil
	}
	if base.ID != updated.ID || base.Provider != updated.Provider {
		return nil, false, authLifecycleChangedError()
	}
	return m.updateWithBase(ctx, updated, base.generation, true, base)
}

func (m *Manager) updateWithBase(ctx context.Context, auth *Auth, expectedGeneration uint64, requireGeneration bool, base *Auth) (*Auth, bool, error) {
	if auth == nil || auth.ID == "" {
		return nil, false, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, false, fmt.Errorf("update auth: %w", errWeight)
	}
	m.mu.Lock()
	existing, ok := m.auths[auth.ID]
	if !ok || existing == nil {
		m.mu.Unlock()
		return nil, false, nil
	}
	if requireGeneration && existing.generation != expectedGeneration {
		m.mu.Unlock()
		return nil, false, nil
	}
	if base != nil {
		if existing.RegistrationEpoch != base.RegistrationEpoch {
			m.mu.Unlock()
			return nil, false, authLifecycleChangedError()
		}
		auth = mergeAsyncAuth(base, existing, auth)
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	if auth.RegistrationEpoch != 0 && auth.RegistrationEpoch < m.authEpochs[auth.ID] {
		m.mu.Unlock()
		return nil, false, fmt.Errorf("update auth %s: stale registration epoch %d < %d", auth.ID, auth.RegistrationEpoch, m.authEpochs[auth.ID])
	}
	if auth.RegistrationEpoch >= m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = auth.RegistrationEpoch
	} else if auth.RegistrationEpoch == 0 {
		auth.RegistrationEpoch = m.authEpochs[auth.ID]
	}
	if !auth.indexAssigned && auth.Index == "" {
		auth.Index = existing.Index
		auth.indexAssigned = existing.indexAssigned
	}
	now := time.Now()
	sameProvider := strings.EqualFold(strings.TrimSpace(existing.Provider), strings.TrimSpace(auth.Provider))
	auth.Success = existing.Success
	auth.Failed = existing.Failed
	auth.recentRequests = existing.recentRequests
	if auth.Generation <= existing.Generation {
		auth.Generation = existing.Generation + 1
	} else {
		auth.Generation++
	}
	if sameProvider {
		auth.generation = existing.generation
		if auth.generation == 0 {
			auth.generation = m.nextAuthGenerationLocked()
		}
		if !existing.Disabled && existing.Status != StatusDisabled && !auth.Disabled && auth.Status != StatusDisabled {
			if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
				auth.ModelStates = existing.ModelStates
			}
			if existing.Quota.Exceeded && existing.Quota.Reason == "credential_quota" && existing.Quota.NextRecoverAt.After(now) {
				auth.Unavailable = existing.Unavailable
				auth.NextRetryAfter = existing.NextRetryAfter
				auth.Quota = existing.Quota
				if auth.Status == StatusActive {
					auth.Status = existing.Status
				}
			}
		}
	} else {
		auth.generation = m.nextAuthGenerationLocked()
		clearProviderRuntimeState(auth, now)
	}
	auth.UpdatedAt = now
	cooldownStateChanged := normalizeModelStates(auth)
	if !sameProvider {
		cooldownStateChanged = true
	}
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone.Clone())
	}
	m.queueRefreshReschedule(auth.ID)
	_ = m.persist(ctx, auth)
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return auth.Clone(), true, nil
}

// Remove deletes an auth from runtime state without persisting.
// Disk and token-store deletion must be handled by the caller.
func (m *Manager) Remove(ctx context.Context, id string) {
	if m == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	_ = ctx

	m.mu.Lock()
	existing := m.auths[id]
	if existing == nil {
		m.mu.Unlock()
		return
	}
	provider := strings.TrimSpace(existing.Provider)
	delete(m.auths, id)
	if m.modelPoolOffsets != nil {
		delete(m.modelPoolOffsets, id)
	}
	for sessionID, sessionAuths := range m.homeRuntimeAuths {
		if sessionAuths == nil {
			continue
		}
		delete(sessionAuths, id)
		if len(sessionAuths) == 0 {
			delete(m.homeRuntimeAuths, sessionID)
		}
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing.RegistrationEpoch > m.authEpochs[id] {
		m.authEpochs[id] = existing.RegistrationEpoch
	}
	m.authEpochs[id]++
	tombstoneEpoch := m.authEpochs[id]
	m.mu.Unlock()

	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.RecordRemovalTombstone(id, tombstoneEpoch)
	}
	m.queueRefreshUnschedule(id)
	m.invalidateSessionAffinity(id)

	if provider != "" {
		if exec, ok := m.Executor(provider); ok && exec != nil {
			if closer, okCloser := exec.(AuthExecutionSessionCloser); okCloser {
				closer.CloseExecutionSessionsForAuth(existing.ID, existing.Index)
			} else if closer, okCloser := exec.(ExecutionSessionCloser); okCloser {
				closer.CloseExecutionSession(CloseAllExecutionSessionsID)
			}
		}
	}
	m.persistCooldownStates(context.Background())
}
func (m *Manager) invalidateSessionAffinity(authID string) {
	if m == nil || authID == "" {
		return
	}
	m.mu.Lock()
	if m.codexRateLimitContinuity != nil {
		m.codexRateLimitContinuity.removeAuth(authID)
	}
	selector := m.selector
	m.mu.Unlock()
	if invalidator, ok := selector.(interface{ InvalidateAuth(string) }); ok && invalidator != nil {
		invalidator.InvalidateAuth(authID)
	}
}

func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	if m.store == nil {
		m.mu.Unlock()
		return nil
	}
	items, err := m.store.List(ctx)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	previousAuths := m.auths
	m.auths = make(map[string]*Auth, len(items))
	if m.codexRateLimitContinuity != nil {
		// Credentials are a new runtime snapshot; continuity leases cannot be
		// safely carried over a Load boundary.
		m.advanceCodexRateLimitContinuityLifecycleLocked()
		m.codexRateLimitContinuity.clear()
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64, len(items))
	}
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		NormalizeCredentialMetadata(auth.Metadata)
		if errWeight := ValidateAuthWeight(auth); errWeight != nil {
			continue
		}
		auth.EnsureIndex()
		auth.generation = m.nextAuthGenerationLocked()
		m.authEpochs[auth.ID] = max(m.authEpochs[auth.ID], auth.RegistrationEpoch) + 1
		auth.RegistrationEpoch = m.authEpochs[auth.ID]
		auth.Generation = 1
		m.auths[auth.ID] = auth.Clone()
	}

	type removalTombstone struct {
		id    string
		epoch uint64
	}
	var removedTombstones []removalTombstone
	for prevID := range previousAuths {
		if _, exists := m.auths[prevID]; !exists {
			m.authEpochs[prevID]++
			removedTombstones = append(removedTombstones, removalTombstone{
				id:    prevID,
				epoch: m.authEpochs[prevID],
			})
		}
	}

	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.rebuildAPIKeyModelAliasLocked(cfg)
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, rt := range removedTombstones {
			m.scheduler.RecordRemovalTombstone(rt.id, rt.epoch)
		}
	}
	m.syncScheduler()
	return nil
}

// Load resets manager state from the backing store.

// persist saves the latest snapshot for this lifecycle. It re-reads m.auths
// under m.mu, so callers must not hold m.mu: capture a clone inside the
// critical section and persist after unlocking.
func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil || shouldSkipPersist(ctx) {
		return nil
	}
	value, _ := m.persistLocks.LoadOrStore(auth.ID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	// A newer update may have reached persistence first. Save the latest
	// snapshot for this lifecycle rather than overwriting it with an old copy.
	m.mu.RLock()
	current := m.auths[auth.ID]
	if current == nil || current.generation != auth.generation || current.RegistrationEpoch != auth.RegistrationEpoch {
		m.mu.RUnlock()
		return nil
	}
	auth = current.Clone()
	m.mu.RUnlock()
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return fmt.Errorf("persist auth: %w", errWeight)
	}
	if shouldSkipPersist(ctx) {
		return nil
	}
	if IsConfigAPIKeyAuth(auth) {
		return nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	if IsPluginVirtualAuth(auth) {
		return nil
	}
	// Skip persistence when metadata is absent (e.g., runtime-only auths).
	if auth.Metadata == nil {
		return nil
	}
	_, err := m.store.Save(ctx, auth)
	return err
}
