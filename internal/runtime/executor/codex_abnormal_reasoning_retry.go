package executor

import (
	"bytes"
	"context"
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
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexAbnormalReasoningRetryPolicy struct {
	enabled                bool
	streamBuffer           bool
	streamBufferMax        int64
	maxRetries             int
	exhaustedBehavior      string
	clientUsageAggregation string
	hedgeEnabled           bool
	hedgeMode              string
	hedgeDelay             time.Duration
	requireDistinct        bool
	authID                 string
	modelContains          []string
	reasoningEfforts       map[string]struct{}
	reasoningTokens        map[int64]struct{}
}

type codexAbnormalReasoningRetryError struct {
	detail                  usage.Detail
	maxRetries              int
	exhaustedBehavior       string
	clientUsageAggregation  string
	hedgeEnabled            bool
	hedgeMode               string
	hedgeDelay              time.Duration
	requireDistinctAuth     bool
	authID                  string
	fallbackResponse        *cliproxyexecutor.Response
	fallbackStreamHeaders   http.Header
	fallbackStreamChunks    []cliproxyexecutor.StreamChunk
	fallbackStreamFinalizer cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer
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

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyHedgeMode() string {
	if e == nil || strings.TrimSpace(e.hedgeMode) == "" {
		return config.CodexAbnormalReasoningHedgedRetryModeQuality
	}
	return e.hedgeMode
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

func (e *codexAbnormalReasoningRetryError) RetryWithoutPenaltyFallbackStreamFinalizer() cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer {
	if e == nil {
		return nil
	}
	return e.fallbackStreamFinalizer
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
		enabled:                true,
		streamBuffer:           effective.StreamBuffer,
		streamBufferMax:        effective.StreamBufferMaxBytes,
		maxRetries:             effective.MaxRetries,
		exhaustedBehavior:      effective.ExhaustedBehavior,
		clientUsageAggregation: effective.ClientUsageAggregation,
		hedgeEnabled:           effective.HedgedRetry.Enabled,
		hedgeMode:              effective.HedgedRetry.Mode,
		hedgeDelay:             time.Duration(effective.HedgedRetry.HedgeDelayMS) * time.Millisecond,
		requireDistinct:        effective.HedgedRetry.RequireDistinctAuth,
		authID:                 strings.TrimSpace(auth.ID),
		modelContains:          modelContains,
		reasoningEfforts:       efforts,
		reasoningTokens:        tokens,
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
	return p.retryError(detail, reasoningEffort, nil, nil, nil, nil)
}

func (p codexAbnormalReasoningRetryPolicy) RetryErrorWithFallbackResponse(detail usage.Detail, reasoningEffort string, fallback cliproxyexecutor.Response) error {
	return p.retryError(detail, reasoningEffort, &fallback, nil, nil, nil)
}

func (p codexAbnormalReasoningRetryPolicy) RetryErrorWithFallbackStreamChunks(detail usage.Detail, reasoningEffort string, headers http.Header, chunks []cliproxyexecutor.StreamChunk) error {
	return p.retryError(detail, reasoningEffort, nil, headers, chunks, nil)
}

func (p codexAbnormalReasoningRetryPolicy) RetryErrorWithFallbackStreamChunksAndFinalizer(detail usage.Detail, reasoningEffort string, headers http.Header, chunks []cliproxyexecutor.StreamChunk, finalizer cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer) error {
	return p.retryError(detail, reasoningEffort, nil, headers, chunks, finalizer)
}

func (p codexAbnormalReasoningRetryPolicy) retryError(detail usage.Detail, reasoningEffort string, fallbackResponse *cliproxyexecutor.Response, fallbackStreamHeaders http.Header, fallbackStreamChunks []cliproxyexecutor.StreamChunk, fallbackStreamFinalizer cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer) error {
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
		detail:                 detail,
		maxRetries:             p.maxRetries,
		exhaustedBehavior:      p.exhaustedBehavior,
		clientUsageAggregation: p.clientUsageAggregation,
		hedgeEnabled:           p.hedgeEnabled,
		hedgeMode:              p.hedgeMode,
		hedgeDelay:             p.hedgeDelay,
		requireDistinctAuth:    p.requireDistinct,
		authID:                 p.authID,
	}
	if fallbackResponse != nil {
		resp := cloneCodexAbnormalReasoningRetryResponse(*fallbackResponse)
		err.fallbackResponse = &resp
	}
	if len(fallbackStreamChunks) > 0 {
		err.fallbackStreamHeaders = cloneCodexAbnormalReasoningRetryHeader(fallbackStreamHeaders)
		err.fallbackStreamChunks = cloneCodexAbnormalReasoningRetryStreamChunks(fallbackStreamChunks)
		err.fallbackStreamFinalizer = fallbackStreamFinalizer
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

func patchCodexAbnormalReasoningClientUsage(eventData []byte, metadata map[string]any, aggregation string) []byte {
	previous, ok := codexAbnormalReasoningRetryUsageSnapshotFromMetadata(metadata)
	if !ok {
		return eventData
	}
	return patchCodexAbnormalReasoningClientUsageWithSnapshot(eventData, previous, aggregation)
}

func patchCodexAbnormalReasoningClientUsageWithSnapshot(eventData []byte, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot, aggregation string) []byte {
	current, ok := helps.ParseCodexUsage(eventData)
	if !ok {
		return eventData
	}
	total := codexAbnormalReasoningRetryAggregateClientUsage(previous, current, aggregation)
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
	snapshot, ok := codexAbnormalReasoningRetryUsageSnapshotFromMetadata(metadata)
	if !ok {
		return usage.Detail{}, false
	}
	return snapshot.Detail, hasCodexUsageDetail(snapshot.Detail)
}

func codexAbnormalReasoningRetryUsageSnapshotFromMetadata(metadata map[string]any) (cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot, bool) {
	if len(metadata) == 0 {
		return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{}, false
	}
	raw := metadata[cliproxyexecutor.CodexAbnormalReasoningRetryUsageMetadataKey]
	switch detail := raw.(type) {
	case cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot:
		return detail, hasCodexUsageDetail(detail.Detail)
	case *cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot:
		if detail == nil {
			return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{}, false
		}
		return *detail, hasCodexUsageDetail(detail.Detail)
	case usage.Detail:
		if !hasCodexUsageDetail(detail) {
			return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{}, false
		}
		return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{
			Detail:             detail,
			FoldedOutputTokens: foldedCodexUsageOutputTokens(detail),
		}, true
	case *usage.Detail:
		if detail == nil {
			return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{}, false
		}
		if !hasCodexUsageDetail(*detail) {
			return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{}, false
		}
		return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{
			Detail:             *detail,
			FoldedOutputTokens: foldedCodexUsageOutputTokens(*detail),
		}, true
	case *cliproxyexecutor.UsageAccumulator:
		snapshot := detail.RetryWithoutPenaltySnapshot()
		return snapshot, hasCodexUsageDetail(snapshot.Detail)
	default:
		return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{}, false
	}
}

func codexAbnormalReasoningRetryAggregateClientUsage(previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot, current usage.Detail, aggregation string) usage.Detail {
	if aggregation == config.CodexAbnormalReasoningRetryClientUsageAggregationSum {
		return addCodexUsageDetail(previous.Detail, current)
	}
	previousDetail := normalizeCodexUsageDetail(previous.Detail)
	current = normalizeCodexUsageDetail(current)
	foldedPreviousOutput := previous.FoldedOutputTokens
	if foldedPreviousOutput == 0 && hasCodexUsageDetail(previousDetail) {
		foldedPreviousOutput = foldedCodexUsageOutputTokens(previousDetail)
	}
	deliveredOutput := current.OutputTokens
	if current.ReasoningTokens > deliveredOutput {
		deliveredOutput = current.ReasoningTokens
	}
	input := previousDetail.InputTokens + current.InputTokens
	output := deliveredOutput + foldedPreviousOutput
	return usage.Detail{
		InputTokens:         input,
		OutputTokens:        output,
		ReasoningTokens:     current.ReasoningTokens + foldedPreviousOutput,
		CachedTokens:        previousDetail.CachedTokens + current.CachedTokens,
		CacheReadTokens:     previousDetail.CacheReadTokens + current.CacheReadTokens,
		CacheCreationTokens: previousDetail.CacheCreationTokens + current.CacheCreationTokens,
		TotalTokens:         input + output,
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

func foldedCodexUsageOutputTokens(detail usage.Detail) int64 {
	detail = normalizeCodexUsageDetail(detail)
	if detail.OutputTokens >= detail.ReasoningTokens {
		return detail.OutputTokens
	}
	return detail.ReasoningTokens
}

func codexAbnormalReasoningRetryResponseMetadata(detail usage.Detail, finalizer cliproxyexecutor.RetryWithoutPenaltyResponseFinalizer) map[string]any {
	meta := map[string]any{
		cliproxyexecutor.RetryWithoutPenaltyUsageDetailMetadataKey: detail,
		cliproxyexecutor.RetryWithoutPenaltyHedgeScoreMetadataKey:  detail.OutputTokens,
	}
	if finalizer != nil {
		meta[cliproxyexecutor.RetryWithoutPenaltyResponseFinalizerMetadataKey] = finalizer
	}
	return meta
}

func codexAbnormalReasoningRetryStreamMetadata(streamUsage *cliproxyexecutor.RetryWithoutPenaltyStreamUsage, finalizer cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer) map[string]any {
	meta := map[string]any{}
	if streamUsage != nil {
		meta[cliproxyexecutor.RetryWithoutPenaltyStreamUsageMetadataKey] = streamUsage
	}
	if finalizer != nil {
		meta[cliproxyexecutor.RetryWithoutPenaltyStreamFinalizerMetadataKey] = finalizer
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// codexAbnormalReasoningRetryStreamRecorder captures the raw upstream SSE lines
// fed into stream translation so a quality-hedge finalizer can re-patch the
// completed usage with the final discarded-attempt snapshot and re-translate the
// whole stream. The completed event is kept in its usage-unpatched form; every
// other line is kept exactly as it entered translation.
type codexAbnormalReasoningRetryStreamRecorder struct {
	lines          [][]byte
	completedIndex int
	completedData  []byte
	recordedBytes  int64
	maxBytes       int64
	dropped        bool
}

func newCodexAbnormalReasoningRetryStreamRecorder(maxBytes int64) *codexAbnormalReasoningRetryStreamRecorder {
	return &codexAbnormalReasoningRetryStreamRecorder{completedIndex: -1, maxBytes: maxBytes}
}

func (r *codexAbnormalReasoningRetryStreamRecorder) recordLine(line []byte) {
	if r == nil || r.dropped || !r.reserve(int64(len(line))) {
		return
	}
	r.lines = append(r.lines, bytes.Clone(line))
}

func (r *codexAbnormalReasoningRetryStreamRecorder) recordCompleted(completedData []byte) {
	if r == nil || r.dropped || !r.reserve(int64(len(completedData))) {
		return
	}
	r.completedData = bytes.Clone(completedData)
	r.completedIndex = len(r.lines)
	r.lines = append(r.lines, nil)
}

func (r *codexAbnormalReasoningRetryStreamRecorder) reserve(size int64) bool {
	if r.maxBytes > 0 && r.recordedBytes+size > r.maxBytes {
		r.dropped = true
		r.lines = nil
		r.completedData = nil
		r.completedIndex = -1
		return false
	}
	r.recordedBytes += size
	return true
}

func (r *codexAbnormalReasoningRetryStreamRecorder) ready() bool {
	return r != nil && !r.dropped && r.completedIndex >= 0
}

// finalizeCodexAbnormalReasoningRetryStreamFromRaw rebuilds the winning stream
// from recorded raw upstream lines, mirroring the non-stream finalizer: the
// completed event usage is re-patched with the final discarded-attempt snapshot
// before the whole stream is re-translated, so the folded usage survives
// translation into any downstream response format. Returns nil when no usable
// recording exists and the caller must fall back to patching translated chunks.
func finalizeCodexAbnormalReasoningRetryStreamFromRaw(ctx context.Context, to, responseFormat sdktranslator.Format, model string, originalPayload, body []byte, identityState codexIdentityConfuseState, recorder *codexAbnormalReasoningRetryStreamRecorder, headers http.Header, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot, aggregation string) *cliproxyexecutor.StreamResult {
	if !recorder.ready() {
		return nil
	}
	var param any
	var patched []cliproxyexecutor.StreamChunk
	for i := range recorder.lines {
		line := recorder.lines[i]
		if i == recorder.completedIndex {
			data := patchCodexAbnormalReasoningClientUsageWithSnapshot(recorder.completedData, previous, aggregation)
			line = append([]byte("data: "), data...)
			line = applyCodexIdentityExposeResponsePayload(line, identityState)
		}
		chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, model, originalPayload, body, line, &param)
		for j := range chunks {
			patched = append(patched, cliproxyexecutor.StreamChunk{Payload: bytes.Clone(chunks[j])})
		}
	}
	out := make(chan cliproxyexecutor.StreamChunk, len(patched))
	for i := range patched {
		out <- patched[i]
	}
	close(out)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneCodexAbnormalReasoningRetryHeader(headers),
		Chunks:  out,
	}
}

func finalizeCodexAbnormalReasoningRetryStream(headers http.Header, chunks []cliproxyexecutor.StreamChunk, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot, aggregation string) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for i := range chunks {
		chunk := chunks[i]
		if chunk.Err == nil && len(chunk.Payload) > 0 {
			chunk.Payload = patchCodexAbnormalReasoningStreamPayload(chunk.Payload, previous, aggregation)
		}
		if len(chunk.Payload) > 0 {
			chunk.Payload = bytes.Clone(chunk.Payload)
		}
		out <- chunk
	}
	close(out)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneCodexAbnormalReasoningRetryHeader(headers),
		Chunks:  out,
	}
}

func patchCodexAbnormalReasoningStreamPayload(payload []byte, previous cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot, aggregation string) []byte {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return payload
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		data := bytes.TrimSpace(trimmed[len("data:"):])
		if !gjson.GetBytes(data, "response.usage").Exists() {
			return payload
		}
		patched := patchCodexAbnormalReasoningClientUsageWithSnapshot(data, previous, aggregation)
		return append([]byte("data: "), patched...)
	}
	if !gjson.GetBytes(trimmed, "response.usage").Exists() {
		return payload
	}
	return patchCodexAbnormalReasoningClientUsageWithSnapshot(trimmed, previous, aggregation)
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
