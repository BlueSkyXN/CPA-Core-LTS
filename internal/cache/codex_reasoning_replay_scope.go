package cache

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"

var claudeCodeSessionSuffixPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// CodexReasoningReplaySessionInput contains the request identity surfaces used
// by Codex reasoning replay. TranslatedPayload must be the final executor body
// after request interception and payload shaping when it is available.
type CodexReasoningReplaySessionInput struct {
	Context                 context.Context
	SourceFormat            string
	TranslatedPayload       []byte
	RequestPayload          []byte
	Headers                 http.Header
	OptionExecutionSession  string
	RequestExecutionSession string
}

// ResolveCodexReasoningReplaySessionKey is the single priority resolver shared
// by the Codex executor and the auth-layer model-fallback preflight.
func ResolveCodexReasoningReplaySessionKey(input CodexReasoningReplaySessionInput) string {
	if !strings.EqualFold(strings.TrimSpace(input.SourceFormat), "claude") {
		return ""
	}
	if value := strings.TrimSpace(input.OptionExecutionSession); value != "" {
		return "execution:" + value
	}
	if value := strings.TrimSpace(input.RequestExecutionSession); value != "" {
		return "execution:" + value
	}
	if value := CodexReasoningReplaySessionKeyFromPayload(input.TranslatedPayload); value != "" {
		return value
	}
	if value := CodexReasoningReplaySessionKeyFromPayload(input.RequestPayload); value != "" {
		return value
	}
	if value := CodexReasoningReplaySessionKeyFromHeaders(input.Headers); value != "" {
		return value
	}
	if ginHeaders := headersFromGinContext(input.Context); ginHeaders != nil {
		if value := CodexReasoningReplaySessionKeyFromHeaders(ginHeaders); value != "" {
			return value
		}
	}
	if sessionID := ExtractClaudeCodeSessionID(input.Context, input.RequestPayload, input.Headers); sessionID != "" {
		return "claude:" + sessionID
	}
	return ""
}

func CodexReasoningReplaySessionKeyFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if value := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); value != "" {
		return "prompt-cache:" + value
	}
	if value := strings.TrimSpace(gjson.GetBytes(payload, "client_metadata.x-codex-window-id").String()); value != "" {
		return "window:" + value
	}
	return CodexReasoningReplaySessionKeyFromTurnMetadata(gjson.GetBytes(payload, "client_metadata.x-codex-turn-metadata").String())
}

func CodexReasoningReplaySessionKeyFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if value := CodexReasoningReplaySessionKeyFromTurnMetadata(headerValueCaseInsensitive(headers, "X-Codex-Turn-Metadata")); value != "" {
		return value
	}
	if value := strings.TrimSpace(headerValueCaseInsensitive(headers, "X-Codex-Window-Id")); value != "" {
		return "window:" + value
	}
	for _, name := range []string{"Session_id", "session_id", "Session-Id"} {
		if value := strings.TrimSpace(headerValueCaseInsensitive(headers, name)); value != "" {
			return "session-id:" + value
		}
	}
	if value := strings.TrimSpace(headerValueCaseInsensitive(headers, "Conversation_id")); value != "" {
		return "conversation_id:" + value
	}
	return ""
}

func CodexReasoningReplaySessionKeyFromTurnMetadata(raw string) string {
	if value := strings.TrimSpace(gjson.Get(raw, "prompt_cache_key").String()); value != "" {
		return "prompt-cache:" + value
	}
	if value := strings.TrimSpace(gjson.Get(raw, "window_id").String()); value != "" {
		return "window:" + value
	}
	return ""
}

// ExtractClaudeCodeSessionID resolves both the legacy `_session_<uuid>` user
// ID and the newer JSON metadata shape, preferring explicit request headers.
func ExtractClaudeCodeSessionID(ctx context.Context, payload []byte, headers http.Header) string {
	if sessionID := strings.TrimSpace(headerValueCaseInsensitive(headers, ClaudeCodeSessionHeader)); sessionID != "" {
		return sessionID
	}
	if ginHeaders := headersFromGinContext(ctx); ginHeaders != nil {
		if sessionID := strings.TrimSpace(headerValueCaseInsensitive(ginHeaders, ClaudeCodeSessionHeader)); sessionID != "" {
			return sessionID
		}
	}
	if len(payload) == 0 {
		return ""
	}
	userID := strings.TrimSpace(gjson.GetBytes(payload, "metadata.user_id").String())
	if userID == "" {
		return ""
	}
	if matches := claudeCodeSessionSuffixPattern.FindStringSubmatch(userID); len(matches) >= 2 {
		return strings.TrimSpace(matches[1])
	}
	if strings.HasPrefix(userID, "{") {
		return strings.TrimSpace(gjson.Get(userID, "session_id").String())
	}
	return ""
}

func headersFromGinContext(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return nil
	}
	return ginCtx.Request.Header
}

func headerValueCaseInsensitive(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), name) || len(values) == 0 {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return ""
}
