package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(&usageQueuePlugin{})
}

type usageQueuePlugin struct{}

func (p *usageQueuePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if !Enabled() || !UsageStatisticsEnabled() || !internalusage.StatisticsEnabled() {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	aliasName := strings.TrimSpace(record.Alias)
	if aliasName == "" {
		aliasName = modelName
	}
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	executorType := strings.TrimSpace(record.ExecutorType)
	if executorType == "" {
		executorType = "unknown"
	}
	authType := strings.TrimSpace(record.AuthType)
	if authType == "" {
		authType = "unknown"
	}
	apiKey := strings.TrimSpace(record.APIKey)
	requestID := strings.TrimSpace(internallogging.GetRequestID(ctx))
	reasoningEffort := strings.TrimSpace(record.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = coreusage.ReasoningEffortFromContext(ctx)
	}
	serviceTier := strings.TrimSpace(record.ServiceTier)
	requestServiceTier := strings.TrimSpace(record.RequestServiceTier)
	if serviceTier == "" {
		serviceTier = requestServiceTier
	}
	if serviceTier == "" {
		serviceTier = coreusage.ServiceTierFromContext(ctx)
	}
	if requestServiceTier == "" {
		requestServiceTier = serviceTier
	}
	outboundServiceTier := strings.TrimSpace(record.OutboundServiceTier)
	responseServiceTier := strings.TrimSpace(record.ResponseServiceTier)
	effectiveServiceTier := coreusage.CanonicalEffectiveServiceTier(record.EffectiveServiceTier)
	clientRequestMetadata := internallogging.GetClientRequestMetadata(ctx)

	usageDetail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	tokens := tokenStats{
		InputTokens:            usageDetail.InputTokens,
		OutputTokens:           usageDetail.OutputTokens,
		ReasoningTokens:        usageDetail.ReasoningTokens,
		CachedTokens:           usageDetail.CachedTokens,
		CacheReadTokens:        usageDetail.CacheReadTokens,
		CacheReadTokensPresent: true,
		CacheCreationTokens:    usageDetail.CacheCreationTokens,
		TotalTokens:            usageDetail.TotalTokens,
	}
	tokens = normalizeQueuedTokenStats(tokens)
	if tokens.CachedTokens == 0 {
		if tokens.CacheReadTokens != 0 {
			tokens.CachedTokens = tokens.CacheReadTokens
		}
	}

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	fail := resolveFail(ctx, record, failed)

	stream := record.Stream
	if !stream {
		stream = coreusage.StreamFromContext(ctx)
	}

	detail := requestDetail{
		Timestamp:       timestamp,
		LatencyMs:       record.Latency.Milliseconds(),
		Source:          record.Source,
		UsageProvenance: coreusage.CanonicalUsageProvenance(record.UsageProvenance),
		AuthIndex:       record.AuthIndex,
		AccessTokenHash: record.AccessTokenSHA256,
		ClientIP:        clientRequestMetadata.ClientIP,
		XForwardedFor:   clientRequestMetadata.XForwardedFor,
		UserAgent:       clientRequestMetadata.UserAgent,
		Tokens:          tokens,
		Failed:          failed,
		Generate:        coreusage.GenerateEnabled(record.Generate),
		Stream:          stream,
		Fail:            fail,
		ResponseHeaders: usageResponseHeaders(record.ResponseHeaders),
	}
	if record.TimingVersion == coreusage.TimingVersionV1 {
		detail.TimingVersion = record.TimingVersion
		detail.TTFBMs = record.TTFB.Milliseconds()
		detail.TTFTMs = record.TTFT.Milliseconds()
		detail.TTFAMs = record.TTFA.Milliseconds()
	}

	payload, err := json.Marshal(queuedUsageDetail{
		requestDetail:        detail,
		AccountingVersion:    coreusage.TokenAccountingSchemaVersion,
		TokenBreakdown:       usageDetail.TokenBreakdown,
		Provider:             provider,
		ExecutorType:         executorType,
		Model:                modelName,
		Alias:                aliasName,
		Endpoint:             resolveEndpoint(ctx),
		AuthType:             authType,
		APIKey:               apiKey,
		RequestID:            requestID,
		ReasoningEffort:      reasoningEffort,
		ServiceTier:          serviceTier,
		RequestServiceTier:   requestServiceTier,
		OutboundServiceTier:  outboundServiceTier,
		ResponseServiceTier:  responseServiceTier,
		EffectiveServiceTier: effectiveServiceTier,
	})
	if err != nil {
		return
	}
	Enqueue(payload)
}

type queuedUsageDetail struct {
	requestDetail
	AccountingVersion    int                      `json:"accounting_version"`
	TokenBreakdown       coreusage.TokenBreakdown `json:"token_breakdown"`
	Provider             string                   `json:"provider"`
	ExecutorType         string                   `json:"executor_type"`
	Model                string                   `json:"model"`
	Alias                string                   `json:"alias"`
	Endpoint             string                   `json:"endpoint"`
	AuthType             string                   `json:"auth_type"`
	APIKey               string                   `json:"api_key"`
	RequestID            string                   `json:"request_id"`
	ReasoningEffort      string                   `json:"reasoning_effort"`
	ServiceTier          string                   `json:"service_tier"`
	RequestServiceTier   string                   `json:"request_service_tier"`
	OutboundServiceTier  string                   `json:"outbound_service_tier,omitempty"`
	ResponseServiceTier  string                   `json:"response_service_tier,omitempty"`
	EffectiveServiceTier string                   `json:"effective_service_tier,omitempty"`
}

type requestDetail struct {
	Timestamp       time.Time   `json:"timestamp"`
	LatencyMs       int64       `json:"latency_ms"`
	TimingVersion   uint32      `json:"timing_version,omitempty"`
	TTFBMs          int64       `json:"ttfb_ms,omitempty"`
	TTFTMs          int64       `json:"ttft_ms,omitempty"`
	TTFAMs          int64       `json:"ttfa_ms,omitempty"`
	Source          string      `json:"source"`
	UsageProvenance string      `json:"usage_provenance,omitempty"`
	AuthIndex       string      `json:"auth_index"`
	AccessTokenHash string      `json:"access_token_sha256,omitempty"`
	ClientIP        string      `json:"client_ip"`
	XForwardedFor   string      `json:"x_forwarded_for"`
	UserAgent       string      `json:"user_agent"`
	Tokens          tokenStats  `json:"tokens"`
	Failed          bool        `json:"failed"`
	Generate        bool        `json:"generate"`
	Stream          bool        `json:"stream"`
	Fail            failDetail  `json:"fail"`
	ResponseHeaders http.Header `json:"response_headers,omitempty"`
}

type tokenStats struct {
	InputTokens            int64 `json:"input_tokens"`
	OutputTokens           int64 `json:"output_tokens"`
	ReasoningTokens        int64 `json:"reasoning_tokens"`
	CachedTokens           int64 `json:"cached_tokens"`
	CacheReadTokens        int64 `json:"cache_read_tokens"`
	CacheReadTokensPresent bool  `json:"cache_read_tokens_present"`
	CacheCreationTokens    int64 `json:"cache_creation_tokens"`
	TotalTokens            int64 `json:"total_tokens"`
}

func normalizeQueuedTokenStats(tokens tokenStats) tokenStats {
	tokens.InputTokens = nonNegativeQueuedToken(tokens.InputTokens)
	tokens.OutputTokens = nonNegativeQueuedToken(tokens.OutputTokens)
	tokens.ReasoningTokens = nonNegativeQueuedToken(tokens.ReasoningTokens)
	tokens.CachedTokens = nonNegativeQueuedToken(tokens.CachedTokens)
	tokens.CacheReadTokens = nonNegativeQueuedToken(tokens.CacheReadTokens)
	tokens.CacheCreationTokens = nonNegativeQueuedToken(tokens.CacheCreationTokens)
	tokens.TotalTokens = nonNegativeQueuedToken(tokens.TotalTokens)
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens, _ = sumNonNegativeQueuedTokens(tokens.InputTokens, tokens.OutputTokens, tokens.ReasoningTokens)
	}
	return tokens
}

func nonNegativeQueuedToken(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func sumNonNegativeQueuedTokens(tokens ...int64) (int64, bool) {
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

type failDetail struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

func resolveFail(ctx context.Context, record coreusage.Record, failed bool) failDetail {
	fail := failDetail{
		StatusCode: record.Fail.StatusCode,
		Body:       strings.TrimSpace(record.Fail.Body),
	}
	if !failed {
		return failDetail{StatusCode: 200}
	}
	if fail.StatusCode <= 0 {
		fail.StatusCode = internallogging.GetResponseStatus(ctx)
	}
	if fail.StatusCode <= 0 {
		fail.StatusCode = 500
	}
	return fail
}

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

func resolveEndpoint(ctx context.Context) string {
	return strings.TrimSpace(internallogging.GetEndpoint(ctx))
}

const httpStatusBadRequest = 400

var sensitiveUsageResponseHeaders = map[string]struct{}{
	"Authorization":       {},
	"Proxy-Authorization": {},
	"Set-Cookie":          {},
	"Cookie":              {},
}

func usageResponseHeaders(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		canonical := http.CanonicalHeaderKey(key)
		if _, blocked := sensitiveUsageResponseHeaders[canonical]; blocked {
			continue
		}
		dst[canonical] = append([]string(nil), values...)
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}
