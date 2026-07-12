package auth

import (
	"context"
	"errors"
	"strings"
	"sync"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type modelFallbackReasonError interface {
	ModelFallbackReason() string
}

type modelFallbackBlockedError interface {
	ModelFallbackBlocked() bool
}

type codexReasoningReplayScopeCarrier interface {
	CodexReasoningReplayScope() (modelName, sessionKey string)
}

// modelFallbackZeroDispatchError marks a scheduler-local target rejection. It
// is intentionally additive so continuity observation in the stacked PR can
// participate without making its internal error type part of this package.
type modelFallbackZeroDispatchError interface {
	ModelFallbackZeroDispatch() bool
}

func modelFallbackReasonFromError(err error) string {
	if err == nil {
		return ""
	}
	var classified modelFallbackReasonError
	if !errors.As(err, &classified) || classified == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(classified.ModelFallbackReason()))
}

func isModelFallbackBlockedError(err error) bool {
	if err == nil {
		return false
	}
	var blocked modelFallbackBlockedError
	return errors.As(err, &blocked) && blocked != nil && blocked.ModelFallbackBlocked()
}

func codexOnlyProviders(providers []string) bool {
	found := false
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		if provider != "codex" {
			return false
		}
		found = true
	}
	return found
}

func codexModelFallbackRequestEligible(providers []string, opts cliproxyexecutor.Options) bool {
	if !codexOnlyProviders(providers) || strings.TrimSpace(opts.Alt) != "" {
		return false
	}
	if len(opts.Metadata) == 0 {
		return true
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestPathMetadataKey]
	if !ok || raw == nil {
		return true
	}
	path := ""
	switch value := raw.(type) {
	case string:
		path = value
	case []byte:
		path = string(value)
	}
	path = strings.ToLower(strings.TrimSpace(path))
	return !strings.Contains(path, "/images/") && !strings.Contains(path, "/videos/")
}

func (m *Manager) codexModelFallbackPlan(providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, err error) (internalconfig.EffectiveCodexModelFallbackConfig, string, []string, bool) {
	if m == nil || !codexModelFallbackRequestEligible(providers, opts) {
		return internalconfig.EffectiveCodexModelFallbackConfig{}, "", nil, false
	}
	reason := modelFallbackReasonFromError(err)
	if reason == "" {
		return internalconfig.EffectiveCodexModelFallbackConfig{}, "", nil, false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return internalconfig.EffectiveCodexModelFallbackConfig{}, "", nil, false
	}
	effective := cfg.Codex.ModelFallback.Effective()
	sourceModel := strings.TrimSpace(thinking.ParseSuffix(req.Model).ModelName)
	if sourceModel == "" {
		sourceModel = strings.TrimSpace(req.Model)
	}
	targets := effective.TargetsFor(sourceModel, reason)
	if len(targets) == 0 {
		return effective, reason, nil, false
	}
	seen := map[string]struct{}{strings.ToLower(sourceModel): {}}
	filtered := make([]string, 0, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		base := strings.TrimSpace(thinking.ParseSuffix(target).ModelName)
		if base == "" {
			base = target
		}
		key := strings.ToLower(base)
		if target == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, target)
	}
	if len(filtered) == 0 {
		return effective, reason, nil, false
	}
	return effective, reason, filtered, true
}

func withCodexModelFallbackMetadata(opts cliproxyexecutor.Options, sourceModel, targetModel, reasoningContinuity string) cliproxyexecutor.Options {
	metadata := cloneSchedulerAnyMap(opts.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 3)
	}
	metadata[cliproxyexecutor.AuthSelectionModelMetadataKey] = strings.TrimSpace(targetModel)
	metadata[cliproxyexecutor.CodexModelFallbackSourceModelMetadataKey] = strings.TrimSpace(sourceModel)
	metadata[cliproxyexecutor.CodexModelFallbackReasoningContinuityMetadataKey] = strings.TrimSpace(reasoningContinuity)
	metadata[codexModelFallbackTargetWaveMetadataKey] = true
	// A websocket auth pin belongs to the source upstream execution session.
	// Carrying it into a different model silently makes target selection either
	// impossible or, worse, sends a reset transcript to the source auth. It is
	// safe to release only when the handler explicitly attested a complete
	// mediated transcript for context-reset replay.
	if reasoningContinuity == internalconfig.CodexModelFallbackReasoningContinuityContextReset && codexModelFallbackContextResetAllowed(metadata) {
		delete(metadata, cliproxyexecutor.PinnedAuthMetadataKey)
		delete(metadata, cliproxyexecutor.SelectedAuthMetadataKey)
	}
	opts.Metadata = metadata
	return opts
}

func codexModelFallbackContextResetAllowed(metadata map[string]any) bool {
	allowed, _ := metadata[cliproxyexecutor.CodexModelFallbackContextResetReplayMetadataKey].(bool)
	return allowed
}

func codexModelFallbackMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return ""
	}
}

func codexModelFallbackHasStatefulContinuity(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	if strings.TrimSpace(gjson.GetBytes(req.Payload, "previous_response_id").String()) != "" {
		return true
	}
	// Translators represent cross-provider thinking differently. Inspect only
	// protocol fields, never raw user text: prompts are allowed to discuss
	// "thinking" or contain JSON examples without becoming session state.
	if codexPayloadHasStatefulReasoning(req.Payload) {
		return true
	}
	if len(opts.Metadata) == 0 {
		return false
	}
	return codexModelFallbackMetadataString(opts.Metadata, cliproxyexecutor.PinnedAuthMetadataKey) != "" ||
		codexModelFallbackMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey) != ""
}

func codexPayloadHasStatefulReasoning(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "reasoning.encrypted_content").String()) != "" {
		return true
	}
	input := gjson.GetBytes(payload, "input")
	if input.IsArray() {
		for _, item := range input.Array() {
			if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "reasoning") {
				return true
			}
		}
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return false
	}
	for _, message := range messages.Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, block := range content.Array() {
			if strings.EqualFold(strings.TrimSpace(block.Get("type").String()), "thinking") &&
				(strings.TrimSpace(block.Get("signature").String()) != "" || strings.TrimSpace(block.Get("thinking").String()) != "") {
				return true
			}
		}
	}
	return false
}

func codexModelFallbackMayAttemptContextReset(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, continuity string) bool {
	if continuity != internalconfig.CodexModelFallbackReasoningContinuityContextReset {
		return !codexModelFallbackHasStatefulContinuity(req, opts)
	}
	// Stateless requests do not need the websocket-only attestation. Any state
	// that would require a reset does: it prevents a bare incremental request
	// from being mistaken for a reconstructable transcript.
	return !codexModelFallbackHasStatefulContinuity(req, opts) || codexModelFallbackContextResetAllowed(opts.Metadata)
}

// codexModelFallbackSourceReplayPreflight mirrors the source-key priority used
// by the Codex executor. It runs before target auth selection so an existing
// source replay entry (or an unavailable required lookup) cannot create an
// observable target selection for an unsafe continuation.
func codexModelFallbackSourceReplayPreflight(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, continuity string, sourceErr error) bool {
	if continuity == internalconfig.CodexModelFallbackReasoningContinuityContextReset && codexModelFallbackContextResetAllowed(opts.Metadata) {
		return true
	}
	sourceModel := strings.TrimSpace(thinking.ParseSuffix(req.Model).ModelName)
	if sourceModel == "" {
		sourceModel = strings.TrimSpace(req.Model)
	}
	if sourceModel == "" {
		return true
	}
	key := ""
	var scope codexReasoningReplayScopeCarrier
	if errors.As(sourceErr, &scope) && scope != nil {
		if scopedModel, scopedKey := scope.CodexReasoningReplayScope(); strings.TrimSpace(scopedKey) != "" {
			key = strings.TrimSpace(scopedKey)
			if strings.TrimSpace(scopedModel) != "" {
				sourceModel = strings.TrimSpace(scopedModel)
			}
		}
	}
	if key == "" {
		key = internalcache.ResolveCodexReasoningReplaySessionKey(internalcache.CodexReasoningReplaySessionInput{
			Context:                 ctx,
			SourceFormat:            opts.SourceFormat.String(),
			RequestPayload:          req.Payload,
			Headers:                 opts.Headers,
			OptionExecutionSession:  codexModelFallbackMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey),
			RequestExecutionSession: codexModelFallbackMetadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey),
		})
	}
	if key == "" {
		return true
	}
	items, found, err := internalcache.GetCodexReasoningReplayItemsRequired(ctx, sourceModel, key)
	return err == nil && !found && len(items) == 0
}

func isCodexModelFallbackZeroDispatchError(err error) bool {
	var marked modelFallbackZeroDispatchError
	if errors.As(err, &marked) && marked != nil && marked.ModelFallbackZeroDispatch() {
		return true
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		switch strings.ToLower(strings.TrimSpace(authErr.Code)) {
		case "auth_not_found", "model_not_supported", "model_cooldown", "continuity_observation_pending":
			return true
		}
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) && cooldownErr != nil {
		return true
	}
	return false
}

func codexModelFallbackSelectedAuthCollector(opts cliproxyexecutor.Options, onDispatch func()) (cliproxyexecutor.Options, func()) {
	if len(opts.Metadata) == 0 {
		return opts, func() {}
	}
	var mu sync.Mutex
	var selected string
	dispatched := make(map[string]struct{})
	var dispatchOnce sync.Once
	metadata := opts.Metadata
	externalCallback, _ := metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		mu.Lock()
		defer mu.Unlock()
		selected = strings.TrimSpace(authID)
	}
	metadata[codexModelFallbackDispatchMetadataKey] = func(authID string) {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			return
		}
		dispatchOnce.Do(func() {
			if onDispatch != nil {
				onDispatch()
			}
		})
		mu.Lock()
		defer mu.Unlock()
		dispatched[authID] = struct{}{}
	}
	return opts, func() {
		mu.Lock()
		defer mu.Unlock()
		metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = externalCallback
		delete(metadata, codexModelFallbackDispatchMetadataKey)
		// Hedge lanes publish their final winner through the selected-auth
		// callback after all internal lane choices. Only expose that winner when
		// the same auth reached the real executor dispatch boundary.
		if _, ok := dispatched[selected]; ok && selected != "" {
			metadata[cliproxyexecutor.SelectedAuthMetadataKey] = selected
			if externalCallback != nil {
				externalCallback(selected)
			}
		}
	}
}

func (m *Manager) executeCodexModelFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, initialErr error) (cliproxyexecutor.Response, error) {
	effective, reason, targets, ok := m.codexModelFallbackPlan(providers, req, opts, initialErr)
	if !ok {
		return cliproxyexecutor.Response{}, initialErr
	}

	sourceModel := req.Model
	if !codexModelFallbackMayAttemptContextReset(req, opts, effective.ReasoningContinuity) {
		return cliproxyexecutor.Response{}, initialErr
	}
	if !codexModelFallbackSourceReplayPreflight(ctx, req, opts, effective.ReasoningContinuity, initialErr) {
		return cliproxyexecutor.Response{}, initialErr
	}
	lastErr := initialErr
	var publishLastDispatched func()
	var closeSourceOnce sync.Once
	for _, target := range targets {
		if errCtx := ctx.Err(); errCtx != nil {
			return cliproxyexecutor.Response{}, errCtx
		}
		entry := logEntryWithRequestID(ctx)
		entry.WithFields(log.Fields{
			"source_model": sourceModel,
			"target_model": target,
			"reason":       reason,
		}).Info("codex model fallback: attempting configured target")

		fallbackReq := req
		fallbackReq.Model = target
		fallbackOpts := withCodexModelFallbackMetadata(opts, sourceModel, target, effective.ReasoningContinuity)
		fallbackOpts, publishSelected := codexModelFallbackSelectedAuthCollector(fallbackOpts, func() {
			closeSourceOnce.Do(func() {
				if effective.ReasoningContinuity == internalconfig.CodexModelFallbackReasoningContinuityContextReset && codexModelFallbackContextResetAllowed(fallbackOpts.Metadata) {
					if sessionID := codexModelFallbackMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); sessionID != "" {
						m.CloseExecutionSession(sessionID)
					}
				}
			})
		})
		resp, errFallback := m.executeWithoutModelFallback(ctx, providers, fallbackReq, fallbackOpts)
		if errFallback == nil {
			publishSelected()
			return resp, nil
		}
		if isModelFallbackBlockedError(errFallback) {
			entry.WithFields(log.Fields{
				"source_model": sourceModel,
				"target_model": target,
			}).Info("codex model fallback: blocked by reasoning continuity policy")
			return cliproxyexecutor.Response{}, initialErr
		}
		if isCodexModelFallbackZeroDispatchError(errFallback) {
			continue
		}
		publishLastDispatched = publishSelected
		lastErr = errFallback
		if modelFallbackReasonFromError(errFallback) == "" {
			publishLastDispatched()
			publishLastDispatched = nil
			break
		}
	}
	if publishLastDispatched != nil {
		publishLastDispatched()
	}
	return cliproxyexecutor.Response{}, lastErr
}

func (m *Manager) executeStreamCodexModelFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, initialErr error) (*cliproxyexecutor.StreamResult, error) {
	effective, reason, targets, ok := m.codexModelFallbackPlan(providers, req, opts, initialErr)
	if !ok {
		return nil, initialErr
	}

	sourceModel := req.Model
	if !codexModelFallbackMayAttemptContextReset(req, opts, effective.ReasoningContinuity) {
		return nil, initialErr
	}
	if !codexModelFallbackSourceReplayPreflight(ctx, req, opts, effective.ReasoningContinuity, initialErr) {
		return nil, initialErr
	}
	lastErr := initialErr
	var publishLastDispatched func()
	var closeSourceOnce sync.Once
	for _, target := range targets {
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}
		entry := logEntryWithRequestID(ctx)
		entry.WithFields(log.Fields{
			"source_model": sourceModel,
			"target_model": target,
			"reason":       reason,
		}).Info("codex model fallback: attempting configured streaming target")

		fallbackReq := req
		fallbackReq.Model = target
		fallbackOpts := withCodexModelFallbackMetadata(opts, sourceModel, target, effective.ReasoningContinuity)
		fallbackOpts, publishSelected := codexModelFallbackSelectedAuthCollector(fallbackOpts, func() {
			closeSourceOnce.Do(func() {
				if effective.ReasoningContinuity == internalconfig.CodexModelFallbackReasoningContinuityContextReset && codexModelFallbackContextResetAllowed(fallbackOpts.Metadata) {
					if sessionID := codexModelFallbackMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); sessionID != "" {
						m.CloseExecutionSession(sessionID)
					}
				}
			})
		})
		result, errFallback := m.executeStreamWithoutModelFallback(ctx, providers, fallbackReq, fallbackOpts)
		if errFallback == nil {
			publishSelected()
			return result, nil
		}
		if isModelFallbackBlockedError(errFallback) {
			entry.WithFields(log.Fields{
				"source_model": sourceModel,
				"target_model": target,
			}).Info("codex model fallback: blocked by reasoning continuity policy")
			return nil, initialErr
		}
		if isCodexModelFallbackZeroDispatchError(errFallback) {
			continue
		}
		publishLastDispatched = publishSelected
		lastErr = errFallback
		if modelFallbackReasonFromError(errFallback) == "" {
			publishLastDispatched()
			publishLastDispatched = nil
			break
		}
	}
	if publishLastDispatched != nil {
		publishLastDispatched()
	}
	return nil, lastErr
}
