package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(&usageQueuePlugin{})
}

type usageQueuePlugin struct{}

func (p *usageQueuePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if !Enabled() || !internalusage.StatisticsEnabled() {
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
	if serviceTier == "" {
		serviceTier = coreusage.ServiceTierFromContext(ctx)
	}

	tokens := internalusage.TokenStats{
		InputTokens:         record.Detail.InputTokens,
		OutputTokens:        record.Detail.OutputTokens,
		ReasoningTokens:     record.Detail.ReasoningTokens,
		CachedTokens:        record.Detail.CachedTokens,
		CacheReadTokens:     record.Detail.CacheReadTokens,
		CacheCreationTokens: record.Detail.CacheCreationTokens,
		TotalTokens:         record.Detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.CachedTokens == 0 {
		if tokens.CacheReadTokens != 0 {
			tokens.CachedTokens = tokens.CacheReadTokens
		} else if tokens.CacheCreationTokens != 0 {
			tokens.CachedTokens = tokens.CacheCreationTokens
		}
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}

	detail := queuedRequestDetail{
		RequestDetail: internalusage.RequestDetail{
			Timestamp:       timestamp,
			LatencyMs:       record.Latency.Milliseconds(),
			TTFTMs:          record.TTFT.Milliseconds(),
			Source:          record.Source,
			AuthIndex:       record.AuthIndex,
			Alias:           aliasName,
			ReasoningEffort: reasoningEffort,
			ServiceTier:     serviceTier,
			Tokens:          tokens,
			Failed:          failed,
		},
		ResponseHeaders: usageResponseHeaders(record.ResponseHeaders),
	}

	payload, err := json.Marshal(queuedUsageDetail{
		queuedRequestDetail: detail,
		Provider:            provider,
		Model:               modelName,
		Endpoint:            resolveEndpoint(ctx),
		AuthType:            authType,
		APIKey:              apiKey,
		RequestID:           requestID,
	})
	if err != nil {
		return
	}
	Enqueue(payload)
}

type queuedUsageDetail struct {
	queuedRequestDetail
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Endpoint  string `json:"endpoint"`
	AuthType  string `json:"auth_type"`
	APIKey    string `json:"api_key"`
	RequestID string `json:"request_id"`
}

type queuedRequestDetail struct {
	internalusage.RequestDetail
	ResponseHeaders http.Header `json:"response_headers,omitempty"`
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
