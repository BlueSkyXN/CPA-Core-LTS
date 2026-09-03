// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

var statisticsEnabled atomic.Bool

const CanonicalExportVersion = 3

const usageTimingVersion = uint32(1)

var (
	ErrInvalidLegacyTokenStats     = errors.New("invalid legacy usage token contract")
	ErrAmbiguousLegacyTokenStats   = errors.New("ambiguous legacy usage token contract: cached version 1 details require uncached_input_tokens")
	ErrInvalidCanonicalTokenStats  = errors.New("invalid canonical usage token contract")
	ErrInvalidCanonicalTimingStats = errors.New("invalid canonical usage timing contract")
	ErrUsageAggregateOverflow      = errors.New("usage aggregate overflow")
)

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// LoggerPlugin collects in-memory request statistics for usage analysis.
// It implements coreusage.Plugin to receive usage records emitted by the runtime.
type LoggerPlugin struct {
	stats *RequestStatistics
}

// NewLoggerPlugin constructs a new logger plugin instance.
//
// Returns:
//   - *LoggerPlugin: A new logger plugin instance wired to the shared statistics store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage implements coreusage.Plugin.
// It updates the in-memory statistics store whenever a usage record is received.
//
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to aggregate
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.stats == nil {
		return
	}
	p.stats.Record(ctx, record)
}

// SetStatisticsEnabled toggles whether in-memory statistics are recorded.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports the current recording state.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64
}

// apiStats holds aggregated metrics for a single API key.
type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

// RequestDetail stores request-level metadata and token usage for a single request.
type RequestDetail struct {
	Timestamp time.Time `json:"timestamp"`
	LatencyMs int64     `json:"latency_ms"`
	// TimingVersion identifies the semantic timing contract used by the
	// optional timing fields below. Zero is retained for migrated legacy rows.
	TimingVersion uint32 `json:"timing_version,omitempty"`
	// TTFBMs records the first upstream response byte/payload latency.
	TTFBMs int64 `json:"ttfb_ms,omitempty"`
	// TTFTMs records the first non-empty reasoning content latency.
	TTFTMs int64 `json:"ttft_ms,omitempty"`
	// TTFAMs records the first non-empty assistant text latency.
	TTFAMs               int64      `json:"ttfa_ms,omitempty"`
	Source               string     `json:"source"`
	UsageProvenance      string     `json:"usage_provenance,omitempty"`
	AuthIndex            string     `json:"auth_index"`
	Alias                string     `json:"alias,omitempty"`
	ReasoningEffort      string     `json:"reasoning_effort,omitempty"`
	ServiceTier          string     `json:"service_tier,omitempty"`
	RequestServiceTier   string     `json:"request_service_tier,omitempty"`
	OutboundServiceTier  string     `json:"outbound_service_tier,omitempty"`
	ResponseServiceTier  string     `json:"response_service_tier,omitempty"`
	EffectiveServiceTier string     `json:"effective_service_tier,omitempty"`
	Tokens               TokenStats `json:"tokens"`
	Failed               bool       `json:"failed"`
	Generate             bool       `json:"generate"`
	FailureReason        string     `json:"failure_reason,omitempty"`
	FailureStatus        int        `json:"failure_status,omitempty"`

	timingFieldsPresent timingFieldPresence
}

type timingFieldPresence uint8

const (
	timingVersionPresent timingFieldPresence = 1 << iota
	timingTTFBPresent
	timingTTFTPresent
	timingTTFAPresent
)

// MarshalJSON preserves explicit zero timing values. The scalar fields remain
// source-compatible for existing callers, while the private presence mask
// keeps an omitted field distinct from a measured zero after import/export.
func (d RequestDetail) MarshalJSON() ([]byte, error) {
	type requestDetailAlias RequestDetail
	encoded, err := json.Marshal(requestDetailAlias(d))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	setField := func(name string, value any, present bool) error {
		if !present {
			return nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		fields[name] = raw
		return nil
	}
	if err := setField("timing_version", d.TimingVersion, d.timingFieldPresent(timingVersionPresent)); err != nil {
		return nil, err
	}
	if err := setField("ttfb_ms", d.TTFBMs, d.timingFieldPresent(timingTTFBPresent)); err != nil {
		return nil, err
	}
	if err := setField("ttft_ms", d.TTFTMs, d.timingFieldPresent(timingTTFTPresent)); err != nil {
		return nil, err
	}
	if err := setField("ttfa_ms", d.TTFAMs, d.timingFieldPresent(timingTTFAPresent)); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// UnmarshalJSON keeps legacy usage exports compatible with the generate field.
// Missing or null values mean generation was enabled; only an explicit false disables it.
func (d *RequestDetail) UnmarshalJSON(data []byte) error {
	type requestDetailAlias RequestDetail
	var decoded requestDetailAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	generate := true
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if rawGenerate, ok := fields["generate"]; ok && strings.TrimSpace(string(rawGenerate)) != "null" {
		if err := json.Unmarshal(rawGenerate, &generate); err != nil {
			return err
		}
	}
	for _, field := range []string{"timing_version", "ttfb_ms", "ttft_ms", "ttfa_ms"} {
		if rawValue, ok := fields[field]; ok && strings.EqualFold(strings.TrimSpace(string(rawValue)), "null") {
			return fmt.Errorf("%w: %s must be an integer", ErrInvalidCanonicalTimingStats, field)
		}
	}

	*d = RequestDetail(decoded)
	d.Generate = generate
	var timingFields timingFieldPresence
	if _, ok := fields["timing_version"]; ok {
		timingFields |= timingVersionPresent
	}
	if _, ok := fields["ttfb_ms"]; ok {
		timingFields |= timingTTFBPresent
	}
	if _, ok := fields["ttft_ms"]; ok {
		timingFields |= timingTTFTPresent
	}
	if _, ok := fields["ttfa_ms"]; ok {
		timingFields |= timingTTFAPresent
	}
	d.timingFieldsPresent = timingFields
	return nil
}

func (d RequestDetail) timingFieldPresent(field timingFieldPresence) bool {
	if d.timingFieldsPresent != 0 {
		return d.timingFieldsPresent&field != 0
	}
	switch field {
	case timingVersionPresent:
		return d.TimingVersion != 0
	case timingTTFBPresent:
		return d.TTFBMs != 0
	case timingTTFTPresent:
		return d.TTFTMs != 0
	case timingTTFAPresent:
		return d.TTFAMs != 0
	default:
		return false
	}
}

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	TotalTokens         int64 `json:"total_tokens"`

	fieldsPresent                  tokenStatsFieldPresence
	legacyUncachedInputTokens      *int64
	legacyUncachedInputTokensFound bool
}

type tokenStatsFieldPresence uint16

const (
	tokenStatsInputPresent tokenStatsFieldPresence = 1 << iota
	tokenStatsOutputPresent
	tokenStatsReasoningPresent
	tokenStatsCachedPresent
	tokenStatsCacheReadPresent
	tokenStatsCacheCreationPresent
	tokenStatsTotalPresent
)

const tokenStatsRequiredExportFields = tokenStatsInputPresent |
	tokenStatsOutputPresent |
	tokenStatsReasoningPresent |
	tokenStatsCachedPresent |
	tokenStatsTotalPresent

const tokenStatsRequiredV1Fields = tokenStatsInputPresent |
	tokenStatsOutputPresent |
	tokenStatsTotalPresent

var tokenStatsJSONFields = []struct {
	name string
	bit  tokenStatsFieldPresence
}{
	{name: "input_tokens", bit: tokenStatsInputPresent},
	{name: "output_tokens", bit: tokenStatsOutputPresent},
	{name: "reasoning_tokens", bit: tokenStatsReasoningPresent},
	{name: "cached_tokens", bit: tokenStatsCachedPresent},
	{name: "cache_read_tokens", bit: tokenStatsCacheReadPresent},
	{name: "cache_creation_tokens", bit: tokenStatsCacheCreationPresent},
	{name: "total_tokens", bit: tokenStatsTotalPresent},
}

// UnmarshalJSON retains field presence and the released v1 migration marker.
// Import validation uses the private presence mask to distinguish omitted
// fields from explicit zero without changing the exported JSON schema.
func (tokens *TokenStats) UnmarshalJSON(data []byte) error {
	type tokenStatsAlias TokenStats
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	var decoded tokenStatsAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: token fields must be integers", ErrInvalidCanonicalTokenStats)
	}

	presence := tokenStatsFieldPresence(0)
	for _, field := range tokenStatsJSONFields {
		rawValue, exists := fields[field.name]
		if !exists {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(string(rawValue)), "null") {
			return fmt.Errorf("%w: %s must be an integer", ErrInvalidCanonicalTokenStats, field.name)
		}
		presence |= field.bit
	}

	decodedTokens := TokenStats(decoded)
	decodedTokens.fieldsPresent = presence
	if rawUncached, exists := fields["uncached_input_tokens"]; exists {
		decodedTokens.legacyUncachedInputTokensFound = true
		if strings.EqualFold(strings.TrimSpace(string(rawUncached)), "null") {
			return fmt.Errorf("%w: uncached_input_tokens must be an integer", ErrInvalidLegacyTokenStats)
		}
		var uncachedInputTokens int64
		if err := json.Unmarshal(rawUncached, &uncachedInputTokens); err != nil {
			return fmt.Errorf("%w: uncached_input_tokens must be an integer", ErrInvalidLegacyTokenStats)
		}
		decodedTokens.legacyUncachedInputTokens = &uncachedInputTokens
	}
	*tokens = decodedTokens
	return nil
}

func (tokens TokenStats) hasRequiredExportFields() bool {
	return tokens.fieldsPresent&tokenStatsRequiredExportFields == tokenStatsRequiredExportFields
}

func (tokens TokenStats) hasRequiredV1Fields() bool {
	return tokens.fieldsPresent&tokenStatsRequiredV1Fields == tokenStatsRequiredV1Fields
}

// MigrateV1TokenStats converts released version 1 details into the canonical
// version 2 total-input contract. Markerless details are safe only when all
// cache categories are zero; cached markerless details remain ambiguous across
// the released OpenAI-inclusive and Claude-uncached input semantics.
func (snapshot *StatisticsSnapshot) MigrateV1TokenStats() error {
	if snapshot == nil {
		return nil
	}
	for apiName, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for detailIndex := range modelSnapshot.Details {
				if err := migrateV1TokenStats(&modelSnapshot.Details[detailIndex].Tokens); err != nil {
					return err
				}
			}
			apiSnapshot.Models[modelName] = modelSnapshot
		}
		snapshot.APIs[apiName] = apiSnapshot
	}
	return nil
}

func migrateV1TokenStats(tokens *TokenStats) error {
	if tokens == nil || !tokens.hasRequiredV1Fields() || !validLegacyV1TokenStats(*tokens) {
		return ErrInvalidLegacyTokenStats
	}

	if tokens.legacyUncachedInputTokens == nil {
		if tokens.legacyUncachedInputTokensFound {
			return ErrInvalidLegacyTokenStats
		}
		if tokens.CachedTokens != 0 || tokens.CacheReadTokens != 0 || tokens.CacheCreationTokens != 0 {
			return ErrAmbiguousLegacyTokenStats
		}
		return nil
	}

	uncachedInputTokens := *tokens.legacyUncachedInputTokens
	if uncachedInputTokens < 0 || uncachedInputTokens > tokens.InputTokens {
		return ErrInvalidLegacyTokenStats
	}
	cacheReadTokens := tokens.CacheReadTokens
	legacyCacheCreationAlias := tokens.CacheCreationTokens > 0 &&
		cacheReadTokens == 0 &&
		tokens.CachedTokens == tokens.CacheCreationTokens
	if cacheReadTokens == 0 && tokens.CachedTokens > 0 && !legacyCacheCreationAlias {
		cacheReadTokens = tokens.CachedTokens
	}
	canonicalInputTokens, ok := sumNonNegativeTokenCounts(
		uncachedInputTokens,
		cacheReadTokens,
		tokens.CacheCreationTokens,
	)
	if !ok {
		return ErrInvalidLegacyTokenStats
	}
	tokens.InputTokens = canonicalInputTokens
	tokens.CacheReadTokens = cacheReadTokens
	if legacyCacheCreationAlias {
		tokens.CachedTokens = 0
	} else {
		tokens.CachedTokens = cacheReadTokens
	}
	tokens.legacyUncachedInputTokens = nil
	tokens.legacyUncachedInputTokensFound = false
	return nil
}

// HasLegacyUncachedInputTokens reports whether a canonical payload still
// carries the version 1-only migration field.
func (snapshot StatisticsSnapshot) HasLegacyUncachedInputTokens() bool {
	for _, apiSnapshot := range snapshot.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if detail.Tokens.legacyUncachedInputTokensFound {
					return true
				}
			}
		}
	}
	return false
}

// ValidateCanonicalV2TokenStats requires every non-omitempty export field and
// validates the canonical token relationships for every imported detail.
func (snapshot StatisticsSnapshot) ValidateCanonicalV2TokenStats() error {
	for _, apiSnapshot := range snapshot.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if !detail.Tokens.hasRequiredExportFields() || !validCanonicalTokenStats(detail.Tokens) {
					return ErrInvalidCanonicalTokenStats
				}
			}
		}
	}
	return nil
}

// ValidateCanonicalV3TokenStats keeps the canonical token contract explicit
// at the schema version boundary. The token relationships remain unchanged
// from v2; only the surrounding timing contract is new in v3.
func (snapshot StatisticsSnapshot) ValidateCanonicalV3TokenStats() error {
	return snapshot.ValidateCanonicalV2TokenStats()
}

// ValidateCanonicalTimingStats validates the optional v3 timing fields. A
// zero timing version is reserved for migrated legacy details that may retain
// only an explicitly named TTFB value.
func (snapshot StatisticsSnapshot) ValidateCanonicalTimingStats() error {
	for _, apiSnapshot := range snapshot.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if !validCanonicalTimingDetail(detail) {
					return ErrInvalidCanonicalTimingStats
				}
			}
		}
	}
	return nil
}

func validCanonicalTimingDetail(detail RequestDetail) bool {
	if detail.TimingVersion != 0 && detail.TimingVersion != usageTimingVersion {
		return false
	}
	hasTimingVersion := detail.timingFieldPresent(timingVersionPresent)
	hasTTFB := detail.timingFieldPresent(timingTTFBPresent)
	hasTTFT := detail.timingFieldPresent(timingTTFTPresent)
	hasTTFA := detail.timingFieldPresent(timingTTFAPresent)
	if hasTimingVersion && detail.TimingVersion == 0 {
		return false
	}
	if (hasTTFT || hasTTFA) && !hasTTFB {
		return false
	}
	if detail.LatencyMs < 0 || detail.TTFBMs < 0 || detail.TTFTMs < 0 || detail.TTFAMs < 0 {
		return false
	}
	if detail.TTFBMs > detail.LatencyMs || detail.TTFTMs > detail.LatencyMs || detail.TTFAMs > detail.LatencyMs {
		return false
	}
	if (hasTTFT && detail.TTFTMs < detail.TTFBMs) || (hasTTFA && detail.TTFAMs < detail.TTFBMs) {
		return false
	}
	if detail.TimingVersion == 0 && (hasTTFT || hasTTFA) {
		return false
	}
	return true
}

// ValidateCanonicalTokenStats validates migrated v1 values after their
// version-specific presence and ambiguity checks have completed.
func (snapshot StatisticsSnapshot) ValidateCanonicalTokenStats() error {
	for _, apiSnapshot := range snapshot.APIs {
		for _, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if !validCanonicalTokenStats(detail.Tokens) {
					return ErrInvalidCanonicalTokenStats
				}
			}
		}
	}
	return nil
}

func validCanonicalTokenStats(tokens TokenStats) bool {
	if !validLegacyV1TokenStats(tokens) || tokens.CachedTokens != tokens.CacheReadTokens {
		return false
	}
	cacheInputTokens, ok := sumNonNegativeTokenCounts(tokens.CacheReadTokens, tokens.CacheCreationTokens)
	if !ok || cacheInputTokens > tokens.InputTokens {
		return false
	}
	minimumTotalTokens, ok := sumNonNegativeTokenCounts(tokens.InputTokens, tokens.OutputTokens)
	return ok && minimumTotalTokens <= tokens.TotalTokens
}

func validLegacyV1TokenStats(tokens TokenStats) bool {
	if tokens.InputTokens < 0 ||
		tokens.OutputTokens < 0 ||
		tokens.ReasoningTokens < 0 ||
		tokens.CachedTokens < 0 ||
		tokens.CacheReadTokens < 0 ||
		tokens.CacheCreationTokens < 0 ||
		tokens.TotalTokens < 0 {
		return false
	}
	return tokens.TotalTokens != 0 ||
		(tokens.InputTokens == 0 && tokens.OutputTokens == 0 && tokens.ReasoningTokens == 0 &&
			tokens.CachedTokens == 0 && tokens.CacheReadTokens == 0 && tokens.CacheCreationTokens == 0)
}

// StatisticsSnapshot represents an immutable view of the aggregated metrics.
type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

// APISnapshot summarises metrics for a single API key.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
	}
}

// Record ingests a new usage record and updates the aggregates.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	if !statisticsEnabled.Load() {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := normaliseDetail(record.Provider, record.Detail)
	statsKey := record.APIKey
	if statsKey == "" {
		statsKey = resolveAPIIdentifier(ctx, record)
	}
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	failureReason, failureStatus := safeFailureDetail(record, failed)
	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}
	requestServiceTier := strings.TrimSpace(record.RequestServiceTier)
	if requestServiceTier == "" {
		requestServiceTier = strings.TrimSpace(record.ServiceTier)
	}
	outboundServiceTier := strings.TrimSpace(record.OutboundServiceTier)
	responseServiceTier := strings.TrimSpace(record.ResponseServiceTier)
	if responseServiceTier == "" {
		responseServiceTier = strings.TrimSpace(record.Detail.ResponseServiceTier)
	}
	effectiveServiceTier := coreusage.CanonicalEffectiveServiceTier(record.EffectiveServiceTier)
	requestDetail := RequestDetail{
		Timestamp:            timestamp,
		LatencyMs:            normaliseLatency(record.Latency),
		Source:               record.Source,
		UsageProvenance:      coreusage.CanonicalUsageProvenance(record.UsageProvenance),
		AuthIndex:            record.AuthIndex,
		Alias:                strings.TrimSpace(record.Alias),
		ReasoningEffort:      strings.TrimSpace(record.ReasoningEffort),
		ServiceTier:          requestServiceTier,
		RequestServiceTier:   requestServiceTier,
		OutboundServiceTier:  outboundServiceTier,
		ResponseServiceTier:  responseServiceTier,
		EffectiveServiceTier: effectiveServiceTier,
		Tokens:               detail,
		Failed:               failed,
		Generate:             coreusage.GenerateEnabled(record.Generate),
		FailureReason:        failureReason,
		FailureStatus:        failureStatus,
	}
	if record.TimingVersion == usageTimingVersion {
		requestDetail.TimingVersion = usageTimingVersion
		requestDetail.timingFieldsPresent |= timingVersionPresent
		requestDetail.TTFBMs = normaliseLatency(record.TTFB)
		requestDetail.TTFTMs = normaliseLatency(record.TTFT)
		requestDetail.TTFAMs = normaliseLatency(record.TTFA)
		if record.TTFB > 0 {
			requestDetail.timingFieldsPresent |= timingTTFBPresent
		}
		if record.TTFT > 0 {
			requestDetail.timingFieldsPresent |= timingTTFTPresent
		}
		if record.TTFA > 0 {
			requestDetail.timingFieldsPresent |= timingTTFAPresent
		}
		if !validCanonicalTimingDetail(requestDetail) {
			// A malformed optional timing sample must not drop the request
			// accounting record. Keep the usage detail and fail closed on timing.
			requestDetail.TimingVersion = 0
			requestDetail.TTFBMs = 0
			requestDetail.TTFTMs = 0
			requestDetail.TTFAMs = 0
			requestDetail.timingFieldsPresent = 0
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := mergeCandidate{apiName: statsKey, modelName: modelName, detail: requestDetail}
	if err := s.validateMergeCandidates([]mergeCandidate{candidate}); err != nil {
		candidate.detail.Tokens = TokenStats{}
		if err = s.validateMergeCandidates([]mergeCandidate{candidate}); err != nil {
			return
		}
	}

	stats, ok := s.apis[statsKey]
	if !ok || stats == nil {
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = stats
	} else if stats.Models == nil {
		stats.Models = make(map[string]*modelStats)
	}
	s.recordImported(statsKey, modelName, stats, candidate.detail)
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	stats.TotalRequests++
	stats.TotalTokens = addNonNegativeTokenCounts(stats.TotalTokens, detail.Tokens.TotalTokens)
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens = addNonNegativeTokenCounts(modelStatsValue.TotalTokens, detail.Tokens.TotalTokens)
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
}

// Snapshot returns a copy of the aggregated metrics for external consumption.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
			for i := range modelStatsValue.Details {
				requestDetails[i] = cloneRequestDetail(modelStatsValue.Details[i])
			}
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelStatsValue.TotalRequests,
				TotalTokens:   modelStatsValue.TotalTokens,
				Details:       requestDetails,
			}
		}
		result.APIs[apiName] = apiSnapshot
	}

	result.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		result.RequestsByDay[k] = v
	}

	result.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		key := formatHour(hour)
		result.RequestsByHour[key] = v
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		key := formatHour(hour)
		result.TokensByHour[key] = v
	}

	return result
}

type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

type mergeCandidate struct {
	apiName   string
	modelName string
	detail    RequestDetail
}

type mergeAggregateDelta struct {
	requests int64
	tokens   int64
}

type mergeModelKey struct {
	apiName   string
	modelName string
}

// MergeSnapshot merges an exported statistics snapshot into the current store.
// Existing data is preserved, duplicate request details are skipped, and every
// aggregate is checked before any candidate is committed.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) (MergeResult, error) {
	result := MergeResult{}
	if s == nil {
		return result, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				seen[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}

	candidates := make([]mergeCandidate, 0)
	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			for _, detail := range modelSnapshot.Details {
				detail.Tokens = normaliseTokenStats(detail.Tokens)
				detail = normaliseServiceTierAliases(detail)
				detail.UsageProvenance = coreusage.CanonicalUsageProvenance(detail.UsageProvenance)
				if !validCanonicalTimingDetail(detail) {
					return MergeResult{}, ErrInvalidCanonicalTimingStats
				}
				key := dedupKey(apiName, modelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}
				candidates = append(candidates, mergeCandidate{apiName: apiName, modelName: modelName, detail: detail})
			}
		}
	}

	if err := s.validateMergeCandidates(candidates); err != nil {
		return MergeResult{}, err
	}
	for _, candidate := range candidates {
		stats, ok := s.apis[candidate.apiName]
		if !ok || stats == nil {
			stats = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[candidate.apiName] = stats
		} else if stats.Models == nil {
			stats.Models = make(map[string]*modelStats)
		}
		s.recordImported(candidate.apiName, candidate.modelName, stats, candidate.detail)
		result.Added++
	}

	return result, nil
}

func (s *RequestStatistics) validateMergeCandidates(candidates []mergeCandidate) error {
	global := mergeAggregateDelta{}
	var successes int64
	var failures int64
	apiDeltas := make(map[string]mergeAggregateDelta)
	modelDeltas := make(map[mergeModelKey]mergeAggregateDelta)
	requestsByDay := make(map[string]int64)
	requestsByHour := make(map[int]int64)
	tokensByDay := make(map[string]int64)
	tokensByHour := make(map[int]int64)

	for _, candidate := range candidates {
		var ok bool
		global, ok = addMergeAggregateDelta(global, candidate.detail.Tokens.TotalTokens)
		if !ok {
			return ErrUsageAggregateOverflow
		}
		if candidate.detail.Failed {
			failures, ok = sumNonNegativeTokenCounts(failures, 1)
		} else {
			successes, ok = sumNonNegativeTokenCounts(successes, 1)
		}
		if !ok {
			return ErrUsageAggregateOverflow
		}

		apiDelta := apiDeltas[candidate.apiName]
		apiDelta, ok = addMergeAggregateDelta(apiDelta, candidate.detail.Tokens.TotalTokens)
		if !ok {
			return ErrUsageAggregateOverflow
		}
		apiDeltas[candidate.apiName] = apiDelta

		modelKey := mergeModelKey{apiName: candidate.apiName, modelName: candidate.modelName}
		modelDelta := modelDeltas[modelKey]
		modelDelta, ok = addMergeAggregateDelta(modelDelta, candidate.detail.Tokens.TotalTokens)
		if !ok {
			return ErrUsageAggregateOverflow
		}
		modelDeltas[modelKey] = modelDelta

		dayKey := candidate.detail.Timestamp.Format("2006-01-02")
		hourKey := candidate.detail.Timestamp.Hour()
		requestsByDay[dayKey], ok = sumNonNegativeTokenCounts(requestsByDay[dayKey], 1)
		if !ok {
			return ErrUsageAggregateOverflow
		}
		requestsByHour[hourKey], ok = sumNonNegativeTokenCounts(requestsByHour[hourKey], 1)
		if !ok {
			return ErrUsageAggregateOverflow
		}
		tokensByDay[dayKey], ok = sumNonNegativeTokenCounts(tokensByDay[dayKey], candidate.detail.Tokens.TotalTokens)
		if !ok {
			return ErrUsageAggregateOverflow
		}
		tokensByHour[hourKey], ok = sumNonNegativeTokenCounts(tokensByHour[hourKey], candidate.detail.Tokens.TotalTokens)
		if !ok {
			return ErrUsageAggregateOverflow
		}
	}

	if !mergeAggregateFits(s.totalRequests, global.requests) ||
		!mergeAggregateFits(s.successCount, successes) ||
		!mergeAggregateFits(s.failureCount, failures) ||
		!mergeAggregateFits(s.totalTokens, global.tokens) {
		return ErrUsageAggregateOverflow
	}
	for apiName, delta := range apiDeltas {
		var currentRequests, currentTokens int64
		if stats := s.apis[apiName]; stats != nil {
			currentRequests = stats.TotalRequests
			currentTokens = stats.TotalTokens
		}
		if !mergeAggregateFits(currentRequests, delta.requests) || !mergeAggregateFits(currentTokens, delta.tokens) {
			return ErrUsageAggregateOverflow
		}
	}
	for key, delta := range modelDeltas {
		var currentRequests, currentTokens int64
		if stats := s.apis[key.apiName]; stats != nil && stats.Models != nil {
			if model := stats.Models[key.modelName]; model != nil {
				currentRequests = model.TotalRequests
				currentTokens = model.TotalTokens
			}
		}
		if !mergeAggregateFits(currentRequests, delta.requests) || !mergeAggregateFits(currentTokens, delta.tokens) {
			return ErrUsageAggregateOverflow
		}
	}
	for day, delta := range requestsByDay {
		if !mergeAggregateFits(s.requestsByDay[day], delta) {
			return ErrUsageAggregateOverflow
		}
	}
	for hour, delta := range requestsByHour {
		if !mergeAggregateFits(s.requestsByHour[hour], delta) {
			return ErrUsageAggregateOverflow
		}
	}
	for day, delta := range tokensByDay {
		if !mergeAggregateFits(s.tokensByDay[day], delta) {
			return ErrUsageAggregateOverflow
		}
	}
	for hour, delta := range tokensByHour {
		if !mergeAggregateFits(s.tokensByHour[hour], delta) {
			return ErrUsageAggregateOverflow
		}
	}
	return nil
}

func addMergeAggregateDelta(delta mergeAggregateDelta, tokens int64) (mergeAggregateDelta, bool) {
	requests, ok := sumNonNegativeTokenCounts(delta.requests, 1)
	if !ok {
		return mergeAggregateDelta{}, false
	}
	totalTokens, ok := sumNonNegativeTokenCounts(delta.tokens, tokens)
	if !ok {
		return mergeAggregateDelta{}, false
	}
	return mergeAggregateDelta{requests: requests, tokens: totalTokens}, true
}

func mergeAggregateFits(current, delta int64) bool {
	_, ok := sumNonNegativeTokenCounts(current, delta)
	return ok
}

func normaliseServiceTierAliases(detail RequestDetail) RequestDetail {
	if strings.TrimSpace(detail.RequestServiceTier) == "" && strings.TrimSpace(detail.ServiceTier) != "" {
		detail.RequestServiceTier = detail.ServiceTier
	}
	if strings.TrimSpace(detail.ServiceTier) == "" && strings.TrimSpace(detail.RequestServiceTier) != "" {
		detail.ServiceTier = detail.RequestServiceTier
	}
	detail.OutboundServiceTier = strings.TrimSpace(detail.OutboundServiceTier)
	detail.EffectiveServiceTier = coreusage.CanonicalEffectiveServiceTier(detail.EffectiveServiceTier)
	return detail
}

func (s *RequestStatistics) recordImported(apiName, modelName string, stats *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens = addNonNegativeTokenCounts(s.totalTokens, totalTokens)

	s.updateAPIStats(stats, modelName, detail)

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] = addNonNegativeTokenCounts(s.tokensByDay[dayKey], totalTokens)
	s.tokensByHour[hourKey] = addNonNegativeTokenCounts(s.tokensByHour[hourKey], totalTokens)
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := detail.Tokens

	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%t|%s|%d|%d|%d|%d|%d|%d|%d|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.AuthIndex,
		detail.Failed,
		detail.FailureReason,
		detail.FailureStatus,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.CacheReadTokens,
		tokens.CacheCreationTokens,
		tokens.TotalTokens,
	)
}

func safeFailureDetail(record coreusage.Record, failed bool) (string, int) {
	if !failed {
		return "", 0
	}
	status := record.Fail.StatusCode
	if status < 0 {
		status = 0
	}
	return extractSafeFailureReason(record.Fail.Body), status
}

func extractSafeFailureReason(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	candidate := body
	if idx := strings.Index(candidate, ":"); idx > 0 {
		candidate = candidate[:idx]
	} else if fields := strings.Fields(candidate); len(fields) > 0 {
		candidate = fields[0]
	}
	candidate = strings.TrimSpace(candidate)
	if !isSafeFailureReason(candidate) {
		return ""
	}
	return candidate
}

func isSafeFailureReason(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '_' ||
			ch == '-' ||
			ch == '.' {
			continue
		}
		return false
	}
	return true
}

func resolveAPIIdentifier(ctx context.Context, record coreusage.Record) string {
	if ctx != nil {
		if endpoint := strings.TrimSpace(internallogging.GetEndpoint(ctx)); endpoint != "" {
			return endpoint
		}
	}
	if record.Provider != "" {
		return record.Provider
	}
	return "unknown"
}

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

const httpStatusBadRequest = 400

func normaliseDetail(provider string, detail coreusage.Detail) TokenStats {
	return normaliseTokenStatsForProvider(provider, TokenStats{
		InputTokens:         nonNegativeTokenCount(detail.InputTokens),
		OutputTokens:        nonNegativeTokenCount(detail.OutputTokens),
		ReasoningTokens:     nonNegativeTokenCount(detail.ReasoningTokens),
		CachedTokens:        nonNegativeTokenCount(detail.CachedTokens),
		CacheReadTokens:     nonNegativeTokenCount(detail.CacheReadTokens),
		CacheCreationTokens: nonNegativeTokenCount(detail.CacheCreationTokens),
		TotalTokens:         nonNegativeTokenCount(detail.TotalTokens),
	})
}

func normaliseTokenStats(tokens TokenStats) TokenStats {
	return normaliseTokenStatsForProvider("", tokens)
}

func normaliseTokenStatsForProvider(provider string, tokens TokenStats) TokenStats {
	tokens.InputTokens = nonNegativeTokenCount(tokens.InputTokens)
	tokens.OutputTokens = nonNegativeTokenCount(tokens.OutputTokens)
	tokens.ReasoningTokens = nonNegativeTokenCount(tokens.ReasoningTokens)
	tokens.CachedTokens = nonNegativeTokenCount(tokens.CachedTokens)
	tokens.CacheReadTokens = nonNegativeTokenCount(tokens.CacheReadTokens)
	tokens.CacheCreationTokens = nonNegativeTokenCount(tokens.CacheCreationTokens)
	tokens.TotalTokens = nonNegativeTokenCount(tokens.TotalTokens)
	if tokens.CacheReadTokens == 0 && tokens.CachedTokens != 0 {
		tokens.CacheReadTokens = tokens.CachedTokens
	}
	tokens.CachedTokens = tokens.CacheReadTokens
	cacheInputTokens, ok := sumNonNegativeTokenCounts(tokens.CacheReadTokens, tokens.CacheCreationTokens)
	if !ok || cacheInputTokens > tokens.InputTokens {
		tokens.CachedTokens = 0
		tokens.CacheReadTokens = 0
		tokens.CacheCreationTokens = 0
	}

	minimumTotalTokens, ok := sumNonNegativeTokenCounts(tokens.InputTokens, tokens.OutputTokens)
	if !ok {
		return TokenStats{}
	}
	if tokens.TotalTokens == 0 {
		fallbackTotal := minimumTotalTokens
		if !reasoningTokensAreOutputSubset(provider) {
			fallbackTotal, ok = sumNonNegativeTokenCounts(fallbackTotal, tokens.ReasoningTokens)
			if !ok {
				return TokenStats{}
			}
		}
		tokens.TotalTokens = fallbackTotal
	} else if tokens.TotalTokens < minimumTotalTokens {
		tokens.TotalTokens = minimumTotalTokens
	}
	if tokens.TotalTokens == 0 && tokens.ReasoningTokens != 0 {
		return TokenStats{}
	}
	if !validCanonicalTokenStats(tokens) {
		return TokenStats{}
	}
	return tokens
}

func reasoningTokensAreOutputSubset(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "codex":
		return true
	default:
		return false
	}
}

func nonNegativeTokenCount(token int64) int64 {
	if token < 0 {
		return 0
	}
	return token
}

func addNonNegativeTokenCounts(a, b int64) int64 {
	total, _ := sumNonNegativeTokenCounts(a, b)
	return total
}

// sumNonNegativeTokenCounts returns false instead of wrapping when a usage
// total cannot be represented as a non-negative int64.
func sumNonNegativeTokenCounts(tokens ...int64) (int64, bool) {
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

func cloneRequestDetail(detail RequestDetail) RequestDetail {
	detail.Tokens = cloneTokenStats(detail.Tokens)
	return detail
}

func cloneTokenStats(tokens TokenStats) TokenStats {
	return tokens
}

func normaliseLatency(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	return latency.Milliseconds()
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}
