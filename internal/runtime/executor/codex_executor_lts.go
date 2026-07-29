package executor

import (
	"crypto/sha256"

	"fmt"

	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexModelHeaderProfile struct {
	overrides map[string]string
	digest    [sha256.Size]byte
}

func resolveCodexModelHeaderProfile(modelName string) codexModelHeaderProfile {
	rawOverrides := registry.ModelOverrideHeaders(strings.TrimSpace(modelName), "codex")
	if len(rawOverrides) == 0 {
		return codexModelHeaderProfile{}
	}

	type headerEntry struct {
		key   string
		value string
	}
	entries := make([]headerEntry, 0, len(rawOverrides))
	for key, value := range rawOverrides {
		key = http.CanonicalHeaderKey(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		entries = append(entries, headerEntry{key: key, value: value})
	}
	if len(entries) == 0 {
		return codexModelHeaderProfile{}
	}
	sort.Slice(entries, func(i, j int) bool {
		leftKey := strings.ToLower(entries[i].key)
		rightKey := strings.ToLower(entries[j].key)
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return entries[i].value < entries[j].value
	})

	overrides := make(map[string]string, len(entries))
	for _, entry := range entries {

		overrides[entry.key] = entry.value
	}

	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return strings.ToLower(keys[i]) < strings.ToLower(keys[j])
	})

	var serialized strings.Builder
	for _, key := range keys {
		value := overrides[key]
		normalizedKey := strings.ToLower(key)
		_, _ = fmt.Fprintf(&serialized, "%d:%s%d:%s", len(normalizedKey), normalizedKey, len(value), value)
	}

	return codexModelHeaderProfile{
		overrides: overrides,
		digest:    sha256.Sum256([]byte(serialized.String())),
	}
}

type codexStreamBufferLimitError struct {
	statusErr
}

func newCodexStreamBufferLimitError(limit int64) codexStreamBufferLimitError {
	return codexStreamBufferLimitError{statusErr: statusErr{
		code: http.StatusBadGateway,
		msg:  fmt.Sprintf("codex abnormal reasoning retry stream buffer limit exceeded: %d bytes", limit),
	}}
}

func (codexStreamBufferLimitError) IsRequestScoped() bool {
	return true
}

// sanitizeCodexUnsupportedReasoningSummary keeps reasoning effort enabled for
// Spark while removing the summary field that its upstream rejects.
func sanitizeCodexUnsupportedReasoningSummary(body []byte, modelName string) []byte {
	if !strings.EqualFold(strings.TrimSpace(modelName), codexSparkModel) {
		return body
	}

	out := body
	if gjson.GetBytes(out, "reasoning.summary").Exists() {
		out, _ = sjson.DeleteBytes(out, "reasoning.summary")
	}

	reasoning := gjson.GetBytes(out, "reasoning")
	if reasoning.IsObject() && len(reasoning.Map()) == 0 {
		out, _ = sjson.DeleteBytes(out, "reasoning")
	}
	return out
}

// CodexUpstreamErrorClassification captures the shared Codex error semantics
// used by translated responses and standalone Codex endpoints.
type CodexUpstreamErrorClassification struct {
	HTTPStatus          int
	ModelFallbackReason string
	RetryAfter          *time.Duration
	RequestInvalid      bool
	RecordWithoutModel  bool
}

// ClassifyCodexUpstreamError normalizes Codex quota and capacity responses.
// Codex can report these conditions with HTTP 400 even though routing and
// cooldown behavior must treat them as HTTP 429.
func ClassifyCodexUpstreamError(statusCode int, body []byte, now time.Time) CodexUpstreamErrorClassification {
	classification := CodexUpstreamErrorClassification{HTTPStatus: statusCode}
	if isCodexModelCapacityError(body) {
		classification.HTTPStatus = http.StatusTooManyRequests
		classification.ModelFallbackReason = config.CodexModelFallbackTriggerCapacity
	} else if isCodexUsageLimitError(body) {
		classification.HTTPStatus = http.StatusTooManyRequests
		classification.ModelFallbackReason = config.CodexModelFallbackTriggerUsageLimit
	}
	classification.RetryAfter = parseCodexRetryAfter(classification.HTTPStatus, body, now)
	classification.RequestInvalid = isCodexRequestInvalidError(classification.HTTPStatus, body)
	classification.RecordWithoutModel = codexErrorRecordableWithoutModel(classification, body)
	return classification
}

func isCodexRequestInvalidError(statusCode int, body []byte) bool {
	if isCodexModelCapacityError(body) || isCodexUsageLimitError(body) || isCodexCredentialError(body) || isCodexModelSupportError(body) {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(string(body)))
	switch statusCode {
	case http.StatusBadRequest:
		return true
	case http.StatusNotFound:
		return isCodexRequestScopedNotFound(body)
	case http.StatusUnprocessableEntity:
		return true
	case http.StatusInternalServerError:
		return strings.Contains(lower, `"status":"unknown"`) || strings.Contains(lower, `"status": "unknown"`)
	default:
		return false
	}
}

func codexErrorRecordableWithoutModel(classification CodexUpstreamErrorClassification, body []byte) bool {
	if classification.RequestInvalid {
		return false
	}
	if classification.ModelFallbackReason != "" || isCodexCredentialError(body) {
		return true
	}
	switch classification.HTTPStatus {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	default:
		return classification.HTTPStatus >= http.StatusInternalServerError
	}
}

func isCodexCredentialError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(body, "error.type").String(),
		gjson.GetBytes(body, "error.code").String(),
		gjson.GetBytes(body, "type").String(),
		gjson.GetBytes(body, "code").String(),
	}
	for _, candidate := range candidates {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "authentication_error", "invalid_api_key", "invalid_grant", "auth_unavailable":
			return true
		}
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "invalid or expired token")
}

func isCodexModelSupportError(body []byte) bool {
	lower := strings.ToLower(strings.TrimSpace(string(body)))
	for _, pattern := range []string{
		"model_not_supported",
		"requested model is not supported",
		"requested model is unsupported",
		"requested model is unavailable",
		"model is not supported",
		"model not supported",
		"unsupported model",
		"model unavailable",
		"not available for your plan",
		"not available for your account",
	} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func isCodexRequestScopedNotFound(body []byte) bool {
	lower := strings.ToLower(strings.TrimSpace(string(body)))
	if strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false") {
		return true
	}
	if !strings.Contains(lower, "model not found") ||
		!strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()), "invalid_request_error") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "error.param").String()), "model")
}
