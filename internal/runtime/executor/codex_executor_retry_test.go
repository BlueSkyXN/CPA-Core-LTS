package executor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseCodexRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("resets_in_seconds", func(t *testing.T) {
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":123}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 123*time.Second {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 123*time.Second)
		}
	})

	t.Run("prefers resets_at", func(t *testing.T) {
		resetAt := now.Add(5 * time.Minute).Unix()
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":1}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 5*time.Minute {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 5*time.Minute)
		}
	})

	t.Run("fallback when resets_at is past", func(t *testing.T) {
		resetAt := now.Add(-1 * time.Minute).Unix()
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":77}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 77*time.Second {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 77*time.Second)
		}
	})

	t.Run("non-429 status code", func(t *testing.T) {
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`)
		if got := parseCodexRetryAfter(http.StatusBadRequest, body, now); got != nil {
			t.Fatalf("expected nil for non-429, got %v", *got)
		}
	})

	t.Run("non usage_limit_reached error type", func(t *testing.T) {
		body := []byte(`{"error":{"type":"server_error","resets_in_seconds":30}}`)
		if got := parseCodexRetryAfter(http.StatusTooManyRequests, body, now); got != nil {
			t.Fatalf("expected nil for non-usage_limit_reached, got %v", *got)
		}
	})
}

func TestNewCodexStatusErrTreatsCapacityAsRetryableRateLimit(t *testing.T) {
	body := []byte(`{"error":{"message":"Selected model is at capacity. Please try a different model."}}`)

	err := newCodexStatusErr(http.StatusBadRequest, body)

	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if err.RetryAfter() != nil {
		t.Fatalf("expected nil explicit retryAfter for capacity fallback, got %v", *err.RetryAfter())
	}
	if got := err.ModelFallbackReason(); got != "capacity" {
		t.Fatalf("ModelFallbackReason() = %q, want capacity", got)
	}
}

func TestNewCodexStatusErrTreatsUsageLimitAsRetryableRateLimit(t *testing.T) {
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":120}}`)

	err := newCodexStatusErr(http.StatusBadRequest, body)

	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	retryAfter := err.RetryAfter()
	if retryAfter == nil {
		t.Fatalf("expected retryAfter from usage_limit_reached, got nil")
	}
	if *retryAfter != 120*time.Second {
		t.Fatalf("retryAfter = %v, want %v", *retryAfter, 120*time.Second)
	}
	if got := err.ModelFallbackReason(); got != "usage-limit" {
		t.Fatalf("ModelFallbackReason() = %q, want usage-limit", got)
	}
	if got := err.CodexRateLimitClass(); got != CodexRateLimitClassUsageLimit {
		t.Fatalf("CodexRateLimitClass() = %q, want %q", got, CodexRateLimitClassUsageLimit)
	}
}

func TestNewCodexStatusErrDoesNotClassifyTransientRateLimitForModelFallback(t *testing.T) {
	err := newCodexStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	if got := err.ModelFallbackReason(); got != "" {
		t.Fatalf("ModelFallbackReason() = %q, want empty", got)
	}
	classified, ok := any(err).(interface{ CodexRateLimitClass() string })
	if !ok {
		t.Fatalf("error %T does not expose CodexRateLimitClass", err)
	}
	if got := classified.CodexRateLimitClass(); got != "transient-rate-limit" {
		t.Fatalf("CodexRateLimitClass() = %q, want transient-rate-limit", got)
	}
}

func TestGenericStatusErrDoesNotClassifyCodexRateLimit(t *testing.T) {
	err := statusErr{
		code: http.StatusTooManyRequests,
		msg:  `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
	}
	if got := err.CodexRateLimitClass(); got != "" {
		t.Fatalf("CodexRateLimitClass() = %q, want empty for a non-Codex status error", got)
	}
}

func TestClassifyCodexUpstreamErrorRequestSemantics(t *testing.T) {
	tests := []struct {
		name                   string
		statusCode             int
		body                   []byte
		wantRequestInvalid     bool
		wantRecordWithoutModel bool
	}{
		{
			name:                   "invalid request",
			statusCode:             http.StatusBadRequest,
			body:                   []byte(`{"error":{"type":"invalid_request_error","message":"invalid query"}}`),
			wantRequestInvalid:     true,
			wantRecordWithoutModel: false,
		},
		{
			name:                   "unprocessable request",
			statusCode:             http.StatusUnprocessableEntity,
			body:                   []byte(`{"error":{"type":"invalid_request_error","message":"query must be a string"}}`),
			wantRequestInvalid:     true,
			wantRecordWithoutModel: false,
		},
		{
			name:                   "request scoped model not found",
			statusCode:             http.StatusNotFound,
			body:                   []byte(`{"error":{"type":"invalid_request_error","param":"model","message":"Model not found gpt-5"}}`),
			wantRequestInvalid:     true,
			wantRecordWithoutModel: false,
		},
		{
			name:                   "model support error needs model scope",
			statusCode:             http.StatusBadRequest,
			body:                   []byte(`{"error":{"type":"invalid_request_error","message":"The requested model is not supported."}}`),
			wantRequestInvalid:     false,
			wantRecordWithoutModel: false,
		},
		{
			name:                   "invalid grant is credential attributable",
			statusCode:             http.StatusBadRequest,
			body:                   []byte(`{"error":{"type":"invalid_grant","code":"invalid_grant","message":"invalid_grant"}}`),
			wantRequestInvalid:     false,
			wantRecordWithoutModel: true,
		},
		{
			name:                   "unauthorized is credential attributable",
			statusCode:             http.StatusUnauthorized,
			body:                   []byte(`{"error":{"type":"authentication_error","code":"invalid_api_key"}}`),
			wantRequestInvalid:     false,
			wantRecordWithoutModel: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			classification := ClassifyCodexUpstreamError(tc.statusCode, tc.body, time.Now())
			if classification.RequestInvalid != tc.wantRequestInvalid {
				t.Fatalf("RequestInvalid = %t, want %t", classification.RequestInvalid, tc.wantRequestInvalid)
			}
			if classification.RecordWithoutModel != tc.wantRecordWithoutModel {
				t.Fatalf("RecordWithoutModel = %t, want %t", classification.RecordWithoutModel, tc.wantRecordWithoutModel)
			}
		})
	}
}

func TestIsCodexUsageLimitError(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{
			name: "nested usage_limit_reached",
			body: []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`),
			want: true,
		},
		{
			name: "top-level usage_limit_reached",
			body: []byte(`{"type":"usage_limit_reached"}`),
			want: true,
		},
		{
			name: "transient rate limit is excluded",
			body: []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`),
			want: false,
		},
		{
			name: "empty body",
			body: nil,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCodexUsageLimitError(tc.body); got != tc.want {
				t.Fatalf("isCodexUsageLimitError = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewCodexStatusErrClassifiesKnownCodexFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		wantStatus int
		wantType   string
		wantCode   string
	}{
		{
			name:       "context length status",
			statusCode: http.StatusRequestEntityTooLarge,
			body:       []byte(`{"error":{"message":"context length exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantType:   "invalid_request_error",
			wantCode:   "context_too_large",
		},
		{
			name:       "thinking signature",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"Invalid signature in thinking block","type":"invalid_request_error","code":"invalid_request_error"}}`),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "thinking_signature_invalid",
		},
		{
			name:       "previous response missing",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"No response found for previous_response_id resp_123","type":"invalid_request_error","code":"previous_response_not_found"}}`),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "previous_response_not_found",
		},
		{
			name:       "auth unavailable",
			statusCode: http.StatusUnauthorized,
			body:       []byte(`{"error":{"message":"invalid or expired token","type":"authentication_error","code":"invalid_api_key"}}`),
			wantStatus: http.StatusUnauthorized,
			wantType:   "authentication_error",
			wantCode:   "auth_unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newCodexStatusErr(tc.statusCode, tc.body)

			if got := err.StatusCode(); got != tc.wantStatus {
				t.Fatalf("status code = %d, want %d", got, tc.wantStatus)
			}
			assertCodexErrorCode(t, err.Error(), tc.wantType, tc.wantCode)
		})
	}
}

func TestNewCodexStatusErrPreservesUnclassifiedErrors(t *testing.T) {
	body := []byte(`{"error":{"message":"documentation mentions too many tokens, but this is a billing configuration failure","type":"server_error","code":"billing_config_error"}}`)

	err := newCodexStatusErr(http.StatusBadGateway, body)

	if got := err.StatusCode(); got != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", got, http.StatusBadGateway)
	}
	if got := err.Error(); got != string(body) {
		t.Fatalf("error body = %s, want original %s", got, string(body))
	}
}

func assertCodexErrorCode(t *testing.T, raw string, wantType string, wantCode string) {
	t.Helper()

	var payload struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("error body is not valid JSON: %v; body=%s", err, raw)
	}
	if payload.Error.Type != wantType {
		t.Fatalf("error.type = %q, want %q; body=%s", payload.Error.Type, wantType, raw)
	}
	if payload.Error.Code != wantCode {
		t.Fatalf("error.code = %q, want %q; body=%s", payload.Error.Code, wantCode, raw)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
