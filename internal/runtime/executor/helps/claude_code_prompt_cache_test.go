package helps

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestClaudeCodePromptCacheUpstreamParity(t *testing.T) {
	// 固定向量对应 upstream 的 UUIDv5 命名空间、模型和 session/agent 输入。
	for _, tt := range []struct {
		name, model, agent, want string
	}{
		{"root", "gpt-5.4", "", "c3fca9c9-c365-5b79-ad56-5ce6e6100cc4"},
		{"child", "gpt-5.4", "agent-a", "f373593b-7976-5def-af29-cac4b7d043aa"},
		{"other model", "gpt-5.5", "", "887f010a-98ad-5d4b-aa60-cdb17075bc90"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{ClaudeCodeSessionHeader: {"session-cache-agents"}}
			if tt.agent != "" {
				headers.Set(ClaudeCodeAgentHeader, tt.agent)
			}
			got, ok, err := ClaudeCodePromptCache(context.Background(), tt.model, nil, headers)
			if err != nil || !ok || got.ID != tt.want || !got.Expire.IsZero() {
				t.Fatalf("prompt cache = %#v, %v, %v; want stable upstream ID %q without expiry", got, ok, err, tt.want)
			}
		})
	}
}

func TestClaudeCodePromptCacheIndependentOfLegacyMapping(t *testing.T) {
	headers := http.Header{ClaudeCodeSessionHeader: {"session-cache-agents"}}
	key := CodexPromptCacheKey("gpt-5.4", "claude:session-cache-agents:agent:main")
	codexCacheMu.Lock()
	previous, existed := codexCacheMap[key]
	codexCacheMu.Unlock()
	t.Cleanup(func() {
		codexCacheMu.Lock()
		defer codexCacheMu.Unlock()
		if existed {
			codexCacheMap[key] = previous
		} else {
			delete(codexCacheMap, key)
		}
	})
	for _, tt := range []struct {
		name   string
		legacy *CodexCache
	}{
		{"unexpired mapping", &CodexCache{ID: "legacy-random-id", Expire: time.Now().Add(time.Hour)}},
		{"expired mapping", &CodexCache{ID: "legacy-random-id", Expire: time.Now().Add(-time.Hour)}},
		{"mapping lost after restart", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			codexCacheMu.Lock()
			if tt.legacy == nil {
				delete(codexCacheMap, key)
			} else {
				codexCacheMap[key] = *tt.legacy
			}
			codexCacheMu.Unlock()
			got, ok, err := ClaudeCodePromptCache(context.Background(), "gpt-5.4", nil, headers)
			if err != nil || !ok || got.ID != "c3fca9c9-c365-5b79-ad56-5ce6e6100cc4" {
				t.Fatalf("legacy mapping affected deterministic identity: %#v, %v, %v", got, ok, err)
			}
		})
	}
}

func TestClaudeCodePromptCacheConcurrentColdStart(t *testing.T) {
	headers := http.Header{ClaudeCodeSessionHeader: {"session-cache-agents"}}
	key := CodexPromptCacheKey("gpt-5.4", "claude:session-cache-agents:agent:main")
	codexCacheMu.Lock()
	previous, existed := codexCacheMap[key]
	delete(codexCacheMap, key)
	codexCacheMu.Unlock()
	t.Cleanup(func() {
		codexCacheMu.Lock()
		defer codexCacheMu.Unlock()
		if existed {
			codexCacheMap[key] = previous
		} else {
			delete(codexCacheMap, key)
		}
	})
	start := make(chan struct{})
	results := make(chan string, 64)
	var workers sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			got, ok, err := ClaudeCodePromptCache(context.Background(), "gpt-5.4", nil, headers)
			if err != nil || !ok {
				results <- ""
				return
			}
			results <- got.ID
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for id := range results {
		if id != "c3fca9c9-c365-5b79-ad56-5ce6e6100cc4" {
			t.Fatalf("concurrent call returned a different upstream identity: %q", id)
		}
	}
}
