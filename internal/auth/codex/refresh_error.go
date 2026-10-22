package codex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Refresh token rejection codes the OpenAI token endpoint returns when the refresh token can no
// longer be used. They match the codes official Codex treats as permanent refresh failures.
const (
	RefreshTokenExpiredCode     = "refresh_token_expired"
	RefreshTokenReusedCode      = "refresh_token_reused"
	RefreshTokenInvalidatedCode = "refresh_token_invalidated"
)

// RefreshTokenError reports that the token endpoint rejected a refresh request. Error keeps the
// historical "token refresh failed with status N: body" text so log readers and existing message
// matchers see no change.
type RefreshTokenError struct {
	// Status is the HTTP status code returned by the token endpoint.
	Status int
	// Code is the structured rejection code (error.code, or the top-level code field).
	Code string
	// Body is the raw response body.
	Body string
}

func (e *RefreshTokenError) Error() string {
	if e == nil {
		return "token refresh failed"
	}
	return fmt.Sprintf("token refresh failed with status %d: %s", e.Status, e.Body)
}

// PermanentRefreshFailure reports whether the refresh token cannot recover without signing in
// again: an expired, reused, or invalidated refresh token, or any 401 from the token endpoint.
// This mirrors official Codex, which stops refreshing and asks for a new login in these cases.
func (e *RefreshTokenError) PermanentRefreshFailure() bool {
	if e == nil {
		return false
	}
	if e.Status == http.StatusUnauthorized {
		return true
	}
	switch e.Code {
	case RefreshTokenExpiredCode, RefreshTokenReusedCode, RefreshTokenInvalidatedCode:
		return true
	default:
		return false
	}
}

func newRefreshTokenError(status int, body []byte) *RefreshTokenError {
	return &RefreshTokenError{Status: status, Code: refreshErrorCode(body), Body: string(body)}
}

// refreshErrorCode extracts the rejection code from either {"error":{"code":"..."}} or a
// top-level {"code":"..."} body.
func refreshErrorCode(body []byte) string {
	var envelope struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	var nested struct {
		Code string `json:"code"`
	}
	if len(envelope.Error) > 0 && json.Unmarshal(envelope.Error, &nested) == nil && strings.TrimSpace(nested.Code) != "" {
		return strings.ToLower(strings.TrimSpace(nested.Code))
	}
	return strings.ToLower(strings.TrimSpace(envelope.Code))
}
