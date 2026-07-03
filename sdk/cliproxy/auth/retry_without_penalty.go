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
	retryWithoutPenaltyHedgeModeSpeed               = "speed"
	retryWithoutPenaltyHedgeModeQuality             = "quality"
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

func retryWithoutPenaltyAuthIDFromError(err error) string {
	if err == nil {
		return ""
	}
	var withAuthID interface {
		RetryWithoutPenaltyAuthID() string
	}
	if !errors.As(err, &withAuthID) {
		return ""
	}
	return strings.TrimSpace(withAuthID.RetryWithoutPenaltyAuthID())
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
	mode                string
	hedgeDelay          time.Duration
	requireDistinctAuth bool
	triggerAuthID       string
}

type retryWithoutPenaltyFallbackCandidate struct {
	stream bool
	set    bool
	err    error
	authID string
	score  int64
	detail coreusage.Detail
}

func newRetryWithoutPenaltyFallbackCandidate(stream bool) *retryWithoutPenaltyFallbackCandidate {
	return &retryWithoutPenaltyFallbackCandidate{stream: stream}
}

func (c *retryWithoutPenaltyFallbackCandidate) Consider(err error, authID string) {
	if c == nil || err == nil || !isRetryWithoutPenaltyError(err) {
		return
	}
	if retryWithoutPenaltyExhaustedBehavior(err) != retryWithoutPenaltyExhaustedBehaviorPassThrough {
		return
	}
	if c.stream {
		if _, ok := retryWithoutPenaltyFallbackStreamResult(err); !ok {
			return
		}
	} else if _, ok := retryWithoutPenaltyFallbackResponse(err); !ok {
		return
	}
	detail, _ := retryWithoutPenaltyUsageDetail(err)
	score := detail.OutputTokens
	if c.set && score <= c.score {
		return
	}
	c.set = true
	c.err = err
	c.authID = strings.TrimSpace(authID)
	c.score = score
	c.detail = detail
}

func (c *retryWithoutPenaltyFallbackCandidate) Err(fallback error) error {
	if c != nil && c.set && c.err != nil {
		return c.err
	}
	return fallback
}

func (c *retryWithoutPenaltyFallbackCandidate) AuthID(fallback string) string {
	if c != nil && c.set && c.authID != "" {
		return c.authID
	}
	return fallback
}

func (c *retryWithoutPenaltyFallbackCandidate) PreviousUsageSnapshot(accumulator *cliproxyexecutor.UsageAccumulator) cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot {
	if accumulator == nil {
		return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{}
	}
	snapshot := accumulator.RetryWithoutPenaltySnapshot()
	if c == nil || !c.set || !hasRetryWithoutPenaltyUsageDetail(c.detail) {
		return snapshot
	}
	selected := normalizeRetryWithoutPenaltyUsageDetail(c.detail)
	snapshot.Detail = subtractRetryWithoutPenaltyUsageDetail(snapshot.Detail, selected)
	snapshot.FoldedOutputTokens -= foldedRetryWithoutPenaltyUsageOutputTokens(selected)
	if snapshot.FoldedOutputTokens < 0 {
		snapshot.FoldedOutputTokens = 0
	}
	return snapshot
}

func subtractRetryWithoutPenaltyUsageDetail(total, selected coreusage.Detail) coreusage.Detail {
	total = normalizeRetryWithoutPenaltyUsageDetail(total)
	selected = normalizeRetryWithoutPenaltyUsageDetail(selected)
	return coreusage.Detail{
		InputTokens:         subtractRetryWithoutPenaltyInt64(total.InputTokens, selected.InputTokens),
		OutputTokens:        subtractRetryWithoutPenaltyInt64(total.OutputTokens, selected.OutputTokens),
		ReasoningTokens:     subtractRetryWithoutPenaltyInt64(total.ReasoningTokens, selected.ReasoningTokens),
		CachedTokens:        subtractRetryWithoutPenaltyInt64(total.CachedTokens, selected.CachedTokens),
		CacheReadTokens:     subtractRetryWithoutPenaltyInt64(total.CacheReadTokens, selected.CacheReadTokens),
		CacheCreationTokens: subtractRetryWithoutPenaltyInt64(total.CacheCreationTokens, selected.CacheCreationTokens),
		TotalTokens:         subtractRetryWithoutPenaltyInt64(total.TotalTokens, selected.TotalTokens),
	}
}

func subtractRetryWithoutPenaltyInt64(total, selected int64) int64 {
	if total <= selected {
		return 0
	}
	return total - selected
}

func foldedRetryWithoutPenaltyUsageOutputTokens(detail coreusage.Detail) int64 {
	detail = normalizeRetryWithoutPenaltyUsageDetail(detail)
	if detail.OutputTokens >= detail.ReasoningTokens {
		return detail.OutputTokens
	}
	return detail.ReasoningTokens
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
		mode:                retryWithoutPenaltyHedgeModeSpeed,
		hedgeDelay:          hedgeDelay,
		requireDistinctAuth: requireDistinctAuth,
	}
	var withMode interface {
		RetryWithoutPenaltyHedgeMode() string
	}
	if errors.As(err, &withMode) {
		policy.mode = normalizeRetryWithoutPenaltyHedgeMode(withMode.RetryWithoutPenaltyHedgeMode())
	}
	var withAuthID interface {
		RetryWithoutPenaltyAuthID() string
	}
	if errors.As(err, &withAuthID) {
		policy.triggerAuthID = strings.TrimSpace(withAuthID.RetryWithoutPenaltyAuthID())
	}
	return policy, true
}

func normalizeRetryWithoutPenaltyHedgeMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "quality":
		return retryWithoutPenaltyHedgeModeQuality
	default:
		return retryWithoutPenaltyHedgeModeSpeed
	}
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

func retryWithoutPenaltyCandidateFallbackResponse(err error, candidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator) (cliproxyexecutor.Response, bool) {
	resp, ok := retryWithoutPenaltyFallbackResponse(err)
	if !ok {
		return cliproxyexecutor.Response{}, false
	}
	finalizer, _ := resp.Metadata[cliproxyexecutor.RetryWithoutPenaltyResponseFinalizerMetadataKey].(cliproxyexecutor.RetryWithoutPenaltyResponseFinalizer)
	if finalizer == nil {
		return resp, true
	}
	return finalizer(resp, candidate.PreviousUsageSnapshot(accumulator)), true
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

func retryWithoutPenaltyCandidateFallbackStreamResult(err error, candidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator) (*cliproxyexecutor.StreamResult, bool) {
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
	var withFinalizer interface {
		RetryWithoutPenaltyFallbackStreamFinalizer() cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer
	}
	if errors.As(err, &withFinalizer) {
		if finalizer := withFinalizer.RetryWithoutPenaltyFallbackStreamFinalizer(); finalizer != nil {
			if result := finalizer(headers, chunks, candidate.PreviousUsageSnapshot(accumulator)); result != nil {
				return result, true
			}
		}
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
