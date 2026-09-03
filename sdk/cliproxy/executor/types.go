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

// AuthSelectionModelMetadataKey overrides the model used only for auth selection.
const AuthSelectionModelMetadataKey = "auth_selection_model"

// ReasoningEffortMetadataKey stores the client-requested reasoning effort for usage logs.
const ReasoningEffortMetadataKey = "reasoning_effort"

// ServiceTierMetadataKey stores the client-requested service tier for usage logs.
const ServiceTierMetadataKey = "service_tier"

// GenerateMetadataKey stores whether the client requested actual generation for usage logs.
// Missing or true means generation is enabled; only an explicit false disables generation.
const GenerateMetadataKey = "generate"

// RequestIDMetadataKey stores the execution lifecycle request ID. It is distinct
// from the transport/logging trace ID and remains stable across auth retries.
const RequestIDMetadataKey = "request_id"

const (
	// CodexAbnormalReasoningRetryUsageMetadataKey carries discarded abnormal attempt usage for client-visible aggregate usage only.
	CodexAbnormalReasoningRetryUsageMetadataKey = "codex_abnormal_reasoning_retry_usage"
	// RetryWithoutPenaltyUsageDetailMetadataKey carries attempt usage for retry-without-penalty hedge selection and client aggregation.
	RetryWithoutPenaltyUsageDetailMetadataKey = "retry_without_penalty_usage_detail"
	// RetryWithoutPenaltyHedgeScoreMetadataKey carries the score used to choose a quality-mode hedge winner.
	RetryWithoutPenaltyHedgeScoreMetadataKey = "retry_without_penalty_hedge_score"
	// RetryWithoutPenaltyCandidatePolicyMetadataKey carries policy-aware candidate selection metadata.
	RetryWithoutPenaltyCandidatePolicyMetadataKey = "retry_without_penalty_candidate_policy"
	// RetryWithoutPenaltyResponseFinalizerMetadataKey carries a response finalizer for post-hedge client usage aggregation.
	RetryWithoutPenaltyResponseFinalizerMetadataKey = "retry_without_penalty_response_finalizer"
	// RetryWithoutPenaltyStreamFinalizerMetadataKey carries a stream finalizer for post-hedge client usage aggregation.
	RetryWithoutPenaltyStreamFinalizerMetadataKey = "retry_without_penalty_stream_finalizer"
	// RetryWithoutPenaltyStreamUsageMetadataKey carries mutable stream usage captured while chunks are produced.
	RetryWithoutPenaltyStreamUsageMetadataKey = "retry_without_penalty_stream_usage"
	// ExcludeAuthIDsMetadataKey instructs auth selection to skip the listed auth IDs.
	ExcludeAuthIDsMetadataKey = "exclude_auth_ids"
	// PinnedAuthMetadataKey locks execution to a specific auth ID.
	PinnedAuthMetadataKey = "pinned_auth_id"
	// SelectedAuthMetadataKey stores the auth ID selected by the scheduler.
	SelectedAuthMetadataKey = "selected_auth_id"
	// SelectedAuthCallbackMetadataKey carries an optional callback invoked with the selected auth ID.
	SelectedAuthCallbackMetadataKey = "selected_auth_callback"
	// SelectedAuthIndexMetadataKey stores the stable index of the auth selected by the scheduler.
	SelectedAuthIndexMetadataKey = "selected_auth_index"
	// SelectedAuthIndexCallbackMetadataKey carries an optional callback invoked with the selected auth index.
	SelectedAuthIndexCallbackMetadataKey = "selected_auth_index_callback"
	// ExecutionSessionMetadataKey identifies a long-lived downstream execution session.
	ExecutionSessionMetadataKey = "execution_session_id"
	// CodexModelFallbackSourceModelMetadataKey records the source model that exhausted before a configured Codex fallback attempt.
	CodexModelFallbackSourceModelMetadataKey = "codex_model_fallback_source_model"
	// CodexModelFallbackReasoningContinuityMetadataKey carries the configured reasoning-continuity policy for a Codex fallback attempt.
	CodexModelFallbackReasoningContinuityMetadataKey = "codex_model_fallback_reasoning_continuity"
	// CodexModelFallbackContextResetReplayMetadataKey is an internal, additive
	// attestation from the Responses websocket handler. It means the request is
	// a complete, CPA-mediated transcript which may be replayed after dropping
	// model-private reasoning state. It must never be set for websocket
	// passthrough or incremental previous_response_id requests.
	CodexModelFallbackContextResetReplayMetadataKey = "codex_model_fallback_context_reset_replay"
	// DerivedSessionIDMetadataKey stores a stable session identity inferred from request context.
	// It may be used to derive a provider session identity.
	DerivedSessionIDMetadataKey = "derived_session_id"
	// LCPAffinitySessionIDMetadataKey stores an LCP-only routing identity. Executors
	// must not use it as a provider conversation or execution-session identity. The
	// current phase also keeps it out of SessionTree topology until downstream wiring exists.
	LCPAffinitySessionIDMetadataKey = "lcp_affinity_session_id"
	// CanonicalSessionIDMetadataKey stores the single unified session identity reconciled
	// across explicit harness headers, body fields, execution sessions, LCP inference,
	// and fallback context derivation for unified debugging and cross-subsystem tracing.
	CanonicalSessionIDMetadataKey = "canonical_session_id"
	// LCPFingerprintMetadataKey stores bounded request-scoped turn fingerprints so
	// SessionAffinitySelector.OnResult can avoid reparsing the original payload.
	LCPFingerprintMetadataKey = "lcp_fingerprints"
	// LCPMinPrefixLengthMetadataKey stores the minimum eligible prefix boundary for
	// the bounded LCP fingerprint sequence.
	LCPMinPrefixLengthMetadataKey = "lcp_min_prefix_length"
	// CallerScopeMetadataKey isolates inferred session identities between downstream callers.
	CallerScopeMetadataKey = "caller_scope"
	// WorkspaceIdentityMetadataKey stores an opaque, secret-safe workspace namespace
	// for provider execution sessions. It must not contain a raw filesystem path,
	// repository credential, or workspace contents.
	WorkspaceIdentityMetadataKey = "workspace_identity"
	// SessionAffinityProviderMetadataKey carries the affinity selection namespace
	// (provider string, e.g. the literal "mixed" pool key) used by SessionAffinitySelector.Pick,
	// so OnResult keys the session cache identically to how selection read it.
	SessionAffinityProviderMetadataKey = "session_affinity_provider"
	// SessionAffinityModelMetadataKey carries the model used during session affinity selection.
	SessionAffinityModelMetadataKey = "session_affinity_model"
)

const (
	RetryWithoutPenaltyCandidateKindNonSpecial = "non-special"
	RetryWithoutPenaltyCandidateKindSpecial    = "special"
)

// RetryWithoutPenaltyUsageSnapshot carries discarded-attempt usage in both the
// legacy field-sum shape and the folded output-token shape needed by clients.
type RetryWithoutPenaltyUsageSnapshot struct {
	Detail             coreusage.Detail
	FoldedOutputTokens int64
}

// RetryWithoutPenaltyCandidatePolicy carries policy-aware candidate selection
// metadata for retry-without-penalty results. It is additive metadata: callers
// that do not understand it can keep using RetryWithoutPenaltyHedgeScore.
type RetryWithoutPenaltyCandidatePolicy struct {
	DeliveryPolicy  string
	FallbackPolicy  string
	CandidateKind   string
	ReasoningTokens int64
	OutputTokens    int64
	VisibleTokens   int64
}

// RetryWithoutPenaltyResponseFinalizer rewrites a delivered non-stream response
// after hedge selection has the final discarded-attempt usage snapshot.
type RetryWithoutPenaltyResponseFinalizer func(Response, RetryWithoutPenaltyUsageSnapshot) Response

// RetryWithoutPenaltyStreamFinalizer rewrites buffered stream chunks after hedge
// selection has the final discarded-attempt usage snapshot.
type RetryWithoutPenaltyStreamFinalizer func(http.Header, []StreamChunk, RetryWithoutPenaltyUsageSnapshot) *StreamResult

// RetryWithoutPenaltyStreamUsage carries completed usage discovered while a
// stream is produced. Producers set it before emitting the terminal usage chunk.
type RetryWithoutPenaltyStreamUsage struct {
	Detail          coreusage.Detail
	HedgeScore      int64
	CandidatePolicy RetryWithoutPenaltyCandidatePolicy
	OK              bool
}

// UsageAccumulator is a thread-safe request-local usage accumulator carried in Options.Metadata.
type UsageAccumulator struct {
	mu                 sync.Mutex
	detail             coreusage.Detail
	foldedOutputTokens int64
}

// NewUsageAccumulator creates a UsageAccumulator seeded with initial usage.
func NewUsageAccumulator(initial coreusage.Detail) *UsageAccumulator {
	acc := &UsageAccumulator{}
	acc.Add(initial)
	return acc
}

// Add merges detail into the accumulator.
func (a *UsageAccumulator) Add(detail coreusage.Detail) {
	if a == nil {
		return
	}
	detail = normalizeUsageAccumulatorDetail(detail)
	if !hasUsageAccumulatorDetail(detail) {
		return
	}
	foldedOutputTokens := foldedUsageAccumulatorOutputTokens(detail)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.detail = addUsageAccumulatorDetail(a.detail, detail)
	a.foldedOutputTokens = addNonNegativeUsageAccumulatorTokens(a.foldedOutputTokens, foldedOutputTokens)
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

// RetryWithoutPenaltySnapshot returns the field-sum and folded discarded usage
// snapshots used by retry-without-penalty response finalizers.
func (a *UsageAccumulator) RetryWithoutPenaltySnapshot() RetryWithoutPenaltyUsageSnapshot {
	if a == nil {
		return RetryWithoutPenaltyUsageSnapshot{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return RetryWithoutPenaltyUsageSnapshot{
		Detail:             a.detail,
		FoldedOutputTokens: a.foldedOutputTokens,
	}
}

func addUsageAccumulatorDetail(a, b coreusage.Detail) coreusage.Detail {
	a = normalizeUsageAccumulatorDetail(a)
	b = normalizeUsageAccumulatorDetail(b)
	return coreusage.Detail{
		InputTokens:         addNonNegativeUsageAccumulatorTokens(a.InputTokens, b.InputTokens),
		OutputTokens:        addNonNegativeUsageAccumulatorTokens(a.OutputTokens, b.OutputTokens),
		ReasoningTokens:     addNonNegativeUsageAccumulatorTokens(a.ReasoningTokens, b.ReasoningTokens),
		CachedTokens:        addNonNegativeUsageAccumulatorTokens(a.CachedTokens, b.CachedTokens),
		CacheReadTokens:     addNonNegativeUsageAccumulatorTokens(a.CacheReadTokens, b.CacheReadTokens),
		CacheCreationTokens: addNonNegativeUsageAccumulatorTokens(a.CacheCreationTokens, b.CacheCreationTokens),
		TotalTokens:         addNonNegativeUsageAccumulatorTokens(a.TotalTokens, b.TotalTokens),
	}
}

func foldedUsageAccumulatorOutputTokens(detail coreusage.Detail) int64 {
	if detail.OutputTokens >= detail.ReasoningTokens {
		return detail.OutputTokens
	}
	return detail.ReasoningTokens
}

func normalizeUsageAccumulatorDetail(detail coreusage.Detail) coreusage.Detail {
	detail.InputTokens = nonNegativeUsageAccumulatorToken(detail.InputTokens)
	detail.OutputTokens = nonNegativeUsageAccumulatorToken(detail.OutputTokens)
	detail.ReasoningTokens = nonNegativeUsageAccumulatorToken(detail.ReasoningTokens)
	detail.CachedTokens = nonNegativeUsageAccumulatorToken(detail.CachedTokens)
	detail.CacheReadTokens = nonNegativeUsageAccumulatorToken(detail.CacheReadTokens)
	detail.CacheCreationTokens = nonNegativeUsageAccumulatorToken(detail.CacheCreationTokens)
	detail.TotalTokens = nonNegativeUsageAccumulatorToken(detail.TotalTokens)
	if detail.TotalTokens == 0 {
		if total, ok := sumNonNegativeUsageAccumulatorTokens(detail.InputTokens, detail.OutputTokens, detail.ReasoningTokens); ok && total > 0 {
			detail.TotalTokens = total
		}
	}
	return detail
}

func nonNegativeUsageAccumulatorToken(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func addNonNegativeUsageAccumulatorTokens(a, b int64) int64 {
	const maxInt64 = int64(1<<63 - 1)
	if a < 0 || b < 0 || a > maxInt64-b {
		return 0
	}
	return a + b
}

func sumNonNegativeUsageAccumulatorTokens(tokens ...int64) (int64, bool) {
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
	// Terminate prevents the selected executor from receiving the request.
	Terminate bool
	// StatusCode is the downstream HTTP status used when Terminate is true.
	StatusCode int
	// ResponseHeaders contains downstream response headers used when Terminate is true.
	ResponseHeaders http.Header
	// ResponseBody contains the downstream response body used when Terminate is true.
	ResponseBody []byte
}

// RequestTerminatedError carries a plugin-defined downstream response without executing upstream.
type RequestTerminatedError struct {
	HTTPStatus int
	Header     http.Header
	Body       []byte
}

func (e *RequestTerminatedError) Error() string {
	return "request terminated by plugin"
}

// StatusCode returns the plugin-defined downstream HTTP status.
func (e *RequestTerminatedError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// ResponseHeaders returns a copy of the plugin-defined downstream headers.
func (e *RequestTerminatedError) ResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	return e.Header.Clone()
}

// ResponseBody returns a copy of the plugin-defined downstream body.
func (e *RequestTerminatedError) ResponseBody() []byte {
	if e == nil {
		return nil
	}
	return append([]byte(nil), e.Body...)
}

// WebSocketResponseEvent describes an upstream WebSocket response event received during execution.
type WebSocketResponseEvent struct {
	RequestID      string
	TraceID        string
	SourceFormat   string
	Model          string
	RequestedModel string
	Provider       string
	AuthID         string
	AuthLabel      string
	AuthType       string
	EventType      string
	Payload        []byte
	Metadata       map[string]any
}

// WebSocketResponseObserver receives upstream WebSocket response events during execution.
type WebSocketResponseObserver func(context.Context, WebSocketResponseEvent)

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
	// WebSocketResponseObserver receives upstream WebSocket response events during execution.
	WebSocketResponseObserver WebSocketResponseObserver
	// ExecutionLifecycle owns Home-dispatched execution resources. Executors must not add it to request metadata.
	ExecutionLifecycle ExecutionLifecycle
}

// EnsureMetadata initializes and returns Metadata, ensuring it is non-nil.
func (o *Options) EnsureMetadata() map[string]any {
	if o.Metadata == nil {
		o.Metadata = make(map[string]any)
	}
	return o.Metadata
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
	// Metadata exposes optional structured data for stream translators and retry helpers.
	Metadata map[string]any
}

// StatusError represents an error that carries an HTTP-like status code.
// Provider executors should implement this when possible to enable
// better auth state updates on failures (e.g., 401/402/429).
type StatusError interface {
	error
	StatusCode() int
}

// RequestScopedError identifies a failure tied to the current request rather
// than the selected credential. Auth managers should not retry these errors
// across credentials or change credential availability because of them.
type RequestScopedError interface {
	error
	IsRequestScoped() bool
}
