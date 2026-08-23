package helps

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
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

// ClaudeCodePromptCache maps one Claude Code agent execution scope to a stable upstream prompt_cache_key.

// HeaderValueCaseInsensitive returns the first non-empty header value matching name case-insensitively.
func HeaderValueCaseInsensitive(headers http.Header, name string) string {
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

func ClaudeCodePromptCache(ctx context.Context, modelName string, payload []byte, headers http.Header) (CodexCache, bool, error) {
	modelName = strings.TrimSpace(modelName)
	executionScope, ok := ClaudeCodeExecutionScope(ctx, payload, headers)
	if modelName == "" || !ok {
		return CodexCache{}, false, nil
	}
	key := CodexPromptCacheKey(modelName, executionScope)
	if cache, found, errCache := GetCodexCacheRequired(ctx, key); errCache != nil || found {
		return cache, found, errCache
	}
	cache := CodexCache{
		ID:     uuid.New().String(),
		Expire: time.Now().Add(time.Hour),
	}
	if errSet := SetCodexCacheRequired(ctx, key, cache); errSet != nil {
		return CodexCache{}, false, errSet
	}
	return cache, true, nil
}
