package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
)

const (
	ClaudeCodeSessionHeader = internalcache.ClaudeCodeSessionHeader
	ClaudeCodeAgentHeader   = internalcache.ClaudeCodeAgentHeader
	ClaudeCodeMainAgentID   = internalcache.ClaudeCodeMainAgentID
)

// ExtractClaudeCodeSessionID resolves a Claude Code session ID, preferring X-Claude-Code-Session-Id over payload metadata.
func ExtractClaudeCodeSessionID(ctx context.Context, payload []byte, headers http.Header) string {
	return internalcache.ExtractClaudeCodeSessionID(ctx, payload, headers)
}

// ExtractClaudeCodeAgentID resolves the Claude Code agent ID and uses a stable sentinel for the root agent.
func ExtractClaudeCodeAgentID(ctx context.Context, headers http.Header) string {
	return internalcache.ExtractClaudeCodeAgentID(ctx, headers)
}

// ClaudeCodeExecutionScope returns the stable root-session and agent identity used by Codex execution state.
func ClaudeCodeExecutionScope(ctx context.Context, payload []byte, headers http.Header) (string, bool) {
	return internalcache.ClaudeCodeExecutionScope(ctx, payload, headers)
}

// HeaderValueCaseInsensitive returns the first non-empty header value matching name case-insensitively.
func HeaderValueCaseInsensitive(headers http.Header, name string) string {
	return headerValueCaseInsensitive(headers, name)
}

// HeaderValuesCaseInsensitive returns all non-empty header values matching name case-insensitively.
func HeaderValuesCaseInsensitive(headers http.Header, name string) []string {
	if headers == nil {
		return nil
	}
	var result []string
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			for _, value := range values {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					result = append(result, trimmed)
				}
			}
		}
	}
	return result
}

func headerValueCaseInsensitive(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

// ClaudeCodePromptCache 按 upstream 的确定性规则派生模型和 session/agent 对应的 PCK。
// 身份不依赖本地或 Home KV 映射的生命周期，首次并发请求也使用相同的 key。
func ClaudeCodePromptCache(ctx context.Context, modelName string, payload []byte, headers http.Header) (CodexCache, bool, error) {
	modelName = strings.TrimSpace(modelName)
	executionScope, ok := ClaudeCodeExecutionScope(ctx, payload, headers)
	if modelName == "" || !ok {
		return CodexCache{}, false, nil
	}
	identity := strings.Join([]string{"cli-proxy-api:codex:claude-code", modelName, executionScope}, "\x00")
	return CodexCache{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()}, true, nil
}
