package executor

import (
	"context"
	"net/http"
	"net/url"
	"sync"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// RequestedModelMetadataKey stores the client-requested model name in Options.Metadata.
const RequestedModelMetadataKey = "requested_model"

// RequestPathMetadataKey stores the inbound HTTP request path (e.g. "/v1/images/generations") in Options.Metadata.
// It is optional and may be absent for non-HTTP executions.
const RequestPathMetadataKey = "request_path"

// DisallowFreeAuthMetadataKey instructs auth selection to skip known free-tier credentials.
const DisallowFreeAuthMetadataKey = "disallow_free_auth"

// ReasoningEffortMetadataKey stores the client-requested reasoning effort for usage logs.
const ReasoningEffortMetadataKey = "reasoning_effort"

// ServiceTierMetadataKey stores the client-requested service tier for usage logs.
const ServiceTierMetadataKey = "service_tier"

const (
	// CodexAbnormalReasoningRetryUsageMetadataKey carries discarded abnormal attempt usage for client-visible aggregate usage only.
	CodexAbnormalReasoningRetryUsageMetadataKey = "codex_abnormal_reasoning_retry_usage"
	// ExcludeAuthIDsMetadataKey instructs auth selection to skip the listed auth IDs.
	ExcludeAuthIDsMetadataKey = "exclude_auth_ids"
	// PinnedAuthMetadataKey locks execution to a specific auth ID.
	PinnedAuthMetadataKey = "pinned_auth_id"
	// SelectedAuthMetadataKey stores the auth ID selected by the scheduler.
	SelectedAuthMetadataKey = "selected_auth_id"
	// SelectedAuthCallbackMetadataKey carries an optional callback invoked with the selected auth ID.
	SelectedAuthCallbackMetadataKey = "selected_auth_callback"
	// ExecutionSessionMetadataKey identifies a long-lived downstream execution session.
	ExecutionSessionMetadataKey = "execution_session_id"
)

// UsageAccumulator is a thread-safe request-local usage accumulator carried in Options.Metadata.
type UsageAccumulator struct {
	mu     sync.Mutex
	detail coreusage.Detail
}

// NewUsageAccumulator creates a UsageAccumulator seeded with initial usage.
func NewUsageAccumulator(initial coreusage.Detail) *UsageAccumulator {
	acc := &UsageAccumulator{}
	acc.Add(initial)
	return acc
}

// Add merges detail into the accumulator.
func (a *UsageAccumulator) Add(detail coreusage.Detail) {
	if a == nil || !hasUsageAccumulatorDetail(detail) {
		return
	}
	detail = normalizeUsageAccumulatorDetail(detail)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.detail = addUsageAccumulatorDetail(a.detail, detail)
}

// Snapshot returns the current accumulated usage.
func (a *UsageAccumulator) Snapshot() coreusage.Detail {
	if a == nil {
		return coreusage.Detail{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.detail
}

func addUsageAccumulatorDetail(a, b coreusage.Detail) coreusage.Detail {
	a = normalizeUsageAccumulatorDetail(a)
	b = normalizeUsageAccumulatorDetail(b)
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

func normalizeUsageAccumulatorDetail(detail coreusage.Detail) coreusage.Detail {
	if detail.TotalTokens == 0 {
		total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
		if total > 0 {
			detail.TotalTokens = total
		}
	}
	return detail
}

func hasUsageAccumulatorDetail(detail coreusage.Detail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0
}

// Request encapsulates the translated payload that will be sent to a provider executor.
type Request struct {
	// Model is the upstream model identifier after translation.
	Model string
	// Payload is the provider specific JSON payload.
	Payload []byte
	// Format represents the provider payload schema.
	Format sdktranslator.Format
	// Metadata carries optional provider specific execution hints.
	Metadata map[string]any
}

// RequestAfterAuthInterceptor rewrites a request after credential selection and before executor translation.
type RequestAfterAuthInterceptor func(context.Context, RequestAfterAuthInterceptRequest) RequestAfterAuthInterceptResponse

// RequestAfterAuthInterceptRequest describes a selected-auth request before executor translation.
type RequestAfterAuthInterceptRequest struct {
	// SourceFormat is the original client protocol format.
	SourceFormat sdktranslator.Format
	// ToFormat is the selected upstream protocol format.
	ToFormat sdktranslator.Format
	// Model is the selected upstream model for this attempt.
	Model string
	// RequestedModel is the client-requested model before alias/model-pool rewriting.
	RequestedModel string
	// Stream reports whether the request expects streaming output.
	Stream bool
	// Headers contains the current upstream request headers.
	Headers http.Header
	// Body contains the current request payload.
	Body []byte
	// Metadata is a best-effort cloned context snapshot. Treat it as read-only and JSON-like.
	Metadata map[string]any
}

// RequestAfterAuthInterceptResponse returns selected-auth request modifications.
type RequestAfterAuthInterceptResponse struct {
	// Headers replaces matching current request headers and preserves headers not mentioned here.
	Headers http.Header
	// Body replaces the current request body only when non-empty.
	Body []byte
	// ClearHeaders explicitly removes current request headers before Headers is applied.
	ClearHeaders []string
}

// Options controls execution behavior for both streaming and non-streaming calls.
type Options struct {
	// Stream toggles streaming mode.
	Stream bool
	// Alt carries optional alternate format hint (e.g. SSE JSON key).
	Alt string
	// Headers are forwarded to the provider request builder.
	Headers http.Header
	// Query contains optional query string parameters.
	Query url.Values
	// OriginalRequest preserves the inbound request bytes prior to translation.
	OriginalRequest []byte
	// SourceFormat identifies the inbound schema.
	SourceFormat sdktranslator.Format
	// ResponseFormat identifies the downstream response schema.
	// Empty means responses should use SourceFormat for backward compatibility.
	ResponseFormat sdktranslator.Format
	// Metadata carries extra execution hints shared across selection and executors.
	Metadata map[string]any
	// RequestAfterAuthInterceptor runs after credential selection and before executor translation.
	RequestAfterAuthInterceptor RequestAfterAuthInterceptor
}

// ResponseFormatOrSource returns the response target format for an execution.
func ResponseFormatOrSource(opts Options) sdktranslator.Format {
	if opts.ResponseFormat != "" {
		return opts.ResponseFormat
	}
	return opts.SourceFormat
}

// Response wraps either a full provider response or metadata for streaming flows.
type Response struct {
	// Payload is the provider response in the executor format.
	Payload []byte
	// Metadata exposes optional structured data for translators.
	Metadata map[string]any
	// Headers carries upstream HTTP response headers for passthrough to clients.
	Headers http.Header
}

// StreamChunk represents a single streaming payload unit emitted by provider executors.
type StreamChunk struct {
	// Payload is the raw provider chunk payload.
	Payload []byte
	// Err reports any terminal error encountered while producing chunks.
	Err error
}

// StreamResult wraps the streaming response, providing both the chunk channel
// and the upstream HTTP response headers captured before streaming begins.
type StreamResult struct {
	// Headers carries upstream HTTP response headers from the initial connection.
	Headers http.Header
	// Chunks is the channel of streaming payload units.
	Chunks <-chan StreamChunk
}

// StatusError represents an error that carries an HTTP-like status code.
// Provider executors should implement this when possible to enable
// better auth state updates on failures (e.g., 401/402/429).
type StatusError interface {
	error
	StatusCode() int
}
