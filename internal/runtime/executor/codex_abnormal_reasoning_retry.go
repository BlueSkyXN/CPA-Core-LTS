package executor

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexAbnormalReasoningRetryPolicy struct {
	enabled           bool
	streamBuffer      bool
	streamBufferMax   int64
	maxRetries        int
	exhaustedBehavior string
	hedgeEnabled      bool
	hedgeDelay        time.Duration
	requireDistinct   bool
	authID            string
	modelContains     []string
	reasoningEfforts  map[string]struct{}
	reasoningTokens   map[int64]struct{}
}

type codexAbnormalReasoningRetryError struct {
	detail                usage.Detail
	maxRetries            int
	exhaustedBehavior     string
	hedgeEnabled          bool
	hedgeDelay            time.Duration
	requireDistinctAuth   bool
	authID                string
	fallbackResponse      *cliproxyexecutor.Response
	fallbackStreamHeaders http.Header
	fallbackStreamChunks  []cliproxyexecutor.StreamChunk
}

const (
	codexAbnormalReasoningRetryClass            = "codex.abnormal-reasoning-retry"
	codexAbnormalReasoningRetryUsageFailureCode = "codex_abnormal_reasoning_response"
)

func (e *codexAbnormalReasoningRetryError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: codex abnormal reasoning response discarded: reasoning_tokens=%d", codexAbnormalReasoningRetryUsageFailureCode, e.detail.ReasoningTokens)
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenalty() bool {
	return e != nil
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyClass() string {
	if e == nil {
		return ""
	}
	return codexAbnormalReasoningRetryClass
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyMaxRetries() int {
	if e == nil {
		return 0
	}
	return e.maxRetries
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyExhaustedBehavior() string {
	if e == nil || strings.TrimSpace(e.exhaustedBehavior) == "" {
		return config.CodexAbnormalReasoningRetryExhaustedBehaviorError
	}
	return e.exhaustedBehavior
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyHedgePolicy() (bool, time.Duration, bool) {
	if e == nil {
		return false, 0, true
	}
	return e.hedgeEnabled, e.hedgeDelay, e.requireDistinctAuth
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyAuthID() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.authID)
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyFallbackResponse() (cliproxyexecutor.Response, bool) {
	if e == nil || e.fallbackResponse == nil {
		return cliproxyexecutor.Response{}, false
	}
	return cloneCodexAbnormalReasoningRetryResponse(*e.fallbackResponse), true
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyFallbackStreamChunks() (http.Header, []cliproxyexecutor.StreamChunk, bool) {
	if e == nil || len(e.fallbackStreamChunks) == 0 {
		return nil, nil, false
	}
	return cloneCodexAbnormalReasoningRetryHeader(e.fallbackStreamHeaders), cloneCodexAbnormalReasoningRetryStreamChunks(e.fallbackStreamChunks), true
}

func (e *codexAbnormalReasoningRetryError) UsageFailureCode() string {
	if e == nil {
		return ""
	}
	return codexAbnormalReasoningRetryUsageFailureCode
}

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyUsageDetail() usage.Detail {
	if e == nil {
		return usage.Detail{}
	}
	return e.detail
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
	efforts := make(map[string]struct{}, len(effective.ReasoningEfforts))
	for _, effort := range effective.ReasoningEfforts {
		effort = normalizeCodexAbnormalReasoningRetryEffort(effort)
		if effort != "" {
			efforts[effort] = struct{}{}
		}
	}
	return codexAbnormalReasoningRetryPolicy{
		enabled:           true,
		streamBuffer:      effective.StreamBuffer,
		streamBufferMax:   effective.StreamBufferMaxBytes,
		maxRetries:        effective.MaxRetries,
		exhaustedBehavior: effective.ExhaustedBehavior,
		hedgeEnabled:      effective.HedgedRetry.Enabled,
		hedgeDelay:        time.Duration(effective.HedgedRetry.HedgeDelayMS) * time.Millisecond,
		requireDistinct:   effective.HedgedRetry.RequireDistinctAuth,
		authID:            strings.TrimSpace(auth.ID),
		modelContains:     modelContains,
		reasoningEfforts:  efforts,
		reasoningTokens:   tokens,
	}
}

func (p codexAbnormalReasoningRetryPolicy) Enabled() bool {
	return p.enabled
}

func (p codexAbnormalReasoningRetryPolicy) StreamBuffer() bool {
	return p.enabled && p.streamBuffer
}

func (p codexAbnormalReasoningRetryPolicy) StreamBufferMaxBytes() int64 {
	if !p.enabled || p.streamBufferMax < 0 {
		return 0
	}
	return p.streamBufferMax
}

func (p codexAbnormalReasoningRetryPolicy) RetryError(detail usage.Detail, reasoningEffort string) error {
	return p.retryError(detail, reasoningEffort, nil, nil, nil)
}

func (p codexAbnormalReasoningRetryPolicy) RetryErrorWithFallbackResponse(detail usage.Detail, reasoningEffort string, fallback cliproxyexecutor.Response) error {
	return p.retryError(detail, reasoningEffort, &fallback, nil, nil)
}

func (p codexAbnormalReasoningRetryPolicy) RetryErrorWithFallbackStreamChunks(detail usage.Detail, reasoningEffort string, headers http.Header, chunks []cliproxyexecutor.StreamChunk) error {
	return p.retryError(detail, reasoningEffort, nil, headers, chunks)
}

func (p codexAbnormalReasoningRetryPolicy) retryError(detail usage.Detail, reasoningEffort string, fallbackResponse *cliproxyexecutor.Response, fallbackStreamHeaders http.Header, fallbackStreamChunks []cliproxyexecutor.StreamChunk) error {
	if !p.enabled || detail.ReasoningTokens <= 0 {
		return nil
	}
	if _, ok := p.reasoningTokens[detail.ReasoningTokens]; !ok {
		return nil
	}
	if len(p.reasoningEfforts) > 0 {
		if _, ok := p.reasoningEfforts[normalizeCodexAbnormalReasoningRetryEffort(reasoningEffort)]; !ok {
			return nil
		}
	}
	err := &codexAbnormalReasoningRetryError{
		detail:              detail,
		maxRetries:          p.maxRetries,
		exhaustedBehavior:   p.exhaustedBehavior,
		hedgeEnabled:        p.hedgeEnabled,
		hedgeDelay:          p.hedgeDelay,
		requireDistinctAuth: p.requireDistinct,
		authID:              p.authID,
	}
	if fallbackResponse != nil {
		resp := cloneCodexAbnormalReasoningRetryResponse(*fallbackResponse)
		err.fallbackResponse = &resp
	}
	if len(fallbackStreamChunks) > 0 {
		err.fallbackStreamHeaders = cloneCodexAbnormalReasoningRetryHeader(fallbackStreamHeaders)
		err.fallbackStreamChunks = cloneCodexAbnormalReasoningRetryStreamChunks(fallbackStreamChunks)
	}
	return err
}

func cloneCodexAbnormalReasoningRetryResponse(resp cliproxyexecutor.Response) cliproxyexecutor.Response {
	resp.Payload = bytes.Clone(resp.Payload)
	resp.Headers = cloneCodexAbnormalReasoningRetryHeader(resp.Headers)
	if resp.Metadata != nil {
		meta := make(map[string]any, len(resp.Metadata))
		for key, value := range resp.Metadata {
			meta[key] = value
		}
		resp.Metadata = meta
	}
	return resp
}

func cloneCodexAbnormalReasoningRetryHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
}

func cloneCodexAbnormalReasoningRetryStreamChunks(chunks []cliproxyexecutor.StreamChunk) []cliproxyexecutor.StreamChunk {
	if len(chunks) == 0 {
		return nil
	}
	out := make([]cliproxyexecutor.StreamChunk, len(chunks))
	for i := range chunks {
		out[i] = chunks[i]
		out[i].Payload = bytes.Clone(chunks[i].Payload)
	}
	return out
}

func normalizeCodexAbnormalReasoningRetryEffort(effort string) string {
	return strings.ToLower(strings.TrimSpace(effort))
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

func patchCodexAbnormalReasoningClientUsage(eventData []byte, metadata map[string]any) []byte {
	previous, ok := codexAbnormalReasoningRetryUsageFromMetadata(metadata)
	if !ok {
		return eventData
	}
	current, ok := helps.ParseCodexUsage(eventData)
	if !ok {
		return eventData
	}
	total := addCodexUsageDetail(previous, current)
	usageNode := gjson.GetBytes(eventData, "response.usage")
	if !usageNode.Exists() {
		return eventData
	}

	out := eventData
	out, _ = sjson.SetBytes(out, "response.usage.input_tokens", total.InputTokens)
	out, _ = sjson.SetBytes(out, "response.usage.output_tokens", total.OutputTokens)
	out, _ = sjson.SetBytes(out, "response.usage.total_tokens", total.TotalTokens)
	if usageNode.Get("input_tokens_details.cached_tokens").Exists() || total.CachedTokens != 0 {
		out, _ = sjson.SetBytes(out, "response.usage.input_tokens_details.cached_tokens", total.CachedTokens)
	}
	if usageNode.Get("prompt_tokens_details.cached_tokens").Exists() {
		out, _ = sjson.SetBytes(out, "response.usage.prompt_tokens_details.cached_tokens", total.CachedTokens)
	}
	if usageNode.Get("output_tokens_details.reasoning_tokens").Exists() || total.ReasoningTokens != 0 {
		out, _ = sjson.SetBytes(out, "response.usage.output_tokens_details.reasoning_tokens", total.ReasoningTokens)
	}
	if usageNode.Get("completion_tokens_details.reasoning_tokens").Exists() {
		out, _ = sjson.SetBytes(out, "response.usage.completion_tokens_details.reasoning_tokens", total.ReasoningTokens)
	}
	return out
}

func codexAbnormalReasoningRetryUsageFromMetadata(metadata map[string]any) (usage.Detail, bool) {
	if len(metadata) == 0 {
		return usage.Detail{}, false
	}
	raw := metadata[cliproxyexecutor.CodexAbnormalReasoningRetryUsageMetadataKey]
	switch detail := raw.(type) {
	case usage.Detail:
		return detail, hasCodexUsageDetail(detail)
	case *usage.Detail:
		if detail == nil {
			return usage.Detail{}, false
		}
		return *detail, hasCodexUsageDetail(*detail)
	case *cliproxyexecutor.UsageAccumulator:
		snapshot := detail.Snapshot()
		return snapshot, hasCodexUsageDetail(snapshot)
	default:
		return usage.Detail{}, false
	}
}

func addCodexUsageDetail(a, b usage.Detail) usage.Detail {
	a = normalizeCodexUsageDetail(a)
	b = normalizeCodexUsageDetail(b)
	return usage.Detail{
		InputTokens:         a.InputTokens + b.InputTokens,
		OutputTokens:        a.OutputTokens + b.OutputTokens,
		ReasoningTokens:     a.ReasoningTokens + b.ReasoningTokens,
		CachedTokens:        a.CachedTokens + b.CachedTokens,
		CacheReadTokens:     a.CacheReadTokens + b.CacheReadTokens,
		CacheCreationTokens: a.CacheCreationTokens + b.CacheCreationTokens,
		TotalTokens:         a.TotalTokens + b.TotalTokens,
	}
}

func normalizeCodexUsageDetail(detail usage.Detail) usage.Detail {
	if detail.TotalTokens == 0 {
		total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
		if total > 0 {
			detail.TotalTokens = total
		}
	}
	return detail
}

func hasCodexUsageDetail(detail usage.Detail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0
}
