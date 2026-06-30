package executor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type codexAbnormalReasoningRetryPolicy struct {
	enabled         bool
	streamBuffer    bool
	modelContains   []string
	reasoningTokens map[int64]struct{}
}

type codexAbnormalReasoningRetryError struct {
	reasoningTokens int64
}

func (e *codexAbnormalReasoningRetryError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("codex abnormal reasoning retry: reasoning_tokens=%d matched retry policy", e.reasoningTokens)
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenalty() bool {
	return e != nil
}

func newCodexAbnormalReasoningRetryPolicy(cfg *config.Config, auth *cliproxyauth.Auth, requestedModel string, upstreamModels ...string) codexAbnormalReasoningRetryPolicy {
	if cfg == nil || auth == nil {
		return codexAbnormalReasoningRetryPolicy{}
	}
	effective := cfg.Codex.AbnormalReasoningRetry.Effective()
	if !effective.Enabled {
		return codexAbnormalReasoningRetryPolicy{}
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return codexAbnormalReasoningRetryPolicy{}
	}
	if len(effective.AuthIDs) > 0 && !stringListContains(effective.AuthIDs, strings.TrimSpace(auth.ID), false) {
		return codexAbnormalReasoningRetryPolicy{}
	}
	authKind := normalizeCodexAbnormalReasoningRetryAuthKind(codexAbnormalReasoningRetryAuthKind(auth))
	if !stringListContainsNormalizedAuthKind(effective.AuthKinds, authKind) {
		return codexAbnormalReasoningRetryPolicy{}
	}
	modelContains := normalizedContainsList(effective.ModelContains)
	if len(modelContains) == 0 || !codexAbnormalReasoningRetryModelMatches(modelContains, requestedModel, upstreamModels...) {
		return codexAbnormalReasoningRetryPolicy{}
	}
	tokens := make(map[int64]struct{}, len(effective.ReasoningTokens))
	for _, token := range effective.ReasoningTokens {
		if token > 0 {
			tokens[token] = struct{}{}
		}
	}
	if len(tokens) == 0 {
		return codexAbnormalReasoningRetryPolicy{}
	}
	return codexAbnormalReasoningRetryPolicy{
		enabled:         true,
		streamBuffer:    effective.StreamBuffer,
		modelContains:   modelContains,
		reasoningTokens: tokens,
	}
}

func (p codexAbnormalReasoningRetryPolicy) Enabled() bool {
	return p.enabled
}

func (p codexAbnormalReasoningRetryPolicy) StreamBuffer() bool {
	return p.enabled && p.streamBuffer
}

func (p codexAbnormalReasoningRetryPolicy) RetryError(detail usage.Detail) error {
	if !p.enabled || detail.ReasoningTokens <= 0 {
		return nil
	}
	if _, ok := p.reasoningTokens[detail.ReasoningTokens]; !ok {
		return nil
	}
	return &codexAbnormalReasoningRetryError{reasoningTokens: detail.ReasoningTokens}
}

func isRetryWithoutPenaltyError(err error) bool {
	if err == nil {
		return false
	}
	var retry interface {
		RetryWithoutPenalty() bool
	}
	return errors.As(err, &retry) && retry.RetryWithoutPenalty()
}

func codexAbnormalReasoningRetryAuthKind(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if kind := strings.TrimSpace(auth.Attributes["auth_kind"]); kind != "" {
			return kind
		}
	}
	kind, _ := auth.AccountInfo()
	return kind
}

func normalizeCodexAbnormalReasoningRetryAuthKind(kind string) string {
	normalized := strings.ToLower(strings.TrimSpace(kind))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	switch normalized {
	case "apikey", "api_key":
		return "api_key"
	default:
		return normalized
	}
}

func stringListContainsNormalizedAuthKind(values []string, needle string) bool {
	if strings.TrimSpace(needle) == "" {
		return false
	}
	for _, value := range values {
		if normalizeCodexAbnormalReasoningRetryAuthKind(value) == needle {
			return true
		}
	}
	return false
}

func codexAbnormalReasoningRetryModelMatches(contains []string, requestedModel string, upstreamModels ...string) bool {
	models := make([]string, 0, 1+len(upstreamModels))
	models = append(models, requestedModel)
	models = append(models, upstreamModels...)
	for _, model := range models {
		normalizedModel := strings.ToLower(strings.TrimSpace(model))
		if normalizedModel == "" {
			continue
		}
		for _, needle := range contains {
			if needle != "" && strings.Contains(normalizedModel, needle) {
				return true
			}
		}
	}
	return false
}

func normalizedContainsList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func stringListContains(values []string, needle string, fold bool) bool {
	if strings.TrimSpace(needle) == "" {
		return false
	}
	for _, value := range values {
		if fold {
			if strings.EqualFold(strings.TrimSpace(value), needle) {
				return true
			}
			continue
		}
		if strings.TrimSpace(value) == needle {
			return true
		}
	}
	return false
}
