package auth

import (
	"context"
	"errors"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type modelFallbackReasonError interface {
	ModelFallbackReason() string
}

type modelFallbackBlockedError interface {
	ModelFallbackBlocked() bool
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
	opts.Metadata = metadata
	return opts
}

func (m *Manager) executeCodexModelFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, initialErr error) (cliproxyexecutor.Response, error) {
	effective, reason, targets, ok := m.codexModelFallbackPlan(providers, req, opts, initialErr)
	if !ok {
		return cliproxyexecutor.Response{}, initialErr
	}

	sourceModel := req.Model
	lastErr := initialErr
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
		resp, errFallback := m.executeWithoutModelFallback(ctx, providers, fallbackReq, fallbackOpts)
		if errFallback == nil {
			return resp, nil
		}
		if isModelFallbackBlockedError(errFallback) {
			entry.WithFields(log.Fields{
				"source_model": sourceModel,
				"target_model": target,
			}).Info("codex model fallback: blocked by reasoning continuity policy")
			return cliproxyexecutor.Response{}, initialErr
		}
		lastErr = errFallback
		if modelFallbackReasonFromError(errFallback) == "" {
			break
		}
	}
	return cliproxyexecutor.Response{}, lastErr
}

func (m *Manager) executeStreamCodexModelFallback(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, initialErr error) (*cliproxyexecutor.StreamResult, error) {
	effective, reason, targets, ok := m.codexModelFallbackPlan(providers, req, opts, initialErr)
	if !ok {
		return nil, initialErr
	}

	sourceModel := req.Model
	lastErr := initialErr
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
		result, errFallback := m.executeStreamWithoutModelFallback(ctx, providers, fallbackReq, fallbackOpts)
		if errFallback == nil {
			return result, nil
		}
		if isModelFallbackBlockedError(errFallback) {
			entry.WithFields(log.Fields{
				"source_model": sourceModel,
				"target_model": target,
			}).Info("codex model fallback: blocked by reasoning continuity policy")
			return nil, initialErr
		}
		lastErr = errFallback
		if modelFallbackReasonFromError(errFallback) == "" {
			break
		}
	}
	return nil, lastErr
}
