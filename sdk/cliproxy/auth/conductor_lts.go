package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"net/http"

	"sort"

	"strings"

	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexmetadata"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func registrySuspensionForModelState(state *ModelState) (reason string, quota, suspend bool) {
	if state == nil {
		return "", false, false
	}
	if state.Quota.Exceeded && strings.EqualFold(strings.TrimSpace(state.Quota.Reason), "quota") {
		return "quota", true, true
	}
	if isModelSupportResultError(state.LastError) {
		return "model_not_supported", false, true
	}
	if isInvalidGrantResultError(state.LastError) {
		return "invalid_grant", false, true
	}
	if isCloudflareChallengeResultError(state.LastError) {
		return "", false, false
	}
	if state.LastError != nil && strings.EqualFold(strings.TrimSpace(state.LastError.Code), "unauthorized") {
		return "unauthorized", false, true
	}
	switch statusCodeFromResult(state.LastError) {
	case http.StatusUnauthorized:
		return "unauthorized", false, true
	case http.StatusPaymentRequired, http.StatusForbidden:
		return "payment_required", false, true
	case http.StatusNotFound:
		return "not_found", false, true
	default:
		return "", false, false
	}
}

// PruneExpiredAvailability clears availability state only when an explicit
// cooldown deadline has elapsed. This keeps Management status aligned with
// scheduler routing without treating zero-deadline or non-resettable failures
// as recovered.
func (m *Manager) PruneExpiredAvailability(ctx context.Context, now time.Time) int {
	if m == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}

	changedAuthCount := 0
	persistCooldownState := false
	reg := registry.GetGlobalRegistry()

	m.mu.Lock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		authExpired := authAvailabilityExpired(auth, now)
		preserveAuthAvailability := !authExpired && authAvailabilityStatePresent(auth) && !authAvailabilityMirrorsExpiredModel(auth, now)
		prunedModels := make([]string, 0)
		for model, state := range auth.ModelStates {
			if !modelAvailabilityExpired(state, now) {
				continue
			}
			resetModelState(state, now)
			model = strings.TrimSpace(model)
			if model != "" {
				prunedModels = append(prunedModels, model)
				reg.ClearModelQuotaExceeded(auth.ID, model)
				reg.ResumeClientModel(auth.ID, model)
			}
		}
		if !authExpired && len(prunedModels) == 0 {
			continue
		}

		if allModelStatesClean(auth) && (authExpired || !preserveAuthAvailability) {
			clearAuthStateOnSuccess(auth, now)
			reg.ClearClientQuotaState(auth.ID)
		} else if authExpired || !preserveAuthAvailability {
			recomputeAggregatedAvailability(auth, now)
			reconcileAuthErrorFromModelStates(auth, now)
		}
		auth.UpdatedAt = now
		if errPersist := m.persist(ctx, auth); errPersist != nil {
			logEntryWithRequestID(ctx).WithField("auth_id", auth.ID).Warnf("failed to persist auth changes during expired availability pruning: %v", errPersist)
		}
		if m.scheduler != nil {

			m.scheduler.upsertAuth(auth)
		}
		changedAuthCount++
		persistCooldownState = persistCooldownState || m.cooldownStore != nil
	}
	m.mu.Unlock()

	if persistCooldownState {
		m.persistCooldownStates(ctx)
	}
	return changedAuthCount
}

// ClearQuotaState clears quota-related auth and registry state for one auth.
func (m *Manager) ClearQuotaState(ctx context.Context, authID string) bool {
	if m == nil {
		return false
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	registryCleared := registry.GetGlobalRegistry().ClearClientQuotaState(authID)

	var snapshot *Auth
	managerChanged := false
	now := time.Now()

	m.mu.Lock()
	if auth, ok := m.auths[authID]; ok && auth != nil {
		managerChanged = clearQuotaStateForAuth(auth, now)
		if managerChanged {
			snapshot = auth.Clone()
		}
	}
	m.mu.Unlock()

	if managerChanged && snapshot != nil {
		if errPersist := m.persist(ctx, snapshot); errPersist != nil {
			logEntryWithRequestID(ctx).WithField("auth_id", snapshot.ID).Warnf("failed to persist auth quota reset state: %v", errPersist)
		}
	}
	if m.scheduler != nil {
		switch {
		case snapshot != nil:
			m.scheduler.upsertAuth(snapshot)
		case registryCleared > 0:
			m.RefreshSchedulerEntry(authID)
		}
	}
	return managerChanged || registryCleared > 0
}

func (m *Manager) preflightCodexClientMetadata(providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Options, error) {
	codexOnly := codexOnlyProviders(providers)
	xaiEligible := containsProvider(normalizeProviderKeys(providers), "xai")
	if !codexOnly && !xaiEligible {
		return opts, nil
	}

	directTurnMetadata := codexTurnMetadataHeaderValue(opts.Headers)
	body := req.Payload
	if len(body) == 0 {
		body = opts.OriginalRequest
	}
	trimmedBody := bytes.TrimSpace(body)
	if len(trimmedBody) == 0 || trimmedBody[0] != '{' {
		if strings.TrimSpace(directTurnMetadata) == "" {
			return opts, nil
		}
		body = []byte(`{}`)
	}
	if xaiEligible && !codexOnly && !hasCodexTurnMetadataCarrier(body, directTurnMetadata) {
		return opts, nil
	}

	effective := (internalconfig.CodexClientMetadataConfig{}).Effective()
	if m != nil {
		if cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config); cfg != nil {
			effective = cfg.Codex.ClientMetadata.Effective()
		}
	}

	_, state, err := codexmetadata.NormalizeRequest(body, directTurnMetadata, codexmetadata.Policy{
		Mode:            effective.Mode,
		WorkspacePolicy: effective.WorkspacePolicy,
		Scope:           "codex:preauth",
	})
	if err != nil {
		return opts, codexmetadata.InvalidRequestError()
	}
	if !state.HasSessionID {
		return opts, nil
	}
	metadata := cloneSchedulerAnyMap(opts.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	if codexOnly {
		metadata[codexCanonicalSessionMetadataKey] = state.SessionID
	} else if xaiEligible && contextStringValue(metadata[cliproxyexecutor.ExecutionSessionMetadataKey]) == "" {
		// The native xAI executor already gives execution_session_id precedence
		// when deriving prompt_cache_key and x-grok-conv-id. Project Codex's
		// canonical root session into that shared contract before Enrich runs so
		// auth selection and the upstream xAI conversation use the same identity.
		metadata[cliproxyexecutor.ExecutionSessionMetadataKey] = state.SessionID
	}
	opts.Metadata = metadata
	return opts, nil
}

func hasCodexTurnMetadataCarrier(body []byte, directTurnMetadata string) bool {
	if strings.TrimSpace(directTurnMetadata) != "" {
		return true
	}
	if !bytes.Contains(body, []byte("x-codex-turn-metadata")) && !bytes.Contains(body, []byte(`\u`)) {
		return false
	}

	var envelope struct {
		ClientMetadata json.RawMessage `json:"client_metadata"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		// Let NormalizeRequest return the request-scoped validation error when an
		// apparent canonical carrier sits inside a malformed JSON request.
		return true
	}
	var clientMetadata map[string]json.RawMessage
	if err := json.Unmarshal(envelope.ClientMetadata, &clientMetadata); err != nil {
		return false
	}
	_, ok := clientMetadata["x-codex-turn-metadata"]
	return ok
}

func codexTurnMetadataHeaderValue(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for key, values := range headers {
		if !strings.EqualFold(key, "X-Codex-Turn-Metadata") {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

// codexModelFallbackRequestBudget is request-local metadata shared by the
// source and every target. Keeping it out of Config avoids a new public knob
// while ensuring abnormal-reasoning counters, hedge state, and discarded usage
// cannot be reset just because the model changes.
type codexModelFallbackRequestBudget struct {
	retryCounts map[string]int
	usage       *cliproxyexecutor.UsageAccumulator
	hedge       *retryWithoutPenaltyHedgeRequestState
}

func (m *Manager) codexModelFallbackEnabled(providers []string) bool {
	if m == nil || !codexOnlyProviders(providers) {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return cfg != nil && cfg.Codex.ModelFallback.Effective().Enabled
}

func withCodexModelFallbackRequestBudget(opts cliproxyexecutor.Options, stream bool) cliproxyexecutor.Options {
	metadata := cloneSchedulerAnyMap(opts.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[codexModelFallbackBudgetMetadataKey] = &codexModelFallbackRequestBudget{
		retryCounts: make(map[string]int),
		usage:       cliproxyexecutor.NewUsageAccumulator(coreusage.Detail{}),
		hedge:       &retryWithoutPenaltyHedgeRequestState{},
	}
	opts.Metadata = metadata
	return opts
}

func codexModelFallbackBudget(opts cliproxyexecutor.Options, stream bool) *codexModelFallbackRequestBudget {
	if budget, ok := opts.Metadata[codexModelFallbackBudgetMetadataKey].(*codexModelFallbackRequestBudget); ok && budget != nil {
		return budget
	}
	return &codexModelFallbackRequestBudget{
		retryCounts: make(map[string]int),
		usage:       cliproxyexecutor.NewUsageAccumulator(coreusage.Detail{}),
		hedge:       &retryWithoutPenaltyHedgeRequestState{},
	}
}

func codexModelFallbackTargetWave(opts cliproxyexecutor.Options) bool {
	target, _ := opts.Metadata[codexModelFallbackTargetWaveMetadataKey].(bool)
	return target
}

func markCodexModelFallbackDispatch(opts cliproxyexecutor.Options, authID string) {
	if callback, ok := opts.Metadata[codexModelFallbackDispatchMetadataKey].(func(string)); ok && callback != nil {
		callback(authID)
	}
}

func (m *Manager) executeWithoutModelFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	if m.HomeEnabled() && !m.hasLegacyHomeRuntimeAuthForRequest(ctx, opts) {
		return m.executeHome(ctx, normalized, req, opts, false)
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	budget := codexModelFallbackBudget(opts, false)
	retryWithoutPenaltyCounts := budget.retryCounts
	retryWithoutPenaltyUsage := budget.usage
	retryWithoutPenaltyHedgeState := budget.hedge
	retryWithoutPenaltyFallback := newRetryWithoutPenaltyFallbackCandidate(false)
	retryRound := 0
	for attempt := 0; ; {
		attemptOpts := withRetryWithoutPenaltyUsageMetadata(opts, retryWithoutPenaltyUsage)
		resp, errExec := m.executeMixedOnce(ctx, normalized, req, attemptOpts, maxRetryCredentials, retryRound, defaultRequestRetry)
		if errExec == nil {
			if fallbackResp, ok := retryWithoutPenaltyMaybeSelectFallbackResponse(resp, retryWithoutPenaltyFallback, retryWithoutPenaltyUsage); ok {
				return fallbackResp, nil
			}
			return resp, nil
		}
		if isRequestTerminatedError(errExec) || isRequestStopError(errExec) {
			return cliproxyexecutor.Response{}, errExec
		}
		if hasUpstreamExecutionAttempt(errExec) {
			preferredUpstreamErr = errExec
		}
		lastErr = errExec
		if detail, ok := retryWithoutPenaltyUsageDetail(errExec); ok {
			retryWithoutPenaltyUsage.Add(detail)
		}
		retryWithoutPenaltyFallback.Consider(errExec, retryWithoutPenaltyAuthIDFromError(errExec))
		if policy, ok := retryWithoutPenaltyHedgePolicyFromError(errExec); ok && policy.enabled && !m.HomeEnabled() {
			class, remaining, terminalErr, limited := retryWithoutPenaltyRemainingRetries(errExec, retryWithoutPenaltyCounts)
			if terminalErr != nil {
				if resp, ok := retryWithoutPenaltyCandidateFallbackResponse(retryWithoutPenaltyFallback.Err(errExec), retryWithoutPenaltyFallback, retryWithoutPenaltyUsage); ok {
					return resp, nil
				}
				lastErr = terminalErr
				break
			}
			if limited && remaining > 0 {
				outcome := m.executeRetryWithoutPenaltyHedged(ctx, normalized, req, opts, maxRetryCredentials, class, policy, remaining, retryWithoutPenaltyUsage, retryWithoutPenaltyHedgeState, retryWithoutPenaltyFallback)
				if outcome.disableSecondLane {
					retryWithoutPenaltyHedgeState.secondLaneDisabled = true
				}
				if outcome.attempts > 0 {
					retryWithoutPenaltyCounts[class] += outcome.attempts
					attempt += outcome.attempts
				}
				if outcome.err == nil {
					return outcome.response, nil
				}
				if hasUpstreamExecutionAttempt(outcome.err) {
					preferredUpstreamErr = outcome.err
				}
				if isRequestStopError(outcome.err) {
					return cliproxyexecutor.Response{}, outcome.err
				}
				lastErr = outcome.err
				if !outcome.usageAccounted {
					if detail, ok := retryWithoutPenaltyUsageDetail(outcome.err); ok {
						retryWithoutPenaltyUsage.Add(detail)
					}
				}
				if codexModelFallbackTargetWave(opts) && !isRetryWithoutPenaltyError(outcome.err) {
					break
				}
				policyRound := retryRound
				if !isRetryWithoutPenaltyError(outcome.err) {
					policyRound = attempt
				}
				wait, shouldRetry, terminalErr := m.shouldRetryAfterErrorWithRetryPolicy(ctx, opts, outcome.err, policyRound, normalized, retryModel, maxWait, -1, defaultRequestRetry, retryWithoutPenaltyCounts)
				if terminalErr != nil {
					if resp, ok := retryWithoutPenaltyCandidateFallbackResponse(retryWithoutPenaltyFallback.Err(outcome.err), retryWithoutPenaltyFallback, retryWithoutPenaltyUsage); ok {
						return resp, nil
					}
					lastErr = terminalErr
				}
				if !shouldRetry {
					break
				}
				if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
					return cliproxyexecutor.Response{}, errWait
				}
				if !isRetryWithoutPenaltyError(outcome.err) {
					retryRound = policyRound + 1
				}
				attempt++
				continue
			}
		}
		if codexModelFallbackTargetWave(opts) && !isRetryWithoutPenaltyError(errExec) {
			break
		}
		policyRound := retryRound
		if !isRetryWithoutPenaltyError(errExec) {
			policyRound = attempt
		}
		wait, shouldRetry, terminalErr := m.shouldRetryAfterErrorWithRetryPolicy(ctx, opts, errExec, policyRound, normalized, retryModel, maxWait, -1, defaultRequestRetry, retryWithoutPenaltyCounts)
		if terminalErr != nil {
			if resp, ok := retryWithoutPenaltyCandidateFallbackResponse(retryWithoutPenaltyFallback.Err(errExec), retryWithoutPenaltyFallback, retryWithoutPenaltyUsage); ok {
				return resp, nil
			}
			lastErr = terminalErr
		}
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
		if !isRetryWithoutPenaltyError(errExec) {
			retryRound = policyRound + 1
		}
		attempt++
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
		}
		lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		if hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, unwrapExecutionBoundaryError(lastErr), normalized) {
			if resp, ok, errCredits := m.tryAntigravityCreditsExecute(ctx, req, opts); errCredits != nil {
				return cliproxyexecutor.Response{}, errCredits
			} else if ok {
				return resp, nil
			}
		}
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func (m *Manager) executeStreamWithoutModelFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	budget := codexModelFallbackBudget(opts, true)
	retryWithoutPenaltyCounts := budget.retryCounts
	retryWithoutPenaltyUsage := budget.usage
	retryWithoutPenaltyHedgeState := budget.hedge
	retryWithoutPenaltyFallback := newRetryWithoutPenaltyFallbackCandidate(true)
	retryRound := 0
	homeRetryLimit := -1
	retryRoundPending := false
	retryRoundWaited := false
	for attempt := 0; ; {
		attemptOpts := withRetryWithoutPenaltyUsageMetadata(opts, retryWithoutPenaltyUsage)
		result, errStream := m.executeStreamMixedOnce(ctx, normalized, req, attemptOpts, maxRetryCredentials, &homeRetryLimit, retryRound, defaultRequestRetry)
		if errStream == nil {
			if fallbackResult, ok := retryWithoutPenaltyMaybeSelectFallbackStreamResult(result, retryWithoutPenaltyFallback, retryWithoutPenaltyUsage); ok {
				return fallbackResult, nil
			}
			return result, nil
		}
		if hasUpstreamExecutionAttempt(errStream) {
			preferredUpstreamErr = errStream
		}
		if m.HomeEnabled() && retryRoundPending {
			if wait, okWait := pendingHomeRetryRoundDelay(errStream, maxWait, &homeRetryLimit, pinnedAuthIDFromMetadata(opts.Metadata) == ""); okWait && m.homeRetryAllowed(retryRound-1, homeRetryLimit) {
				if retryRoundWaited {
					return nil, errStream
				}
				if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
					return nil, errWait
				}
				retryRoundWaited = true
				continue
			}
		}
		retryRoundPending = false
		retryRoundWaited = false
		if isRequestTerminatedError(errStream) || isRequestStopError(errStream) {
			return nil, errStream
		}
		lastErr = errStream
		if detail, ok := retryWithoutPenaltyUsageDetail(errStream); ok {
			retryWithoutPenaltyUsage.Add(detail)
		}
		retryWithoutPenaltyFallback.Consider(errStream, retryWithoutPenaltyAuthIDFromError(errStream))
		if policy, ok := retryWithoutPenaltyHedgePolicyFromError(errStream); ok && policy.enabled && !m.HomeEnabled() {
			class, remaining, terminalErr, limited := retryWithoutPenaltyRemainingRetries(errStream, retryWithoutPenaltyCounts)
			if terminalErr != nil {
				if result, ok := retryWithoutPenaltyCandidateFallbackStreamResult(retryWithoutPenaltyFallback.Err(errStream), retryWithoutPenaltyFallback, retryWithoutPenaltyUsage); ok {
					return result, nil
				}
				lastErr = terminalErr
				break
			}
			if limited && remaining > 0 {
				outcome := m.executeStreamRetryWithoutPenaltyHedged(ctx, normalized, req, opts, maxRetryCredentials, class, policy, remaining, retryWithoutPenaltyUsage, retryWithoutPenaltyHedgeState, retryWithoutPenaltyFallback)
				if outcome.disableSecondLane {
					retryWithoutPenaltyHedgeState.secondLaneDisabled = true
				}
				if outcome.attempts > 0 {
					retryWithoutPenaltyCounts[class] += outcome.attempts
					attempt += outcome.attempts
				}
				if outcome.err == nil {
					return outcome.stream, nil
				}
				if hasUpstreamExecutionAttempt(outcome.err) {
					preferredUpstreamErr = outcome.err
				}
				if isRequestStopError(outcome.err) {
					return nil, outcome.err
				}
				lastErr = outcome.err
				if !outcome.usageAccounted {
					if detail, ok := retryWithoutPenaltyUsageDetail(outcome.err); ok {
						retryWithoutPenaltyUsage.Add(detail)
					}
				}
				if codexModelFallbackTargetWave(opts) && !isRetryWithoutPenaltyError(outcome.err) {
					break
				}
				policyRound := retryRound
				if !isRetryWithoutPenaltyError(outcome.err) && !m.HomeEnabled() {
					policyRound = attempt
				}
				wait, shouldRetry, terminalErr := m.shouldRetryAfterErrorWithRetryPolicy(ctx, opts, outcome.err, policyRound, normalized, retryModel, maxWait, -1, defaultRequestRetry, retryWithoutPenaltyCounts)
				if terminalErr != nil {
					if result, ok := retryWithoutPenaltyCandidateFallbackStreamResult(retryWithoutPenaltyFallback.Err(outcome.err), retryWithoutPenaltyFallback, retryWithoutPenaltyUsage); ok {
						return result, nil
					}
					lastErr = terminalErr
				}
				if !shouldRetry {
					break
				}
				if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
					return nil, errWait
				}
				if !isRetryWithoutPenaltyError(outcome.err) {
					retryRound = policyRound + 1
				}
				attempt++
				continue
			}
		}
		if codexModelFallbackTargetWave(opts) && !isRetryWithoutPenaltyError(errStream) {
			break
		}
		policyRound := retryRound
		if !isRetryWithoutPenaltyError(errStream) && !m.HomeEnabled() {
			policyRound = attempt
		}
		wait, shouldRetry, terminalErr := m.shouldRetryAfterErrorWithRetryPolicy(ctx, opts, errStream, policyRound, normalized, retryModel, maxWait, homeRetryLimit, defaultRequestRetry, retryWithoutPenaltyCounts)
		if terminalErr != nil {
			if result, ok := retryWithoutPenaltyCandidateFallbackStreamResult(retryWithoutPenaltyFallback.Err(errStream), retryWithoutPenaltyFallback, retryWithoutPenaltyUsage); ok {
				return result, nil
			}
			lastErr = terminalErr
		}
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return nil, errWait
		}
		if !isRetryWithoutPenaltyError(errStream) {
			retryRound = policyRound + 1
			retryRoundPending = m.HomeEnabled()
			retryRoundWaited = false
		}
		attempt++
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
		}
		if preferredUpstreamErr != nil && (!m.HomeEnabled() || isHomeRetryRoundExhausted(lastErr)) {
			lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		}
		if hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, unwrapExecutionBoundaryError(lastErr), normalized) {
			if result, ok, errCredits := m.tryAntigravityCreditsExecuteStream(ctx, req, opts); errCredits != nil {
				return nil, errCredits
			} else if ok {
				return result, nil
			}
		}
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func excludedAuthIDsFromMetadata(meta map[string]any) map[string]struct{} {
	excluded := make(map[string]struct{})
	if len(meta) == 0 {
		return excluded
	}
	add := func(authID string) {
		authID = strings.TrimSpace(authID)
		if authID != "" {
			excluded[authID] = struct{}{}
		}
	}
	raw, ok := meta[cliproxyexecutor.ExcludeAuthIDsMetadataKey]
	if !ok || raw == nil {
		return excluded
	}
	switch value := raw.(type) {
	case string:
		for _, part := range strings.Split(value, ",") {
			add(part)
		}
	case []byte:
		for _, part := range strings.Split(string(value), ",") {
			add(part)
		}
	case []string:
		for _, authID := range value {
			add(authID)
		}
	case []any:
		for _, item := range value {
			switch authID := item.(type) {
			case string:
				add(authID)
			case []byte:
				add(string(authID))
			}
		}
	}
	return excluded
}

func availabilityDeadlinesExpired(nextRetryAfter, nextRecoverAt, now time.Time) bool {
	hasDeadline := false
	for _, deadline := range []time.Time{nextRetryAfter, nextRecoverAt} {
		if deadline.IsZero() {
			continue
		}
		hasDeadline = true
		if deadline.After(now) {
			return false
		}
	}
	return hasDeadline
}

func availabilityStateIsNonResettable(quota QuotaState, lastErr *Error, statusMessage string) bool {
	if quotaReasonIsNonResettable(quota.Reason) || quotaReasonIsNonResettable(statusMessage) {
		return true
	}
	if lastErr == nil {
		return false
	}
	return isCloudflareChallengeErrorMessage(strings.TrimSpace(lastErr.Code + " " + lastErr.Message))
}

func authAvailabilityExpired(auth *Auth, now time.Time) bool {
	if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
		return false
	}
	if availabilityStateIsNonResettable(auth.Quota, auth.LastError, auth.StatusMessage) {
		return false
	}
	if !availabilityDeadlinesExpired(auth.NextRetryAfter, auth.Quota.NextRecoverAt, now) {
		return false
	}
	return auth.Status != StatusActive || auth.Unavailable || auth.StatusMessage != "" || auth.LastError != nil || !quotaStateIsEmpty(auth.Quota) || !auth.NextRetryAfter.IsZero()
}

func authAvailabilityStatePresent(auth *Auth) bool {
	if auth == nil {
		return false
	}
	return auth.Status != StatusActive || auth.Unavailable || auth.StatusMessage != "" || auth.LastError != nil || !quotaStateIsEmpty(auth.Quota) || !auth.NextRetryAfter.IsZero()
}

func authAvailabilityMirrorsExpiredModel(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	for _, state := range auth.ModelStates {
		if modelAvailabilityExpired(state, now) && authAvailabilityMatchesModelState(auth, state) {
			return true
		}
	}
	return false
}

func authAvailabilityMatchesModelState(auth *Auth, state *ModelState) bool {
	if auth == nil || state == nil || auth.Status != state.Status {
		return false
	}
	authMessage := strings.TrimSpace(auth.StatusMessage)
	stateMessage := strings.TrimSpace(state.StatusMessage)
	if authMessage != stateMessage || !availabilityErrorsEqual(auth.LastError, state.LastError) {
		return false
	}
	if authMessage == "" && auth.LastError == nil {
		return false
	}
	if auth.Unavailable && !state.Unavailable {
		return false
	}
	if auth.Unavailable && !auth.NextRetryAfter.Equal(state.NextRetryAfter) {
		return false
	}
	if !auth.Unavailable && !auth.NextRetryAfter.IsZero() && !auth.NextRetryAfter.Equal(state.NextRetryAfter) {
		return false
	}
	return availabilityQuotaMatchesModel(auth.Quota, state.Quota)
}

func availabilityErrorsEqual(left, right *Error) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Code == right.Code && left.Message == right.Message && left.Retryable == right.Retryable && left.HTTPStatus == right.HTTPStatus
}

func availabilityQuotaMatchesModel(authQuota, modelQuota QuotaState) bool {
	if quotaStateIsEmpty(authQuota) || quotaStateIsEmpty(modelQuota) {
		return quotaStateIsEmpty(authQuota) && quotaStateIsEmpty(modelQuota)
	}
	if authQuota.Exceeded != modelQuota.Exceeded || authQuota.BackoffLevel != modelQuota.BackoffLevel || !authQuota.NextRecoverAt.Equal(modelQuota.NextRecoverAt) {
		return false
	}
	authReason := strings.ToLower(strings.TrimSpace(authQuota.Reason))
	modelReason := strings.ToLower(strings.TrimSpace(modelQuota.Reason))
	return authReason == modelReason || (authReason == "quota" && quotaMessageIsResettable(modelReason))
}

func modelAvailabilityExpired(state *ModelState, now time.Time) bool {
	if state == nil || state.Status == StatusDisabled || modelStateIsClean(state) {
		return false
	}
	if availabilityStateIsNonResettable(state.Quota, state.LastError, state.StatusMessage) {
		return false
	}
	return availabilityDeadlinesExpired(state.NextRetryAfter, state.Quota.NextRecoverAt, now)
}

func allModelStatesClean(auth *Auth) bool {
	if auth == nil {
		return true
	}
	for _, state := range auth.ModelStates {
		if !modelStateIsClean(state) {
			return false
		}
	}
	return true
}

// recomputeAggregatedAvailability updates only auth-level fields. Unlike
// updateAggregatedAvailability, it never mutates model state while preserving
// non-resettable or zero-deadline warnings.
func recomputeAggregatedAvailability(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	if len(auth.ModelStates) == 0 {
		clearAggregatedAvailability(auth)
		return
	}
	allUnavailable := true
	hasState := false
	earliestRetry := time.Time{}
	quotaExceeded := false
	quotaReason := ""
	quotaRecover := time.Time{}
	maxBackoffLevel := 0
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		hasState = true
		stateUnavailable := state.Status == StatusDisabled || (state.Unavailable && !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now))
		if !stateUnavailable {
			allUnavailable = false
		} else if !state.NextRetryAfter.IsZero() && (earliestRetry.IsZero() || state.NextRetryAfter.Before(earliestRetry)) {
			earliestRetry = state.NextRetryAfter
		}
		if state.Quota.Exceeded {
			quotaExceeded = true
			if quotaReason == "" && strings.TrimSpace(state.Quota.Reason) != "" {
				quotaReason = state.Quota.Reason
			}
			if quotaRecover.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.Before(quotaRecover)) {
				quotaRecover = state.Quota.NextRecoverAt
			}
			if state.Quota.BackoffLevel > maxBackoffLevel {
				maxBackoffLevel = state.Quota.BackoffLevel
			}
		}
	}
	if !hasState {
		clearAggregatedAvailability(auth)
		return
	}
	auth.Unavailable = allUnavailable
	if allUnavailable {
		auth.NextRetryAfter = earliestRetry
	} else {
		auth.NextRetryAfter = time.Time{}
	}
	if quotaExceeded {
		if quotaReason == "" {
			quotaReason = "quota"
		}
		auth.Quota = QuotaState{Exceeded: true, Reason: quotaReason, NextRecoverAt: quotaRecover, BackoffLevel: maxBackoffLevel}
	} else {
		auth.Quota = QuotaState{}
	}
}

func reconcileAuthErrorFromModelStates(auth *Auth, now time.Time) {
	if auth == nil || allModelStatesClean(auth) {
		return
	}
	keys := make([]string, 0, len(auth.ModelStates))
	for model := range auth.ModelStates {
		keys = append(keys, model)
	}
	sort.Strings(keys)
	var representative *ModelState
	for _, model := range keys {
		state := auth.ModelStates[model]
		if modelStateIsClean(state) {
			continue
		}
		if representative == nil {
			representative = state
		}
		if state != nil && (state.LastError != nil || strings.TrimSpace(state.StatusMessage) != "") {
			representative = state
			break
		}
	}
	auth.Status = StatusError
	auth.StatusMessage = ""
	auth.LastError = nil
	if representative != nil {
		auth.StatusMessage = representative.StatusMessage
		auth.LastError = cloneError(representative.LastError)
		if auth.StatusMessage == "" && auth.LastError != nil {
			auth.StatusMessage = auth.LastError.Message
		}
	}
	auth.UpdatedAt = now
}

func clearQuotaStateForAuth(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	changed := false
	authQuotaState := quotaStateIsResettable(auth.Quota) || errorIsResettableQuota(auth.LastError) || quotaMessageIsResettable(auth.StatusMessage)
	if authQuotaState {
		if auth.Unavailable || !auth.NextRetryAfter.IsZero() || !quotaStateIsEmpty(auth.Quota) {
			auth.Unavailable = false
			auth.NextRetryAfter = time.Time{}
			auth.Quota = QuotaState{}
			changed = true
		}
		if errorIsResettableQuota(auth.LastError) {
			auth.LastError = nil
			changed = true
		}
		if quotaMessageIsResettable(auth.StatusMessage) {
			auth.StatusMessage = ""
			changed = true
		}
	}
	for _, state := range auth.ModelStates {
		if !modelStateHasResettableQuota(state) {
			continue
		}
		if !modelStateIsClean(state) {
			resetModelState(state, now)
			changed = true
		}
	}
	if !changed {
		return false
	}
	if len(auth.ModelStates) > 0 {
		updateAggregatedAvailability(auth, now)
	} else {
		clearAggregatedAvailability(auth)
	}
	if !hasModelError(auth, now) {
		auth.LastError = nil
		auth.StatusMessage = ""
		if !auth.Disabled && auth.Status != StatusDisabled {
			auth.Status = StatusActive
		}
	} else {
		if errorIsResettableQuota(auth.LastError) {
			auth.LastError = nil
		}
		if quotaMessageIsResettable(auth.StatusMessage) {
			auth.StatusMessage = ""
		}
	}
	auth.UpdatedAt = now
	return true
}

func quotaStateIsResettable(quota QuotaState) bool {
	reason := strings.ToLower(strings.TrimSpace(quota.Reason))
	if quotaReasonIsNonResettable(reason) {
		return false
	}
	if quota.Exceeded {
		return reason == "" || quotaMessageIsResettable(reason)
	}
	return quotaMessageIsResettable(reason)
}

func quotaStateIsEmpty(quota QuotaState) bool {
	return !quota.Exceeded && quota.Reason == "" && quota.NextRecoverAt.IsZero() && quota.BackoffLevel == 0
}

func modelStateHasResettableQuota(state *ModelState) bool {
	if state == nil {
		return false
	}
	if quotaStateIsResettable(state.Quota) {
		return true
	}
	if errorIsResettableQuota(state.LastError) {
		return true
	}
	return quotaMessageIsResettable(state.StatusMessage)
}

func errorIsResettableQuota(err *Error) bool {
	if err == nil {
		return false
	}
	if quotaMessageIsResettable(err.Code) || quotaMessageIsResettable(err.Message) {
		return true
	}
	if err.StatusCode() != http.StatusTooManyRequests {
		return false
	}
	combined := strings.TrimSpace(err.Code + " " + err.Message)
	return quotaMessageIsResettable(combined)
}

func quotaMessageIsResettable(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" || quotaReasonIsNonResettable(lower) {
		return false
	}
	return strings.Contains(lower, "quota") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "rate limited") ||
		strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "rate-limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "too_many_requests")
}

func quotaReasonIsNonResettable(reason string) bool {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return false
	}
	return strings.Contains(reason, "cloudflare") || strings.Contains(reason, "challenge")
}

func countTokensResultErrorFromError(err error, requestedModel string) *Error {
	resultErr := resultErrorFromError(err)
	if isCountTokensEndpointNotFoundError(err, requestedModel) {
		resultErr.Code = requestScopedErrorCode
	}
	return resultErr
}

func isTransientEOFError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	message = strings.TrimSpace(strings.TrimSuffix(message, " (status=0)"))
	switch message {
	case "eof", "unexpected eof":
		return true
	default:
		return strings.HasSuffix(message, ": eof") || strings.HasSuffix(message, ": unexpected eof")
	}
}

func isXaiBadCredentialsMessage(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "bad-credentials") || strings.Contains(lower, "could not be validated")
}

func isXaiBadCredentialsError(provider string, err error) bool {
	if err == nil || !strings.EqualFold(strings.TrimSpace(provider), "xai") {
		return false
	}
	if statusCodeFromError(err) != http.StatusForbidden {
		return false
	}
	return isXaiBadCredentialsMessage(err.Error())
}

func isXaiBadCredentialsResultError(provider string, err *Error) bool {
	if err == nil || !strings.EqualFold(strings.TrimSpace(provider), "xai") {
		return false
	}
	if statusCodeFromResult(err) != http.StatusForbidden {
		return false
	}
	return isXaiBadCredentialsMessage(err.Code) || isXaiBadCredentialsMessage(err.Message)
}

func isCodexTransientRateLimitResultError(provider string, err *Error) bool {
	if err == nil || !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(err.Code)) {
	case codexTransientRateLimitClass, "rate_limit_error", "rate_limit_exceeded":
		return true
	default:
		return false
	}
}

// isRequestScopedModelNotFoundMessage identifies the OpenAI-style 404 emitted
// when request/model routing or Codex client identity is rejected. This shape
// is not evidence of unhealthy credentials, and penalizing each auth can
// otherwise suspend an entire model pool for hours.
func isRequestScopedModelNotFoundMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if !strings.Contains(lower, "model not found") || !strings.Contains(lower, "invalid_request_error") {
		return false
	}

	var payload struct {
		Error struct {
			Type  string `json:"type"`
			Param any    `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(message), &payload); err == nil {
		return strings.EqualFold(strings.TrimSpace(payload.Error.Type), "invalid_request_error") &&
			strings.EqualFold(strings.TrimSpace(fmt.Sprint(payload.Error.Param)), "model")
	}

	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(lower)
	return strings.Contains(compact, `"param":"model"`) || strings.Contains(compact, "param=model")
}

// closeExecutionSession releases an execution session while optionally
// preserving the already-admitted target auth/model attempt that triggered a
// context-reset fallback. The source session state is still cleared.
func (m *Manager) closeExecutionSession(sessionID, preserveAuthID, preserveRouteModel string) {
	m.closeExecutionSessionScoped(sessionID, "", "", preserveAuthID, preserveRouteModel)
}

func (m *Manager) closeExecutionSessionScoped(sessionID, callerScope, workspaceIdentity, preserveAuthID, preserveRouteModel string) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}
	callerScope = strings.TrimSpace(callerScope)
	workspaceIdentity = strings.TrimSpace(workspaceIdentity)

	m.mu.Lock()
	var selections []*HomeDispatchSelection
	if sessionID == CloseAllExecutionSessionsID {
		if m.codexRateLimitContinuity != nil {
			m.advanceCodexRateLimitContinuityLifecycleLocked()
		}
		m.clearHomeRuntimeAuthsLocked()
		if m.codexRateLimitContinuity != nil {
			m.codexRateLimitContinuity.clear()
		}
		selections = m.takeAllHomeSessionSelectionsLocked()
		m.clearHomeSessionLocks()
	} else {
		m.clearHomeRuntimeAuthsForSessionLocked(sessionID)
		if m.codexRateLimitContinuity != nil {
			var preserve *codexRateLimitContinuityKey
			if auth := m.auths[strings.TrimSpace(preserveAuthID)]; auth != nil {
				if modelKey := m.selectionModelKeyForAuth(auth, preserveRouteModel); modelKey != "" {
					key := codexRateLimitContinuityKey{authID: auth.ID, model: modelKey}
					preserve = &key
				}
			}
			m.codexRateLimitContinuity.removeSessionPreservingKey("execution:"+sessionID, preserve)
		}
		selections = m.takeHomeSessionSelectionsLocked(sessionID)
		m.homeSessionLocks.Delete(sessionID)
	}
	executors := make([]ProviderExecutor, 0, len(m.executors))
	for _, exec := range m.executors {
		executors = append(executors, exec)
	}
	m.mu.Unlock()

	for _, selection := range selections {
		selection.End("session_closed")
	}
	for i := range executors {
		if closer, ok := executors[i].(ScopedExecutionSessionCloser); ok && closer != nil {
			closer.CloseExecutionSessionScoped(sessionID, callerScope, workspaceIdentity)
			continue
		}
		if closer, ok := executors[i].(ExecutionSessionCloser); ok && closer != nil {
			closer.CloseExecutionSession(sessionID)
		}
	}
}

// SelectAuthForRequest selects one credential and starts any request-scoped
// continuity tracking required by the selected auth. Callers must pass the
// returned context to ConfirmSelectedAuthDispatch immediately before the
// upstream dispatch, then to MarkResult exactly once when an upstream outcome
// exists. If no upstream outcome exists, callers must abandon the attempt; it
// is safe to defer AbandonSelectedAuthRequest because MarkResult consumes an
// observed attempt first.
func (m *Manager) SelectAuthForRequest(ctx context.Context, provider, model string, opts cliproxyexecutor.Options) (*Auth, context.Context, error) {
	return m.selectAuthForRequest(ctx, provider, model, "", opts)
}

// SelectAuthForRequestByKind is SelectAuthForRequest with an additional auth
// kind constraint. It preserves request-scoped continuity while skipping
// credentials that cannot serve the endpoint, such as API keys for Codex Alpha Search.
func (m *Manager) SelectAuthForRequestByKind(ctx context.Context, provider, model, requiredKind string, opts cliproxyexecutor.Options) (*Auth, context.Context, error) {
	requiredKind = normalizeAuthKind(requiredKind)
	if requiredKind == "" {
		if ctx == nil {
			ctx = context.Background()
		}
		return nil, ctx, &Error{Code: "invalid_auth_kind", Message: "required auth kind is invalid", HTTPStatus: http.StatusBadRequest}
	}
	return m.selectAuthForRequest(ctx, provider, model, requiredKind, opts)
}

// SelectAuthForRequestByKindWithUncataloguedModel preserves model-scoped auth
// availability and request lifecycle tracking for an endpoint-specific model
// that is intentionally absent from the general proxy capability registry.
func (m *Manager) SelectAuthForRequestByKindWithUncataloguedModel(ctx context.Context, provider, model, requiredKind string, opts cliproxyexecutor.Options) (*Auth, context.Context, error) {
	requiredKind = normalizeAuthKind(requiredKind)
	if requiredKind == "" {
		if ctx == nil {
			ctx = context.Background()
		}
		return nil, ctx, &Error{Code: "invalid_auth_kind", Message: "required auth kind is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.selectAuthForRequest(withUncataloguedEndpointModel(ctx), provider, model, requiredKind, opts)
}

func (m *Manager) selectAuthForRequest(ctx context.Context, provider, model, requiredKind string, opts cliproxyexecutor.Options) (*Auth, context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if requiredKind != "" {
		ctx = withRequiredAuthKind(ctx, requiredKind)
	}
	ctx = m.withCodexRateLimitContinuityLifecycle(ctx)
	homeMode := m.HomeEnabled()
	homeAuthCount := homeAuthCountFromMetadata(opts.Metadata)
	tried := make(map[string]struct{})
	for {
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeAuthCount(opts, homeAuthCount)
		}
		selected, _, errPick := m.pickNext(ctx, provider, model, pickOpts, tried)
		if errPick != nil {
			return nil, ctx, errPick
		}
		if selected == nil {
			return nil, ctx, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
		}
		authID := strings.TrimSpace(selected.ID)
		if authID == "" {
			return nil, ctx, &Error{Code: "auth_not_found", Message: "selected auth has no ID"}
		}
		if requiredKind != "" && selected.AuthKind() != requiredKind {
			if _, alreadyTried := tried[authID]; alreadyTried {
				return nil, ctx, &Error{Code: "auth_not_found", Message: "selector repeatedly returned an ineligible auth"}
			}
			tried[authID] = struct{}{}
			if homeMode {
				homeAuthCount++
			}
			continue
		}
		attemptCtx, allowed := m.beginCodexRateLimitContinuityAttempt(ctx, selected, provider, model, opts)
		if allowed {
			return selected, contextWithAuthGeneration(attemptCtx, selected), nil
		}
		tried[authID] = struct{}{}
		if homeMode {
			homeAuthCount++
		}
	}
}

// ConfirmSelectedAuthDispatch rechecks request-scoped continuity immediately
// before an upstream dispatch. It rejects attempts invalidated by a confirmed
// cooldown or lifecycle reset and releases their in-flight continuity state.
func (m *Manager) ConfirmSelectedAuthDispatch(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		m.abandonCodexRateLimitContinuityAttempt(ctx)
		return err
	}
	if allowed, confirmed := m.codexRateLimitContinuityDispatchDisposition(ctx); !allowed {
		m.abandonCodexRateLimitContinuityAttempt(ctx)
		return codexRateLimitObservationPendingError{confirmed: confirmed}
	}
	return nil
}

// AbandonSelectedAuthRequest releases request-scoped continuity state when a
// selected credential never produces an upstream outcome for MarkResult.
func (m *Manager) AbandonSelectedAuthRequest(ctx context.Context) {
	m.abandonCodexRateLimitContinuityAttempt(ctx)
}

func (m *Manager) hasLegacyHomeRuntimeAuthForRequest(ctx context.Context, opts cliproxyexecutor.Options) bool {
	if m == nil || !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return false
	}
	if m.HomeDispatchBundle() != nil {
		return false
	}
	sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	if sessionID == "" || pinnedAuthID == "" {
		return false
	}
	_, _, _, ok := m.homeRuntimeAuthByID(sessionID, pinnedAuthID)
	return ok
}
