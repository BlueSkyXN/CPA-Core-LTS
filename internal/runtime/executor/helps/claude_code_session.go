package helps

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
)

const ClaudeCodeSessionHeader = internalcache.ClaudeCodeSessionHeader

// ExtractClaudeCodeSessionID resolves a Claude Code session ID, preferring X-Claude-Code-Session-Id over payload metadata.
func ExtractClaudeCodeSessionID(ctx context.Context, payload []byte, headers http.Header) string {
	return internalcache.ExtractClaudeCodeSessionID(ctx, payload, headers)
}

// ClaudeCodePromptCache maps a Claude Code session to a stable upstream prompt_cache_key.
func ClaudeCodePromptCache(ctx context.Context, modelName string, payload []byte, headers http.Header) (CodexCache, bool, error) {
	sessionID := ExtractClaudeCodeSessionID(ctx, payload, headers)
	if sessionID == "" {
		return CodexCache{}, false, nil
	}
	key := CodexPromptCacheKey(modelName, "claude:"+sessionID)
	if cache, ok, errCache := GetCodexCacheRequired(ctx, key); errCache != nil || ok {
		return cache, ok, errCache
	}
	cache := CodexCache{
		ID:     uuid.New().String(),
		Expire: time.Now().Add(1 * time.Hour),
	}
	if errSet := SetCodexCacheRequired(ctx, key, cache); errSet != nil {
		return CodexCache{}, false, errSet
	}
	return cache, true, nil
}
