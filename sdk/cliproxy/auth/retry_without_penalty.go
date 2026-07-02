package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	retryWithoutPenaltyExhaustedBehaviorError       = "error"
	retryWithoutPenaltyExhaustedBehaviorPassThrough = "pass-through"
)

func withRetryWithoutPenaltyUsageMetadata(opts cliproxyexecutor.Options, accumulator *cliproxyexecutor.UsageAccumulator) cliproxyexecutor.Options {
	if accumulator == nil || !hasRetryWithoutPenaltyUsageDetail(accumulator.Snapshot()) {
		return opts
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[cliproxyexecutor.CodexAbnormalReasoningRetryUsageMetadataKey] = accumulator
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

func retryWithoutPenaltyRemainingRetries(err error, retryWithoutPenaltyCounts map[string]int) (string, int, error, bool) {
	class, maxRetries, ok := retryWithoutPenaltyLimit(err)
	if !ok {
		return "", 0, nil, false
	}
	if maxRetries <= 0 {
		return class, 0, newRetryWithoutPenaltyExhaustedError(err, class), true
	}
	used := 0
	if retryWithoutPenaltyCounts != nil {
		used = retryWithoutPenaltyCounts[class]
	}
	if used >= maxRetries {
		return class, 0, newRetryWithoutPenaltyExhaustedError(err, class), true
	}
	return class, maxRetries - used, nil, true
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

func retryWithoutPenaltyExhaustedBehavior(err error) string {
	if err == nil {
		return retryWithoutPenaltyExhaustedBehaviorError
	}
	var withBehavior interface {
		RetryWithoutPenaltyExhaustedBehavior() string
	}
	if !errors.As(err, &withBehavior) {
		return retryWithoutPenaltyExhaustedBehaviorError
	}
	return normalizeRetryWithoutPenaltyExhaustedBehavior(withBehavior.RetryWithoutPenaltyExhaustedBehavior())
}

type retryWithoutPenaltyHedgePolicy struct {
	enabled             bool
	hedgeDelay          time.Duration
	requireDistinctAuth bool
	triggerAuthID       string
}

func retryWithoutPenaltyHedgePolicyFromError(err error) (retryWithoutPenaltyHedgePolicy, bool) {
	if err == nil {
		return retryWithoutPenaltyHedgePolicy{}, false
	}
	var withPolicy interface {
		RetryWithoutPenaltyHedgePolicy() (bool, time.Duration, bool)
	}
	if !errors.As(err, &withPolicy) {
		return retryWithoutPenaltyHedgePolicy{}, false
	}
	enabled, hedgeDelay, requireDistinctAuth := withPolicy.RetryWithoutPenaltyHedgePolicy()
	if hedgeDelay < 0 {
		hedgeDelay = 0
	}
	policy := retryWithoutPenaltyHedgePolicy{
		enabled:             enabled,
		hedgeDelay:          hedgeDelay,
		requireDistinctAuth: requireDistinctAuth,
	}
	var withAuthID interface {
		RetryWithoutPenaltyAuthID() string
	}
	if errors.As(err, &withAuthID) {
		policy.triggerAuthID = strings.TrimSpace(withAuthID.RetryWithoutPenaltyAuthID())
	}
	return policy, true
}

func normalizeRetryWithoutPenaltyExhaustedBehavior(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pass-through", "passthrough", "pass_through":
		return retryWithoutPenaltyExhaustedBehaviorPassThrough
	default:
		return retryWithoutPenaltyExhaustedBehaviorError
	}
}

func retryWithoutPenaltyFallbackResponse(err error) (cliproxyexecutor.Response, bool) {
	if retryWithoutPenaltyExhaustedBehavior(err) != retryWithoutPenaltyExhaustedBehaviorPassThrough {
		return cliproxyexecutor.Response{}, false
	}
	var withFallback interface {
		RetryWithoutPenaltyFallbackResponse() (cliproxyexecutor.Response, bool)
	}
	if !errors.As(err, &withFallback) {
		return cliproxyexecutor.Response{}, false
	}
	resp, ok := withFallback.RetryWithoutPenaltyFallbackResponse()
	if !ok {
		return cliproxyexecutor.Response{}, false
	}
	return cloneRetryWithoutPenaltyResponse(resp), true
}

func retryWithoutPenaltyFallbackStreamResult(err error) (*cliproxyexecutor.StreamResult, bool) {
	if retryWithoutPenaltyExhaustedBehavior(err) != retryWithoutPenaltyExhaustedBehaviorPassThrough {
		return nil, false
	}
	var withFallback interface {
		RetryWithoutPenaltyFallbackStreamChunks() (http.Header, []cliproxyexecutor.StreamChunk, bool)
	}
	if !errors.As(err, &withFallback) {
		return nil, false
	}
	headers, chunks, ok := withFallback.RetryWithoutPenaltyFallbackStreamChunks()
	if !ok || len(chunks) == 0 {
		return nil, false
	}
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for i := range chunks {
		chunk := chunks[i]
		chunk.Payload = bytes.Clone(chunk.Payload)
		out <- chunk
	}
	close(out)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneRetryWithoutPenaltyHeader(headers),
		Chunks:  out,
	}, true
}

func cloneRetryWithoutPenaltyResponse(resp cliproxyexecutor.Response) cliproxyexecutor.Response {
	resp.Payload = bytes.Clone(resp.Payload)
	resp.Headers = cloneRetryWithoutPenaltyHeader(resp.Headers)
	if resp.Metadata != nil {
		meta := make(map[string]any, len(resp.Metadata))
		for key, value := range resp.Metadata {
			meta[key] = value
		}
		resp.Metadata = meta
	}
	return resp
}

func cloneRetryWithoutPenaltyHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
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
