package api

import (
	"context"

	"errors"

	"io"

	"net/http"

	"strconv"
	"strings"

	"time"

	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"

	"github.com/tidwall/gjson"
)

const (
	codexAlphaSearchRequestMaxBytes  int64 = 16 << 20
	codexAlphaSearchResponseMaxBytes int64 = 32 << 20
)

func readCodexAlphaSearchBody(reader io.Reader, maxBytes int64) ([]byte, bool, error) {
	if reader == nil {
		return nil, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		if int64(len(body)) > maxBytes {
			body = body[:maxBytes]
		}
		return body, false, err
	}
	if int64(len(body)) > maxBytes {
		return nil, true, nil
	}
	return body, false, nil
}

func codexAlphaSearchShouldMarkTransportFailure(ctx context.Context, requestErr error) bool {
	if requestErr == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return !errors.Is(requestErr, context.Canceled) && !errors.Is(requestErr, context.DeadlineExceeded)
}

func markCodexAlphaSearchResult(manager *auth.Manager, ctx context.Context, selected *auth.Auth, model string, status int, headers http.Header, body []byte, requestErr error) int {
	statusCode := status
	if statusError, ok := requestErr.(interface{ StatusCode() int }); ok && statusError.StatusCode() > 0 {
		statusCode = statusError.StatusCode()
	}
	classification := runtimeexecutor.ClassifyCodexUpstreamError(statusCode, body, time.Now())
	statusCode = classification.HTTPStatus
	if manager == nil || selected == nil {
		return statusCode
	}
	result := auth.Result{
		AuthID:   selected.ID,
		Provider: "codex",
		Model:    strings.TrimSpace(model),
		Success:  requestErr == nil && statusCode >= http.StatusOK && statusCode < http.StatusBadRequest,
	}
	if !result.Success && (classification.RequestInvalid || (result.Model == "" && !classification.RecordWithoutModel)) {
		return statusCode
	}
	if !result.Success {
		message := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
		if message == "" {
			message = strings.TrimSpace(gjson.GetBytes(body, "message").String())
		}
		if message == "" {
			message = http.StatusText(statusCode)
		}
		if message == "" {
			message = "Codex search request failed"
		}
		if requestErr != nil {
			message = requestErr.Error()
		}
		code := codexAlphaSearchErrorCode(body)
		result.Error = &auth.Error{Code: code, Message: message, HTTPStatus: statusCode}
		result.RetryAfter = codexAlphaSearchHeaderRetryAfter(headers, time.Now())
		if result.RetryAfter == nil {
			result.RetryAfter = classification.RetryAfter
		}
		result.ModelFallbackReason = classification.ModelFallbackReason
	}
	manager.MarkResult(ctx, result)
	return statusCode
}

func codexAlphaSearchErrorCode(body []byte) string {
	for _, path := range []string{"error.code", "error.type", "code", "type"} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			return value
		}
	}
	return ""
}

func codexAlphaSearchHeaderRetryAfter(headers http.Header, now time.Time) *time.Duration {
	if headers != nil {
		raw := strings.TrimSpace(headers.Get("Retry-After"))
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
			duration := time.Duration(seconds) * time.Second
			return &duration
		}
		if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
			duration := retryAt.Sub(now)
			return &duration
		}
	}
	return nil
}
