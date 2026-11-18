package helps

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// 原始 usage 缺少缓存字段时不能把整数默认值解释为上游明确报告零。
func observeOpenAICacheFields(node ojson.Result) *usage.CacheObservation {
	if !node.IsObject() {
		return nil
	}
	return &usage.CacheObservation{
		Version: 1,
		Read:    observeCacheToken(node, "prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens"),
		Write:   observeCacheToken(node, "input_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_creation_tokens"),
	}
}

func observeCacheToken(node ojson.Result, paths ...string) usage.CacheTokenObservation {
	for _, path := range paths {
		value := node.Get(path)
		if !value.Exists() {
			continue
		}
		out := usage.CacheTokenObservation{State: "invalid", Source: path}
		if count, ok := parseOptionalStrictUsageTokenCount(value); ok {
			out.State, out.Value = "present", &count
		}
		return out
	}
	return usage.CacheTokenObservation{State: "absent"}
}
