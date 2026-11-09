package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	internalusage "github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
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
	accessProvider := strings.TrimSpace(record.AccessProvider)
	if accessProvider == "" {
		accessProvider = strings.TrimSpace(coreusage.AccessProviderFromContext(ctx))
	}
	isNativeKey := record.IsNativeKey
	if !isNativeKey {
		if nativeExplicit, ok := coreusage.IsNativeKeyFromContext(ctx); ok {
			isNativeKey = nativeExplicit
		} else if accessProvider != "" {
			isNativeKey = coreusage.IsNativeAccessProvider(accessProvider)
		}
	}
	requestID := strings.TrimSpace(internallogging.GetRequestID(ctx))
	traceID := strings.TrimSpace(record.TraceID)
	if traceID == "" {
		traceID = requestID
	}
	executionID := strings.TrimSpace(record.RequestID)
	if executionID == "" {
		executionID = uuid.NewString()
	}
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
	responseCyberProgram := record.ResponseCyberProgram
	if responseCyberProgram == "" {
		responseCyberProgram = record.Detail.ResponseCyberProgram
	}

	responseModel := strings.TrimSpace(record.ResponseModel)
	clientRequestMetadata := internallogging.GetClientRequestMetadata(ctx)
	sessionID := strings.TrimSpace(record.SessionID)
	parentSessionID := strings.TrimSpace(record.ParentSessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(clientRequestMetadata.SessionID)
		parentSessionID = strings.TrimSpace(clientRequestMetadata.ParentSessionID)
	} else if parentSessionID == "" && sessionID == strings.TrimSpace(clientRequestMetadata.SessionID) {
		parentSessionID = strings.TrimSpace(clientRequestMetadata.ParentSessionID)
	}
	sessionID = coresession.NormalizeToCanonicalUUID(sessionID)
	parentSessionID = coresession.NormalizeToCanonicalUUID(parentSessionID)
	if sessionID == "" || sessionID == parentSessionID {
		parentSessionID = ""
	}

	usageDetail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	tokens := tokenStats{
		InputTokens:              usageDetail.InputTokens,
		OutputTokens:             usageDetail.OutputTokens,
		ReasoningTokens:          usageDetail.ReasoningTokens,
		CachedTokens:             usageDetail.CachedTokens,
		CacheReadTokens:          usageDetail.CacheReadTokens,
		CacheReadTokensPresent:   true,
		CacheCreationTokens:      usageDetail.CacheCreationTokens,
		CacheCreation5mTokens:    coreusage.CloneInt64Ptr(usageDetail.CacheCreation5mTokens),
		CacheCreation30mTokens:   coreusage.CloneInt64Ptr(usageDetail.CacheCreation30mTokens),
		CacheCreation1hTokens:    coreusage.CloneInt64Ptr(usageDetail.CacheCreation1hTokens),
		CacheCreation12hTokens:   coreusage.CloneInt64Ptr(usageDetail.CacheCreation12hTokens),
		TextInputTokens:          coreusage.CloneInt64Ptr(usageDetail.TextInputTokens),
		ImageInputTokens:         coreusage.CloneInt64Ptr(usageDetail.ImageInputTokens),
		AudioInputTokens:         coreusage.CloneInt64Ptr(usageDetail.AudioInputTokens),
		VideoInputTokens:         coreusage.CloneInt64Ptr(usageDetail.VideoInputTokens),
		FileInputTokens:          coreusage.CloneInt64Ptr(usageDetail.FileInputTokens),
		CachedTextTokens:         coreusage.CloneInt64Ptr(usageDetail.CachedTextTokens),
		CachedImageTokens:        coreusage.CloneInt64Ptr(usageDetail.CachedImageTokens),
		CachedAudioTokens:        coreusage.CloneInt64Ptr(usageDetail.CachedAudioTokens),
		TextOutputTokens:         coreusage.CloneInt64Ptr(usageDetail.TextOutputTokens),
		ImageOutputTokens:        coreusage.CloneInt64Ptr(usageDetail.ImageOutputTokens),
		AudioOutputTokens:        coreusage.CloneInt64Ptr(usageDetail.AudioOutputTokens),
		AcceptedPredictionTokens: coreusage.CloneInt64Ptr(usageDetail.AcceptedPredictionTokens),
		RejectedPredictionTokens: coreusage.CloneInt64Ptr(usageDetail.RejectedPredictionTokens),
		TotalTokens:              usageDetail.TotalTokens,
	}
	tokens = normalizeQueuedTokenStats(tokens)
	if tokens.CachedTokens == 0 {
		if tokens.CacheReadTokens != 0 {
			tokens.CachedTokens = tokens.CacheReadTokens
		}
	}

	// Derive the outcome from the record itself; the final inbound status may
	// belong to a later attempt of the same request.
	failed := record.Failed || record.Fail.StatusCode >= httpStatusBadRequest
	fail := resolveFail(ctx, record, failed)

	stream := record.Stream
	if !stream {
		stream = coreusage.StreamFromContext(ctx)
	}

	detail := requestDetail{
		Timestamp:        timestamp,
		LatencyMs:        record.Latency.Milliseconds(),
		Source:           record.Source,
		UsageProvenance:  coreusage.CanonicalUsageProvenance(record.UsageProvenance),
		AuthIndex:        record.AuthIndex,
		AccessTokenHash:  record.AccessTokenSHA256,
		ClientIP:         clientRequestMetadata.ClientIP,
		ResolvedClientIP: clientRequestMetadata.ResolvedClientIP,
		XForwardedFor:    clientRequestMetadata.XForwardedFor,
		UserAgent:        clientRequestMetadata.UserAgent,
		Tokens:           tokens,
		Failed:           failed,
		Generate:         coreusage.GenerateEnabled(record.Generate),
		Stream:           stream,
		OutputTiming:     record.OutputTiming.Clone(),
		Fail:             fail,
		ResponseHeaders:  usageResponseHeaders(record.ResponseHeaders),
		requestSize:      queuedRequestSize(record.Size),
	}
	if record.TimingVersion == coreusage.TimingVersionV1 {
		detail.TimingVersion = record.TimingVersion
		detail.TTFBMs = record.TTFB.Milliseconds()
		detail.TTFTMs = record.TTFT.Milliseconds()
		detail.TTFAMs = record.TTFA.Milliseconds()
	}

	payload, err := json.Marshal(queuedUsageDetail{
		requestDetail:         detail,
		AccountingVersion:     coreusage.TokenAccountingSchemaVersion,
		TokenBreakdown:        usageDetail.TokenBreakdown,
		Provider:              provider,
		ExecutorType:          executorType,
		Model:                 modelName,
		Alias:                 aliasName,
		UpstreamModel:         strings.TrimSpace(record.UpstreamModel),
		Endpoint:              resolveEndpoint(ctx),
		AuthType:              authType,
		APIKey:                apiKey,
		IsNativeKey:           isNativeKey,
		AccessProvider:        accessProvider,
		RequestID:             requestID,
		ExecutionID:           executionID,
		TraceID:               traceID,
		SessionID:             sessionID,
		ParentSessionID:       parentSessionID,
		NodeKind:              strings.TrimSpace(clientRequestMetadata.NodeKind),
		IsFork:                clientRequestMetadata.IsFork,
		IsCompaction:          clientRequestMetadata.IsCompaction,
		ReasoningEffort:       reasoningEffort,
		ServiceTier:           serviceTier,
		RequestServiceTier:    requestServiceTier,
		OutboundServiceTier:   outboundServiceTier,
		ResponseServiceTier:   responseServiceTier,
		EffectiveServiceTier:  effectiveServiceTier,
		ResponseCyberProgram:  coreusage.CanonicalResponseCyberProgram(responseCyberProgram),
		UpstreamServiceTier:   strings.TrimSpace(usageDetail.UpstreamServiceTier),
		ResponseModel:         responseModel,
		Speed:                 strings.TrimSpace(usageDetail.Speed),
		InferenceGeo:          strings.TrimSpace(usageDetail.InferenceGeo),
		CacheDetails:          usageDetail.CacheDetails.Clone(),
		WebSearchRequests:     coreusage.CloneInt64Ptr(usageDetail.WebSearchRequests),
		ImageCount:            coreusage.CloneInt64Ptr(usageDetail.ImageCount),
		RequestedImageDetails: usageDetail.RequestedImageDetails.Clone(),
		ImageDetails:          usageDetail.ImageDetails.Clone(),
		MediaCounts:           usageDetail.MediaCounts.Clone(),
		ToolUsageDetails:      usageDetail.ToolUsageDetails.Clone(),
		Iterations:            usageDetail.IterationSlice(),
		CostDetails:           usageDetail.CostDetails.Clone(),
	})
	if err != nil {
		return
	}
	Enqueue(payload)
}

type queuedUsageDetail struct {
	requestDetail
	AccountingVersion     int                         `json:"accounting_version"`
	TokenBreakdown        coreusage.TokenBreakdown    `json:"token_breakdown"`
	Provider              string                      `json:"provider"`
	ExecutorType          string                      `json:"executor_type"`
	Model                 string                      `json:"model"`
	Alias                 string                      `json:"alias"`
	UpstreamModel         string                      `json:"upstream_model,omitempty"`
	Endpoint              string                      `json:"endpoint"`
	AuthType              string                      `json:"auth_type"`
	APIKey                string                      `json:"api_key"`
	IsNativeKey           bool                        `json:"is_native_key"`
	AccessProvider        string                      `json:"access_provider,omitempty"`
	RequestID             string                      `json:"request_id"`
	ReasoningEffort       string                      `json:"reasoning_effort"`
	ServiceTier           string                      `json:"service_tier"`
	RequestServiceTier    string                      `json:"request_service_tier"`
	OutboundServiceTier   string                      `json:"outbound_service_tier,omitempty"`
	ResponseServiceTier   string                      `json:"response_service_tier,omitempty"`
	EffectiveServiceTier  string                      `json:"effective_service_tier,omitempty"`
	ResponseCyberProgram  string                      `json:"response_cyber_program,omitempty"`
	UpstreamServiceTier   string                      `json:"upstream_service_tier,omitempty"`
	SessionID             string                      `json:"session_id,omitempty"`
	ParentSessionID       string                      `json:"parent_session_id,omitempty"`
	NodeKind              string                      `json:"node_kind,omitempty"`
	IsFork                bool                        `json:"is_fork,omitempty"`
	IsCompaction          bool                        `json:"is_compaction,omitempty"`
	ResponseModel         string                      `json:"response_model,omitempty"`
	ExecutionID           string                      `json:"execution_id,omitempty"`
	TraceID               string                      `json:"trace_id,omitempty"`
	Speed                 string                      `json:"speed,omitempty"`
	InferenceGeo          string                      `json:"inference_geo,omitempty"`
	CacheDetails          *coreusage.CacheDetails     `json:"cache_details,omitempty"`
	WebSearchRequests     *int64                      `json:"web_search_requests,omitempty"`
	ImageCount            *int64                      `json:"image_count,omitempty"`
	RequestedImageDetails *coreusage.ImageDetails     `json:"requested_image_details,omitempty"`
	ImageDetails          *coreusage.ImageDetails     `json:"image_details,omitempty"`
	MediaCounts           *coreusage.MediaCounts      `json:"media_counts,omitempty"`
	ToolUsageDetails      *coreusage.ToolUsageDetails `json:"tool_usage_details,omitempty"`
	Iterations            []coreusage.IterationUsage  `json:"iterations,omitempty"`
	CostDetails           *coreusage.CostDetails      `json:"cost_details,omitempty"`
}

type requestDetail struct {
	Timestamp        time.Time               `json:"timestamp"`
	LatencyMs        int64                   `json:"latency_ms"`
	TimingVersion    uint32                  `json:"timing_version,omitempty"`
	OutputTiming     *coreusage.OutputTiming `json:"output_timing,omitempty"`
	TTFBMs           int64                   `json:"ttfb_ms,omitempty"`
	TTFTMs           int64                   `json:"ttft_ms,omitempty"`
	TTFAMs           int64                   `json:"ttfa_ms,omitempty"`
	Source           string                  `json:"source"`
	UsageProvenance  string                  `json:"usage_provenance,omitempty"`
	AuthIndex        string                  `json:"auth_index"`
	AccessTokenHash  string                  `json:"access_token_sha256,omitempty"`
	ClientIP         string                  `json:"client_ip"`
	ResolvedClientIP string                  `json:"resolved_client_ip"`
	XForwardedFor    string                  `json:"x_forwarded_for"`
	UserAgent        string                  `json:"user_agent"`
	Tokens           tokenStats              `json:"tokens"`
	Failed           bool                    `json:"failed"`
	Generate         bool                    `json:"generate"`
	Stream           bool                    `json:"stream"`
	Fail             failDetail              `json:"fail"`
	ResponseHeaders  http.Header             `json:"response_headers,omitempty"`
	requestSize
}

// requestSize mirrors the usage detail size fields; zero values are omitted.
type requestSize struct {
	SizeVersion              uint32 `json:"size_version,omitempty"`
	ClientRequestBytes       int64  `json:"client_request_bytes,omitempty"`
	ClientRequestEncoding    string `json:"client_request_encoding,omitempty"`
	UpstreamTransport        string `json:"upstream_transport,omitempty"`
	UpstreamRequestBytes     int64  `json:"upstream_request_bytes,omitempty"`
	UpstreamRequestWireBytes int64  `json:"upstream_request_wire_bytes,omitempty"`
	UpstreamRequestEncoding  string `json:"upstream_request_encoding,omitempty"`
	UpstreamResponseBytes    int64  `json:"upstream_response_bytes,omitempty"`
}

func queuedRequestSize(size coreusage.RequestSize) requestSize {
	size = size.Canonical()
	return requestSize{
		SizeVersion:              size.Version,
		ClientRequestBytes:       size.ClientRequestBytes,
		ClientRequestEncoding:    size.ClientRequestEncoding,
		UpstreamTransport:        size.UpstreamTransport,
		UpstreamRequestBytes:     size.UpstreamRequestBytes,
		UpstreamRequestWireBytes: size.UpstreamRequestWireBytes,
		UpstreamRequestEncoding:  size.UpstreamRequestEncoding,
		UpstreamResponseBytes:    size.UpstreamResponseBytes,
	}
}

type tokenStats struct {
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	ReasoningTokens          int64  `json:"reasoning_tokens"`
	CachedTokens             int64  `json:"cached_tokens"`
	CacheReadTokens          int64  `json:"cache_read_tokens"`
	CacheReadTokensPresent   bool   `json:"cache_read_tokens_present"`
	CacheCreationTokens      int64  `json:"cache_creation_tokens"`
	CacheCreation5mTokens    *int64 `json:"cache_creation_5m_tokens,omitempty"`
	CacheCreation30mTokens   *int64 `json:"cache_creation_30m_tokens,omitempty"`
	CacheCreation1hTokens    *int64 `json:"cache_creation_1h_tokens,omitempty"`
	CacheCreation12hTokens   *int64 `json:"cache_creation_12h_tokens,omitempty"`
	TextInputTokens          *int64 `json:"text_input_tokens,omitempty"`
	ImageInputTokens         *int64 `json:"image_input_tokens,omitempty"`
	AudioInputTokens         *int64 `json:"audio_input_tokens,omitempty"`
	VideoInputTokens         *int64 `json:"video_input_tokens,omitempty"`
	FileInputTokens          *int64 `json:"file_input_tokens,omitempty"`
	CachedTextTokens         *int64 `json:"cached_text_tokens,omitempty"`
	CachedImageTokens        *int64 `json:"cached_image_tokens,omitempty"`
	CachedAudioTokens        *int64 `json:"cached_audio_tokens,omitempty"`
	TextOutputTokens         *int64 `json:"text_output_tokens,omitempty"`
	ImageOutputTokens        *int64 `json:"image_output_tokens,omitempty"`
	AudioOutputTokens        *int64 `json:"audio_output_tokens,omitempty"`
	AcceptedPredictionTokens *int64 `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens *int64 `json:"rejected_prediction_tokens,omitempty"`
	TotalTokens              int64  `json:"total_tokens"`
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
	Code       string `json:"code,omitempty"`
}

func resolveFail(ctx context.Context, record coreusage.Record, failed bool) failDetail {
	fail := failDetail{
		StatusCode: record.Fail.StatusCode,
		Body:       strings.TrimSpace(record.Fail.Body),
		Code:       strings.TrimSpace(record.Fail.Code),
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
