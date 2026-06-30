package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func withRetryWithoutPenaltyUsageMetadata(opts cliproxyexecutor.Options, detail coreusage.Detail) cliproxyexecutor.Options {
	if !hasRetryWithoutPenaltyUsageDetail(detail) {
		return opts
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[cliproxyexecutor.CodexAbnormalReasoningRetryUsageMetadataKey] = detail
	opts.Metadata = meta
	return opts
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

func retryWithoutPenaltyLimit(err error) (string, int, bool) {
	if err == nil {
		return "", 0, false
	}
	var limited interface {
		RetryWithoutPenaltyClass() string
		RetryWithoutPenaltyMaxRetries() int
	}
	if !errors.As(err, &limited) {
		return "", 0, false
	}
	class := strings.TrimSpace(limited.RetryWithoutPenaltyClass())
	if class == "" {
		return "", 0, false
	}
	return class, limited.RetryWithoutPenaltyMaxRetries(), true
}

func retryWithoutPenaltyUsageDetail(err error) (coreusage.Detail, bool) {
	if err == nil {
		return coreusage.Detail{}, false
	}
	var withDetail interface {
		RetryWithoutPenaltyUsageDetail() coreusage.Detail
	}
	if !errors.As(err, &withDetail) {
		return coreusage.Detail{}, false
	}
	detail := withDetail.RetryWithoutPenaltyUsageDetail()
	return detail, hasRetryWithoutPenaltyUsageDetail(detail)
}

func addRetryWithoutPenaltyUsageDetail(a, b coreusage.Detail) coreusage.Detail {
	a = normalizeRetryWithoutPenaltyUsageDetail(a)
	b = normalizeRetryWithoutPenaltyUsageDetail(b)
	return coreusage.Detail{
		InputTokens:         a.InputTokens + b.InputTokens,
		OutputTokens:        a.OutputTokens + b.OutputTokens,
		ReasoningTokens:     a.ReasoningTokens + b.ReasoningTokens,
		CachedTokens:        a.CachedTokens + b.CachedTokens,
		CacheReadTokens:     a.CacheReadTokens + b.CacheReadTokens,
		CacheCreationTokens: a.CacheCreationTokens + b.CacheCreationTokens,
		TotalTokens:         a.TotalTokens + b.TotalTokens,
	}
}

func normalizeRetryWithoutPenaltyUsageDetail(detail coreusage.Detail) coreusage.Detail {
	if detail.TotalTokens == 0 {
		total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
		if total > 0 {
			detail.TotalTokens = total
		}
	}
	return detail
}

func hasRetryWithoutPenaltyUsageDetail(detail coreusage.Detail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0
}

type retryWithoutPenaltyExhaustedError struct {
	class string
}

func newRetryWithoutPenaltyExhaustedError(_ error, class string) error {
	return &retryWithoutPenaltyExhaustedError{class: strings.TrimSpace(class)}
}

func (e *retryWithoutPenaltyExhaustedError) Error() string {
	code, message := retryWithoutPenaltyExhaustedErrorDetail(e.class)
	body, err := json.Marshal(map[string]any{
		"error": map[string]string{
			"message": message,
			"type":    "server_error",
			"code":    code,
		},
	})
	if err != nil {
		return code + ": " + message
	}
	return string(body)
}

func (e *retryWithoutPenaltyExhaustedError) StatusCode() int {
	return http.StatusBadGateway
}

func retryWithoutPenaltyExhaustedErrorDetail(class string) (string, string) {
	switch strings.TrimSpace(class) {
	case "codex.abnormal-reasoning-retry":
		return "codex_abnormal_reasoning_retry_exhausted", "codex abnormal reasoning retry exhausted"
	default:
		return "retry_without_penalty_exhausted", "retry without penalty retry exhausted"
	}
}
