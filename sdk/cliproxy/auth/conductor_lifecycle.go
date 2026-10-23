package auth

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
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
	var toReschedule []string
	m.mu.Lock()
	replaced = m.executors[provider]
	m.executors[provider] = executor
	for id, auth := range m.auths {
		if auth != nil && strings.EqualFold(executorKeyFromAuth(auth), provider) {
			toReschedule = append(toReschedule, id)
		}
	}
	m.mu.Unlock()

	for _, id := range toReschedule {
		m.queueRefreshReschedule(id)
	}

	if replaced == nil || replaced == executor {
		return
	}
	if compatible, ok := replaced.(ExecutionSessionReplacementCompatible); ok && compatible.CompatibleExecutorReplacement(executor) {
		return
	}
	if sharer, ok := replaced.(ExecutionSessionSharer); ok && sharer != nil && sharer.SharesExecutionSessionsWith(executor) {
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
	releaseMutation, errMutation := m.lockAuthMutationContext(ctx, auth.ID)
	if errMutation != nil {
		return nil, errMutation
	}
	defer releaseMutation()
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
	if existing, exists := m.auths[auth.ID]; exists && existing != nil {
		auth.CredentialVersion = max(auth.CredentialVersion, existing.CredentialVersion) + 1
	} else if auth.CredentialVersion == 0 {
		auth.CredentialVersion = 1
	}
	auth.Generation = 1
	// Serialize this credential, but release the manager lock during store I/O.
	// Persist failures stay non-fatal, but must not be silent: a restart would
	// lose the credential that only exists in memory.
	if errPersist := m.persistLocked(ctx, auth); errPersist != nil {
		log.WithFields(log.Fields{"auth_id": auth.ID, "credential": auth.ID, "provider": auth.Provider}).Warnf("failed to persist registered auth %s (%s): %v", auth.Provider, auth.ID, errPersist)
	}
	if m.authEpochs[auth.ID] != auth.RegistrationEpoch {
		m.mu.Unlock()
		return nil, fmt.Errorf("auth %s changed registration during persistence", auth.ID)
	}
	auth.ClearFileSnapshot()
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone
	m.notifyAuthChangeLocked(auth.ID)
	// Snapshot before unlocking: MarkResult mutates the published auth in place.
	var schedulerSnapshot *Auth
	if m.scheduler != nil {
		schedulerSnapshot = authClone.Clone()
	}
	m.mu.Unlock()
	releaseMutation()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(schedulerSnapshot)
	}
	m.structuralEpoch.Add(1)
	m.queueRefreshReschedule(auth.ID)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return auth.Clone(), nil
}

// UpdatePreparedAuth atomically merges request preparation results into the latest runtime auth
// under the manager lock, preserving concurrent modifications without modifying refresh lifecycle fields.
func (m *Manager) UpdatePreparedAuth(ctx context.Context, base, updated *Auth) (*Auth, error) {
	saved, _, err := m.updateFromAsyncWithMode(ctx, base, updated, updateModePrepare)
	return saved, err
}

// UpdateRefreshedAuth atomically merges refresh results into the latest runtime auth
// under the manager lock, preserving concurrent modifications (proxy_url, notes, weights, etc.).
func (m *Manager) UpdateRefreshedAuth(ctx context.Context, base, updated *Auth) (*Auth, error) {
	saved, _, err := m.updateFromAsyncWithMode(ctx, base, updated, updateModeRefresh)
	return saved, err
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
	return m.updateWithBase(ctx, auth, expectedGeneration, requireGeneration, nil, updateModeReplace)
}

type updateAuthMode int

const (
	updateModeReplace updateAuthMode = iota
	updateModeRefresh
	updateModePrepare
)

// updateFromAsync applies only changes made by refresh/preparation since base.
// The private lifecycle generation and public registration epoch remain fences.
func (m *Manager) updateFromAsync(ctx context.Context, base, updated *Auth) (*Auth, bool, error) {
	return m.updateFromAsyncWithMode(ctx, base, updated, updateModeReplace)
}

func (m *Manager) updateFromAsyncWithMode(ctx context.Context, base, updated *Auth, mode updateAuthMode) (*Auth, bool, error) {
	if base == nil || updated == nil {
		return nil, false, nil
	}
	if base.ID != updated.ID || base.Provider != updated.Provider {
		return nil, false, authLifecycleChangedError()
	}
	return m.updateWithBase(ctx, updated, base.generation, true, base, mode)
}

// Modify loads the latest auth for id while holding the per-auth mutation gate,
// applies edit to a clone of that auth, and commits the result through the
// standard persistence and publication path. It returns (nil, false, nil) if
// the auth is not present when the gate is acquired.
func (m *Manager) Modify(ctx context.Context, id string, expectedEpoch uint64, edit func(*Auth) error) (*Auth, bool, error) {
	id = strings.TrimSpace(id)
	if m == nil || id == "" || edit == nil {
		return nil, false, nil
	}
	releaseMutation, errMutation := m.lockAuthMutationContext(ctx, id)
	if errMutation != nil {
		return nil, false, errMutation
	}
	defer releaseMutation()

	m.mu.RLock()
	existing, ok := m.auths[id]
	var current *Auth
	var currentEpoch uint64
	if ok && existing != nil {
		current = existing.Clone()
		currentEpoch = existing.RegistrationEpoch
		if m.authEpochs != nil && m.authEpochs[id] > currentEpoch {
			currentEpoch = m.authEpochs[id]
		}
	}
	m.mu.RUnlock()
	if current == nil {
		return nil, false, nil
	}
	if expectedEpoch != 0 && expectedEpoch < currentEpoch {
		return nil, false, fmt.Errorf("update auth %s: stale registration epoch %d < %d", id, expectedEpoch, currentEpoch)
	}
	if errEdit := edit(current); errEdit != nil {
		return nil, false, errEdit
	}
	if expectedEpoch != 0 {
		current.RegistrationEpoch = expectedEpoch
	}
	committedAuth, didCommit, persistCooldown, errUpdate := m.updateInternalLocked(ctx, nil, current, updateModeReplace)
	releaseMutation()
	if errUpdate == nil && didCommit && committedAuth != nil {
		m.finishAuthUpdate(ctx, committedAuth, persistCooldown)
	}
	return committedAuth, errUpdate == nil && didCommit && committedAuth != nil, errUpdate
}

func (m *Manager) updateWithBase(ctx context.Context, auth *Auth, expectedGeneration uint64, requireGeneration bool, base *Auth, mode updateAuthMode) (*Auth, bool, error) {
	if auth == nil || auth.ID == "" {
		return nil, false, nil
	}
	releaseMutation, errMutation := m.lockAuthMutationContext(ctx, auth.ID)
	if errMutation != nil {
		return nil, false, errMutation
	}
	committedAuth, didCommit, persistCooldown, err := m.updateInternalWithGenerationLocked(ctx, base, auth, mode, expectedGeneration, requireGeneration)
	releaseMutation()
	if err == nil && didCommit && committedAuth != nil {
		m.finishAuthUpdate(ctx, committedAuth, persistCooldown)
	}
	return committedAuth, didCommit, err
}

// finishAuthUpdate runs post-commit side effects after the per-auth mutation
// gate has been released, preserving the original update ordering in which
// hooks and scheduler updates could re-enter manager operations.
func (m *Manager) finishAuthUpdate(ctx context.Context, committed *Auth, persistCooldown bool) {
	if committed == nil {
		return
	}
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(committed.Clone())
	}
	m.structuralEpoch.Add(1)
	m.queueRefreshReschedule(committed.ID)
	m.hook.OnAuthUpdated(ctx, committed.Clone())
	if persistCooldown {
		m.persistCooldownStates(context.Background())
	}
}

// updateInternalLocked assumes the caller already holds the per-auth
// mutation lock. It only commits manager state; callers run
// finishAuthUpdate after releasing the gate. persistCooldownStates is also
// deferred by callers because it may re-enter manager operations.
// updateInternalLocked commits an update while the caller holds the gate.
// The second return value reports whether this call actually committed new
// state (versus returning the current snapshot for a discarded stale
// update); only real commits may run post-commit side effects.
func (m *Manager) updateInternalLocked(ctx context.Context, base, auth *Auth, mode updateAuthMode) (*Auth, bool, bool, error) {
	return m.updateInternalWithGenerationLocked(ctx, base, auth, mode, 0, false)
}

func (m *Manager) updateInternalWithGenerationLocked(ctx context.Context, base, auth *Auth, mode updateAuthMode, expectedGeneration uint64, requireGeneration bool) (committedAuth *Auth, committed bool, persistCooldown bool, err error) {
	if auth == nil || auth.ID == "" {
		return nil, false, false, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, false, false, fmt.Errorf("update auth: %w", errWeight)
	}
	persistMetaMint := (mode == updateModePrepare || mode == updateModeRefresh) && strings.EqualFold(strings.TrimSpace(auth.Provider), "meta")
	isFileSnapshot := mode == updateModeReplace && auth.HasFileSnapshot()
	if isFileSnapshot {
		m.mu.RLock()
		existing, ok := m.auths[auth.ID]
		m.mu.RUnlock()
		if !ok || existing == nil {
			return nil, false, false, nil
		}
		// Validate file snapshot freshness under the per-auth mutation gate
		// without holding m.mu across filesystem I/O.
		if !auth.FileSnapshotMatchesDisk() {
			m.mu.RLock()
			current := m.auths[auth.ID].Clone()
			m.mu.RUnlock()
			return current, false, false, nil
		}
	}
	m.mu.Lock()
	existing, ok := m.auths[auth.ID]
	if !ok || existing == nil {
		m.mu.Unlock()
		return nil, false, false, nil
	}
	if base != nil && existing.RegistrationEpoch != base.RegistrationEpoch {
		m.mu.Unlock()
		return nil, false, false, authLifecycleChangedError()
	}
	if requireGeneration && existing.generation != expectedGeneration {
		m.mu.Unlock()
		if persistMetaMint {
			return nil, false, false, authLifecycleChangedError()
		}
		return nil, false, false, nil
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	if (mode == updateModeRefresh || mode == updateModePrepare) && base != nil && existing.RegistrationEpoch != base.RegistrationEpoch {
		m.mu.Unlock()
		return nil, false, false, fmt.Errorf("update auth %s: stale registration epoch %d != %d", auth.ID, base.RegistrationEpoch, existing.RegistrationEpoch)
	}
	// Do not let an in-flight refresh overwrite credentials committed after its snapshot.
	if mode == updateModeRefresh && base != nil && (existing.CredentialVersion != base.CredentialVersion || CredentialsChanged(base, existing)) {
		if auth.CodexRefreshPatch == nil || !auth.CodexRefreshPatch.CanApplyTo(existing.Metadata) {
			current := existing.Clone()
			m.mu.Unlock()
			// Discarded stale refresh: committed=false so callers do not
			// run finishAuthUpdate for a state this call did not change.
			return current, false, false, nil
		}
	}
	if base != nil {
		patch := auth.CodexRefreshPatch
		auth = mergeAsyncAuth(base, existing, auth)
		if mode == updateModeRefresh && patch != nil {
			auth.CodexRefreshPatch = patch
			auth.Metadata = patch.Apply(auth.Metadata)
		}
		NormalizeCredentialMetadata(auth.Metadata)
	}
	if auth.RegistrationEpoch != 0 && auth.RegistrationEpoch < m.authEpochs[auth.ID] {
		m.mu.Unlock()
		return nil, false, false, fmt.Errorf("update auth %s: stale registration epoch %d < %d", auth.ID, auth.RegistrationEpoch, m.authEpochs[auth.ID])
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
	if isFileSnapshot && auth.CreatedAt.IsZero() {
		auth.CreatedAt = existing.CreatedAt
	}
	auth.Success = existing.Success
	auth.Failed = existing.Failed
	auth.recentRequests = existing.recentRequests
	if auth.Generation <= existing.Generation {
		auth.Generation = existing.Generation + 1
	} else {
		auth.Generation++
	}
	existingVersion := existing.CredentialVersion
	if existingVersion == 0 {
		existingVersion = 1
	}
	credChanged := CredentialsChanged(existing, auth)
	if credChanged {
		auth.CredentialVersion = max(auth.CredentialVersion, existingVersion) + 1
	} else {
		auth.CredentialVersion = existingVersion
	}
	cooldownStateChanged := false
	if sameProvider {
		if credChanged || mode == updateModeRefresh {
			applyPreservedRejectedTokens(existing, auth, mode == updateModeRefresh)
		} else {
			applyPreservedRejectedTokens(existing, auth, false)
		}
		auth.generation = existing.generation
		if auth.generation == 0 {
			auth.generation = m.nextAuthGenerationLocked()
		}
		if !existing.Disabled && existing.Status != StatusDisabled && !auth.Disabled && auth.Status != StatusDisabled {
			if isFileSnapshot {
				auth.ModelStates = existing.ModelStates
				auth.LastRefreshedAt = existing.LastRefreshedAt
				auth.NextRefreshAfter = existing.NextRefreshAfter
				auth.RefreshFailures = existing.RefreshFailures
				if (auth.Status == "" || auth.Status == StatusActive) && existing.Status != "" {
					auth.Status = existing.Status
				}
				if auth.StatusMessage == "" {
					auth.StatusMessage = existing.StatusMessage
				}
				auth.Unavailable = existing.Unavailable
				auth.NextRetryAfter = existing.NextRetryAfter
				if auth.LastError == nil && existing.LastError != nil {
					auth.LastError = cloneError(existing.LastError)
				}
				observation := mergeQuotaObservation(auth.Quota, existing.Quota)
				auth.Quota = existing.Quota.Clone()
				auth.Quota.ObservedAt = observation.ObservedAt
				auth.Quota.Signals = observation.Signals
				auth.cooldownRecordedSeq = existing.cooldownRecordedSeq
			} else if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
				auth.ModelStates = existing.ModelStates
			}
			credChanged := CredentialsChanged(existing, auth)
			if credChanged && (auth.RejectedAccessToken == "" || authAccessToken(auth) != auth.RejectedAccessToken) {
				if hasUnauthorizedAuthFailure(existing) || (auth.LastError != nil && isUnauthorizedError(auth.LastError)) {
					auth.Unavailable = false
					auth.NextRetryAfter = time.Time{}
					auth.cooldownRecordedSeq = 0
					auth.LastError = nil
					auth.NextRetryAfter = time.Time{}
					auth.StatusMessage = ""
					auth.Status = StatusActive
					cooldownStateChanged = true
				}
				resumed := clearUnauthorizedModelStates(auth, time.Now())
				if len(resumed) > 0 {
					cooldownStateChanged = true
				}
			} else if hasUnauthorizedAuthFailure(existing) {
				// Re-synthesizing the same credentials (metadata, note or model
				// settings edits) must not revive a credential whose refresh token
				// was rejected; only new credential material clears this state.
				auth.Unavailable = true
				auth.Status = existing.Status
				auth.StatusMessage = existing.StatusMessage
				auth.LastError = cloneError(existing.LastError)
				auth.NextRefreshAfter = time.Time{}
				auth.NextRetryAfter = time.Time{}
				cooldownStateChanged = true
			}
			devinQuotaRestored := mode == updateModeRefresh && applyDevinQuotaRestoration(auth, existing, now)
			if devinQuotaRestored {
				cooldownStateChanged = true
			} else if existing.Quota.Exceeded && existing.Quota.Reason == "credential_quota" && existing.Quota.NextRecoverAt.After(now) {
				auth.Unavailable = existing.Unavailable
				auth.NextRetryAfter = existing.NextRetryAfter
				observation := mergeQuotaObservation(auth.Quota, existing.Quota)
				auth.Quota = existing.Quota
				auth.Quota.ObservedAt = observation.ObservedAt
				auth.Quota.Signals = observation.Signals
				auth.cooldownRecordedSeq = existing.cooldownRecordedSeq
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
	cooldownStateChanged = normalizeModelStates(auth) || cooldownStateChanged
	if !sameProvider {
		cooldownStateChanged = true
	}
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	if existing.Metadata != nil {
		if persistedHints, hasHints := existing.Metadata["antigravity_capability_hints"]; hasHints {
			if auth.Metadata == nil {
				auth.Metadata = make(map[string]any)
			}
			auth.Metadata["antigravity_capability_hints"] = persistedHints
		}
	}
	auth.EnsureIndex()
	// Save before publication, including the transactional Meta mint path.
	// Runtime-only changes made during I/O are merged below, never overwritten.
	existingBeforeSave := existing.Clone()
	refreshPatchBeforeSave := auth.CodexRefreshPatch
	if errPersist := m.persistLocked(ctx, auth); errPersist != nil {
		// A minted Meta key must reach the store before requests can use it.
		if persistMetaMint {
			m.mu.Unlock()
			return nil, false, false, fmt.Errorf("persist meta auth: %w", errPersist)
		}
		// Ordinary persistence failures remain non-fatal, but never silent.
		log.WithFields(log.Fields{"auth_id": auth.ID, "credential": auth.ID, "provider": auth.Provider}).Warnf("failed to persist updated auth %s (%s): %v", auth.Provider, auth.ID, errPersist)
	}
	if m.authEpochs[auth.ID] != auth.RegistrationEpoch {
		m.mu.Unlock()
		return nil, false, false, fmt.Errorf("auth %s changed registration during persistence", auth.ID)
	}
	mergeAuthSaveDelta(auth, existingBeforeSave, existing, false)
	if (auth.RejectedAccessToken == "" && existing.RejectedAccessToken != "") || (auth.RejectedSIWCAccessToken == "" && existing.RejectedSIWCAccessToken != "") {
		probe := auth.Clone()
		probe.CodexRefreshPatch = refreshPatchBeforeSave
		applyPreservedRejectedTokens(existing, probe, mode == updateModeRefresh)
		auth.RejectedAccessToken = probe.RejectedAccessToken
		auth.RejectedSIWCAccessToken = probe.RejectedSIWCAccessToken
	}
	if existing.Generation >= auth.Generation {
		auth.Generation = existing.Generation + 1
	}
	auth.ClearFileSnapshot()
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone
	m.notifyAuthChangeLocked(auth.ID)
	// Snapshot before unlocking: MarkResult mutates the published auth in place.
	var schedulerSnapshot *Auth
	if m.scheduler != nil {
		schedulerSnapshot = authClone.Clone()
	}
	m.mu.Unlock()
	_ = schedulerSnapshot
	return auth.Clone(), true, cooldownStateChanged, nil
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
	releaseMutation := m.lockAuthMutation(id)
	defer releaseMutation()

	m.mu.Lock()
	existing := m.auths[id]
	if existing == nil {
		m.mu.Unlock()
		return
	}
	provider := strings.TrimSpace(existing.Provider)
	delete(m.auths, id)
	m.notifyAuthChangeLocked(id)
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
	releaseMutation()

	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.RecordRemovalTombstone(id, tombstoneEpoch)
	}
	m.structuralEpoch.Add(1)
	m.queueRefreshUnschedule(id)
	m.invalidateSessionAffinity(id)

	if provider != "" {
		if exec, ok := m.Executor(provider); ok && exec != nil {
			if closer, okCloser := exec.(AuthExecutionSessionCloser); okCloser {
				closer.CloseExecutionSessionsForAuth(existing.ID, existing.Index)
			} else if authCloser, okAuthCloser := exec.(authExecutionSessionIDCloser); okAuthCloser && authCloser != nil {
				authCloser.CloseAuthExecutionSessions(id)
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
	// Exclude complete Save/publication transactions, including new registrations
	// not yet present in auths. Never wait for this barrier while holding m.mu.
	lockCtx := ctx
	if lockCtx == nil {
		lockCtx = context.Background()
	}
	if errAcquire := m.authLoadGate.Acquire(lockCtx, math.MaxInt64); errAcquire != nil {
		return errAcquire
	}
	defer m.authLoadGate.Release(math.MaxInt64)
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == nil {
		return nil
	}
	items, err := store.List(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
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
		if prev, exists := previousAuths[auth.ID]; exists && prev != nil {
			auth.CredentialVersion = max(auth.CredentialVersion, prev.CredentialVersion)
			if CredentialsChanged(prev, auth) {
				auth.CredentialVersion++
			}
		}
		if auth.CredentialVersion == 0 {
			auth.CredentialVersion = 1
		}
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
	for id := range m.authChangeWatchers {
		m.notifyAuthChangeLocked(id)
	}
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, rt := range removedTombstones {
			m.scheduler.RecordRemovalTombstone(rt.id, rt.epoch)
		}
	}
	m.structuralEpoch.Add(1)
	m.syncScheduler()
	return nil
}

type authPersistLock struct {
	mu             sync.Mutex
	lastEpoch      uint64
	lastGeneration uint64
}

func (m *Manager) persistLockFor(authID string) *authPersistLock {
	lockValue, _ := m.persistLocks.LoadOrStore(authID, &authPersistLock{})
	lock, _ := lockValue.(*authPersistLock)
	if lock == nil {
		lock = &authPersistLock{}
		m.persistLocks.Store(authID, lock)
	}
	return lock
}

// persist saves the latest snapshot for this lifecycle. It re-reads m.auths
// under m.mu, so callers must not hold m.mu: capture a clone inside the
// critical section and persist after unlocking.
func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil || shouldSkipPersist(ctx) {
		return nil
	}
	lock := m.persistLockFor(auth.ID)
	lock.mu.Lock()
	defer lock.mu.Unlock()
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
	return m.persistSnapshotWithLock(ctx, auth, lock)
}

// persistSnapshotWithLock saves an already lifecycle-validated snapshot. The caller
// must hold lock when persistence is enabled; Meta prepare/refresh acquires it before
// m.mu so the minted credential is durable before it becomes request-visible without
// reversing the normal persist-lock -> manager-lock ordering.
func (m *Manager) persistSnapshotWithLock(ctx context.Context, auth *Auth, lock *authPersistLock) error {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == nil || auth == nil || shouldSkipPersist(ctx) {
		return nil
	}
	if lock == nil {
		return fmt.Errorf("persist auth: missing lifecycle lock")
	}
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return fmt.Errorf("persist auth: %w", errWeight)
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

	if auth.RegistrationEpoch < lock.lastEpoch || (auth.RegistrationEpoch == lock.lastEpoch && auth.Generation < lock.lastGeneration) {
		return nil
	}
	lock.lastEpoch = auth.RegistrationEpoch
	lock.lastGeneration = auth.Generation
	if shouldSkipPersist(ctx) {
		return nil
	}
	releaseFg, errWait := m.beginForegroundStoreSave(ctx)
	if errWait != nil {
		return errWait
	}
	defer releaseFg()
	_, err := store.Save(ctx, auth)
	return err
}
