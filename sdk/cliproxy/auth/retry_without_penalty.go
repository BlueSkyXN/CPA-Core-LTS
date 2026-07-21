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
	retryWithoutPenaltyExhaustedBehaviorError         = "error"
	retryWithoutPenaltyExhaustedBehaviorPassThrough   = "pass-through"
	retryWithoutPenaltyHedgeModeSpeed                 = "speed"
	retryWithoutPenaltyHedgeModeQuality               = "quality"
	retryWithoutPenaltyDeliveryPolicyBestNonSpecial   = "best-non-special"
	retryWithoutPenaltyDeliveryPolicyFirstNonSpecial  = "first-non-special"
	retryWithoutPenaltyDeliveryPolicyMaxOutput        = "max-output"
	retryWithoutPenaltyDeliveryPolicyLatest           = "latest"
	retryWithoutPenaltyFallbackPolicyBestSpecial      = "best-special"
	retryWithoutPenaltyFallbackPolicyMaxOutputSpecial = "max-output-special"
	retryWithoutPenaltyFallbackPolicyLatestSpecial    = "latest-special"
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
	enabled              bool
	mode                 string
	deliveryPolicy       string
	fallbackPolicy       string
	hedgeDelay           time.Duration
	requireDistinctAuth  bool
	triggerAuthID        string
	streamBufferMaxBytes int64
}

type retryWithoutPenaltyFallbackCandidate struct {
	stream bool
	set    bool
	err    error
	authID string
	score  int64
	detail coreusage.Detail
	policy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy
}

func newRetryWithoutPenaltyFallbackCandidate(stream bool) *retryWithoutPenaltyFallbackCandidate {
	return &retryWithoutPenaltyFallbackCandidate{stream: stream}
}

func (c *retryWithoutPenaltyFallbackCandidate) Consider(err error, authID string) {
	if c == nil || err == nil || !isRetryWithoutPenaltyError(err) {
		return
	}
	if c.stream {
		if _, _, ok := retryWithoutPenaltyRawFallbackStreamChunks(err); !ok {
			return
		}
	} else if _, ok := retryWithoutPenaltyRawFallbackResponse(err); !ok {
		return
	}
	detail, _ := retryWithoutPenaltyUsageDetail(err)
	policy := retryWithoutPenaltyCandidatePolicyFromError(err, detail)
	score := retryWithoutPenaltyFallbackScore(detail, policy)
	if c.set && !retryWithoutPenaltyFallbackCandidateBetter(c.detail, c.policy, c.score, detail, policy, score) {
		return
	}
	c.set = true
	c.err = err
	c.authID = strings.TrimSpace(authID)
	c.score = score
	c.detail = detail
	c.policy = policy
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
		deliveryPolicy:      retryWithoutPenaltyDeliveryPolicyBestNonSpecial,
		fallbackPolicy:      retryWithoutPenaltyFallbackPolicyBestSpecial,
		hedgeDelay:          hedgeDelay,
		requireDistinctAuth: requireDistinctAuth,
	}
	var withMode interface {
		RetryWithoutPenaltyHedgeMode() string
	}
	if errors.As(err, &withMode) {
		policy.mode = normalizeRetryWithoutPenaltyHedgeMode(withMode.RetryWithoutPenaltyHedgeMode())
	}
	if deliveryPolicy := retryWithoutPenaltyDeliveryPolicyFromError(err); deliveryPolicy != "" {
		policy.deliveryPolicy = deliveryPolicy
	}
	if fallbackPolicy := retryWithoutPenaltyFallbackPolicyFromError(err); fallbackPolicy != "" {
		policy.fallbackPolicy = fallbackPolicy
	}
	var withAuthID interface {
		RetryWithoutPenaltyAuthID() string
	}
	if errors.As(err, &withAuthID) {
		policy.triggerAuthID = strings.TrimSpace(withAuthID.RetryWithoutPenaltyAuthID())
	}
	var withStreamBufferLimit interface {
		RetryWithoutPenaltyStreamBufferMaxBytes() int64
	}
	if errors.As(err, &withStreamBufferLimit) {
		if maxBytes := withStreamBufferLimit.RetryWithoutPenaltyStreamBufferMaxBytes(); maxBytes > 0 {
			policy.streamBufferMaxBytes = maxBytes
		}
	}
	return policy, true
}

func retryWithoutPenaltyDeliveryPolicyFromError(err error) string {
	if err == nil {
		return ""
	}
	var withPolicy interface {
		RetryWithoutPenaltyDeliveryPolicy() string
	}
	if !errors.As(err, &withPolicy) {
		return ""
	}
	return normalizeRetryWithoutPenaltyDeliveryPolicy(withPolicy.RetryWithoutPenaltyDeliveryPolicy())
}

func retryWithoutPenaltyFallbackPolicyFromError(err error) string {
	if err == nil {
		return ""
	}
	var withPolicy interface {
		RetryWithoutPenaltyFallbackPolicy() string
	}
	if !errors.As(err, &withPolicy) {
		return ""
	}
	return normalizeRetryWithoutPenaltyFallbackPolicy(withPolicy.RetryWithoutPenaltyFallbackPolicy())
}

func normalizeRetryWithoutPenaltyDeliveryPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case retryWithoutPenaltyDeliveryPolicyFirstNonSpecial:
		return retryWithoutPenaltyDeliveryPolicyFirstNonSpecial
	case retryWithoutPenaltyDeliveryPolicyMaxOutput:
		return retryWithoutPenaltyDeliveryPolicyMaxOutput
	case retryWithoutPenaltyDeliveryPolicyLatest:
		return retryWithoutPenaltyDeliveryPolicyLatest
	case retryWithoutPenaltyDeliveryPolicyBestNonSpecial:
		return retryWithoutPenaltyDeliveryPolicyBestNonSpecial
	default:
		return ""
	}
}

func normalizeRetryWithoutPenaltyFallbackPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case retryWithoutPenaltyFallbackPolicyMaxOutputSpecial:
		return retryWithoutPenaltyFallbackPolicyMaxOutputSpecial
	case retryWithoutPenaltyFallbackPolicyLatestSpecial:
		return retryWithoutPenaltyFallbackPolicyLatestSpecial
	case retryWithoutPenaltyFallbackPolicyBestSpecial:
		return retryWithoutPenaltyFallbackPolicyBestSpecial
	default:
		return ""
	}
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

func retryWithoutPenaltyCandidatePolicyFromError(err error, detail coreusage.Detail) cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy {
	var policy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy
	if err != nil {
		var withPolicy interface {
			RetryWithoutPenaltyCandidatePolicy() cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy
		}
		if errors.As(err, &withPolicy) {
			policy = withPolicy.RetryWithoutPenaltyCandidatePolicy()
		}
	}
	policy = normalizeRetryWithoutPenaltyCandidatePolicy(policy, detail)
	if policy.CandidateKind == "" {
		policy.CandidateKind = cliproxyexecutor.RetryWithoutPenaltyCandidateKindSpecial
	}
	if policy.FallbackPolicy == "" {
		if fallbackPolicy := retryWithoutPenaltyFallbackPolicyFromError(err); fallbackPolicy != "" {
			policy.FallbackPolicy = fallbackPolicy
		} else {
			policy.FallbackPolicy = retryWithoutPenaltyFallbackPolicyBestSpecial
		}
	}
	return policy
}

func normalizeRetryWithoutPenaltyCandidatePolicy(policy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy, detail coreusage.Detail) cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy {
	if hasRetryWithoutPenaltyUsageDetail(detail) {
		detail = normalizeRetryWithoutPenaltyUsageDetail(detail)
		if policy.ReasoningTokens == 0 {
			policy.ReasoningTokens = detail.ReasoningTokens
		}
		if policy.OutputTokens == 0 {
			policy.OutputTokens = detail.OutputTokens
		}
	}
	if policy.VisibleTokens == 0 && policy.OutputTokens > policy.ReasoningTokens {
		policy.VisibleTokens = policy.OutputTokens - policy.ReasoningTokens
	}
	policy.DeliveryPolicy = normalizeRetryWithoutPenaltyDeliveryPolicy(policy.DeliveryPolicy)
	policy.FallbackPolicy = normalizeRetryWithoutPenaltyFallbackPolicy(policy.FallbackPolicy)
	switch strings.ToLower(strings.TrimSpace(policy.CandidateKind)) {
	case cliproxyexecutor.RetryWithoutPenaltyCandidateKindNonSpecial:
		policy.CandidateKind = cliproxyexecutor.RetryWithoutPenaltyCandidateKindNonSpecial
	case cliproxyexecutor.RetryWithoutPenaltyCandidateKindSpecial:
		policy.CandidateKind = cliproxyexecutor.RetryWithoutPenaltyCandidateKindSpecial
	default:
		policy.CandidateKind = ""
	}
	return policy
}

func retryWithoutPenaltyFallbackScore(detail coreusage.Detail, policy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy) int64 {
	policy = normalizeRetryWithoutPenaltyCandidatePolicy(policy, detail)
	if policy.OutputTokens > 0 {
		return policy.OutputTokens
	}
	detail = normalizeRetryWithoutPenaltyUsageDetail(detail)
	return detail.OutputTokens
}

func retryWithoutPenaltyFallbackCandidateBetter(currentDetail coreusage.Detail, currentPolicy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy, currentScore int64, nextDetail coreusage.Detail, nextPolicy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy, nextScore int64) bool {
	currentPolicy = normalizeRetryWithoutPenaltyCandidatePolicy(currentPolicy, currentDetail)
	nextPolicy = normalizeRetryWithoutPenaltyCandidatePolicy(nextPolicy, nextDetail)
	switch nextPolicy.FallbackPolicy {
	case retryWithoutPenaltyFallbackPolicyLatestSpecial:
		return true
	case retryWithoutPenaltyFallbackPolicyMaxOutputSpecial:
		if nextScore != currentScore {
			return nextScore > currentScore
		}
		if nextPolicy.VisibleTokens != currentPolicy.VisibleTokens {
			return nextPolicy.VisibleTokens > currentPolicy.VisibleTokens
		}
		return true
	default:
		if nextPolicy.ReasoningTokens != currentPolicy.ReasoningTokens {
			return nextPolicy.ReasoningTokens > currentPolicy.ReasoningTokens
		}
		if nextScore != currentScore {
			return nextScore > currentScore
		}
		if nextPolicy.VisibleTokens != currentPolicy.VisibleTokens {
			return nextPolicy.VisibleTokens > currentPolicy.VisibleTokens
		}
		return true
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

func retryWithoutPenaltyMaybeSelectFallbackResponse(resp cliproxyexecutor.Response, candidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator) (cliproxyexecutor.Response, bool) {
	if candidate == nil || !candidate.set || candidate.err == nil {
		return cliproxyexecutor.Response{}, false
	}
	detail, ok := retryWithoutPenaltyResponseUsage(resp.Metadata)
	if !ok {
		return cliproxyexecutor.Response{}, false
	}
	score := retryWithoutPenaltyHedgeScore(resp.Metadata)
	policy, hasPolicy := retryWithoutPenaltyCandidatePolicyFromMetadata(resp.Metadata, detail)
	if !hasPolicy {
		return cliproxyexecutor.Response{}, false
	}
	if !retryWithoutPenaltyFallbackShouldReplaceDelivered(detail, policy, score, candidate) {
		return cliproxyexecutor.Response{}, false
	}
	fallbackResp, ok := retryWithoutPenaltyRawFallbackResponse(candidate.err)
	if !ok {
		return cliproxyexecutor.Response{}, false
	}
	finalizer, _ := fallbackResp.Metadata[cliproxyexecutor.RetryWithoutPenaltyResponseFinalizerMetadataKey].(cliproxyexecutor.RetryWithoutPenaltyResponseFinalizer)
	if finalizer == nil {
		return fallbackResp, true
	}
	return finalizer(fallbackResp, retryWithoutPenaltyMixedFallbackSnapshot(candidate, accumulator, detail)), true
}

func retryWithoutPenaltyRawFallbackResponse(err error) (cliproxyexecutor.Response, bool) {
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

func retryWithoutPenaltyMaybeSelectFallbackStreamResult(result *cliproxyexecutor.StreamResult, candidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator) (*cliproxyexecutor.StreamResult, bool) {
	if result == nil || candidate == nil || !candidate.set || candidate.err == nil {
		return nil, false
	}
	detail, score, ok := retryWithoutPenaltyStreamUsage(result.Metadata)
	if !ok {
		return nil, false
	}
	policy, hasPolicy := retryWithoutPenaltyCandidatePolicyFromMetadata(result.Metadata, detail)
	if !hasPolicy {
		return nil, false
	}
	if !retryWithoutPenaltyFallbackShouldReplaceDelivered(detail, policy, score, candidate) {
		return nil, false
	}
	return retryWithoutPenaltyRawCandidateFallbackStreamResultWithSnapshot(candidate.err, retryWithoutPenaltyMixedFallbackSnapshot(candidate, accumulator, detail))
}

func retryWithoutPenaltyCandidateFallbackStreamResult(err error, candidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator) (*cliproxyexecutor.StreamResult, bool) {
	if retryWithoutPenaltyExhaustedBehavior(err) != retryWithoutPenaltyExhaustedBehaviorPassThrough {
		return nil, false
	}
	return retryWithoutPenaltyRawCandidateFallbackStreamResult(err, candidate, accumulator)
}

func retryWithoutPenaltyRawCandidateFallbackStreamResult(err error, candidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator) (*cliproxyexecutor.StreamResult, bool) {
	return retryWithoutPenaltyRawCandidateFallbackStreamResultWithSnapshot(err, candidate.PreviousUsageSnapshot(accumulator))
}

func retryWithoutPenaltyRawCandidateFallbackStreamResultWithSnapshot(err error, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot) (*cliproxyexecutor.StreamResult, bool) {
	headers, chunks, ok := retryWithoutPenaltyRawFallbackStreamChunks(err)
	if !ok || len(chunks) == 0 {
		return nil, false
	}
	var withFinalizer interface {
		RetryWithoutPenaltyFallbackStreamFinalizer() cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer
	}
	if errors.As(err, &withFinalizer) {
		if finalizer := withFinalizer.RetryWithoutPenaltyFallbackStreamFinalizer(); finalizer != nil {
			if result := finalizer(headers, chunks, previous); result != nil {
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

func retryWithoutPenaltyRawFallbackStreamChunks(err error) (http.Header, []cliproxyexecutor.StreamChunk, bool) {
	var withFallback interface {
		RetryWithoutPenaltyFallbackStreamChunks() (http.Header, []cliproxyexecutor.StreamChunk, bool)
	}
	if !errors.As(err, &withFallback) {
		return nil, nil, false
	}
	headers, chunks, ok := withFallback.RetryWithoutPenaltyFallbackStreamChunks()
	if !ok || len(chunks) == 0 {
		return nil, nil, false
	}
	return headers, chunks, true
}

func retryWithoutPenaltyFallbackShouldReplaceDelivered(deliveredDetail coreusage.Detail, deliveredPolicy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy, deliveredScore int64, candidate *retryWithoutPenaltyFallbackCandidate) bool {
	if candidate == nil || !candidate.set {
		return false
	}
	deliveredPolicy = normalizeRetryWithoutPenaltyCandidatePolicy(deliveredPolicy, deliveredDetail)
	candidatePolicy := normalizeRetryWithoutPenaltyCandidatePolicy(candidate.policy, candidate.detail)
	deliveryPolicy := deliveredPolicy.DeliveryPolicy
	if deliveryPolicy == "" {
		deliveryPolicy = candidatePolicy.DeliveryPolicy
	}
	if deliveryPolicy != retryWithoutPenaltyDeliveryPolicyMaxOutput {
		return false
	}
	if candidate.score != deliveredScore {
		return candidate.score > deliveredScore
	}
	if candidatePolicy.VisibleTokens != deliveredPolicy.VisibleTokens {
		return candidatePolicy.VisibleTokens > deliveredPolicy.VisibleTokens
	}
	return false
}

func retryWithoutPenaltyMixedFallbackSnapshot(candidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator, deliveredDetail coreusage.Detail) cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot {
	snapshot := candidate.PreviousUsageSnapshot(accumulator)
	if hasRetryWithoutPenaltyUsageDetail(deliveredDetail) {
		delivered := normalizeRetryWithoutPenaltyUsageDetail(deliveredDetail)
		snapshot.Detail = addRetryWithoutPenaltyUsageDetail(snapshot.Detail, delivered)
		snapshot.FoldedOutputTokens = addNonNegativeRetryWithoutPenaltyTokens(
			snapshot.FoldedOutputTokens,
			foldedRetryWithoutPenaltyUsageOutputTokens(delivered),
		)
	}
	return snapshot
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
		InputTokens:         addNonNegativeRetryWithoutPenaltyTokens(a.InputTokens, b.InputTokens),
		OutputTokens:        addNonNegativeRetryWithoutPenaltyTokens(a.OutputTokens, b.OutputTokens),
		ReasoningTokens:     addNonNegativeRetryWithoutPenaltyTokens(a.ReasoningTokens, b.ReasoningTokens),
		CachedTokens:        addNonNegativeRetryWithoutPenaltyTokens(a.CachedTokens, b.CachedTokens),
		CacheReadTokens:     addNonNegativeRetryWithoutPenaltyTokens(a.CacheReadTokens, b.CacheReadTokens),
		CacheCreationTokens: addNonNegativeRetryWithoutPenaltyTokens(a.CacheCreationTokens, b.CacheCreationTokens),
		TotalTokens:         addNonNegativeRetryWithoutPenaltyTokens(a.TotalTokens, b.TotalTokens),
	}
}

func normalizeRetryWithoutPenaltyUsageDetail(detail coreusage.Detail) coreusage.Detail {
	detail.InputTokens = nonNegativeRetryWithoutPenaltyToken(detail.InputTokens)
	detail.OutputTokens = nonNegativeRetryWithoutPenaltyToken(detail.OutputTokens)
	detail.ReasoningTokens = nonNegativeRetryWithoutPenaltyToken(detail.ReasoningTokens)
	detail.CachedTokens = nonNegativeRetryWithoutPenaltyToken(detail.CachedTokens)
	detail.CacheReadTokens = nonNegativeRetryWithoutPenaltyToken(detail.CacheReadTokens)
	detail.CacheCreationTokens = nonNegativeRetryWithoutPenaltyToken(detail.CacheCreationTokens)
	detail.TotalTokens = nonNegativeRetryWithoutPenaltyToken(detail.TotalTokens)
	if detail.TotalTokens == 0 {
		if total, ok := sumNonNegativeRetryWithoutPenaltyTokens(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens); ok && total > 0 {
			detail.TotalTokens = total
		}
	}
	return detail
}

func nonNegativeRetryWithoutPenaltyToken(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func addNonNegativeRetryWithoutPenaltyTokens(a, b int64) int64 {
	const maxInt64 = int64(1<<63 - 1)
	if a < 0 || b < 0 || a > maxInt64-b {
		return 0
	}
	return a + b
}

func sumNonNegativeRetryWithoutPenaltyTokens(tokens ...int64) (int64, bool) {
	const maxInt64 = int64(1<<63 - 1)
	var total int64
	for _, tokens := range tokens {
		if tokens < 0 || tokens > maxInt64-total {
			return 0, false
		}
		total += tokens
	}
	return total, true
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
