package pluginhost

import (
	"context"
	"encoding/json"
	"math"
	"strconv"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Only the selected-auth Execute call installs this sink. The synchronous RPC
// decoder leaves its original error type intact for routing, cooldown and HTTP.
type partialUsageContextKey struct{}
type executorPartialUsage struct {
	detail   coreusage.Detail
	observed bool
}

// A nested host callback may execute another plugin. It must not inherit the
// outer RPC's write target, even if the nested execution has no usage owner.
func withoutExecutorPartialUsage(ctx context.Context) context.Context {
	if ctx != nil {
		if p, _ := ctx.Value(partialUsageContextKey{}).(*executorPartialUsage); p != nil {
			return context.WithValue(ctx, partialUsageContextKey{}, (*executorPartialUsage)(nil))
		}
	}
	return ctx
}

func parseExecutorPartialUsage(raw []byte) (coreusage.Detail, bool) {
	// Six scalar counters fit comfortably; never retain arbitrary plugin metadata.
	var fields map[string]json.RawMessage
	if len(raw) > 1024 || json.Unmarshal(raw, &fields) != nil || len(fields) == 0 {
		return coreusage.Detail{}, false
	}
	var d coreusage.Detail
	for name, value := range fields {
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil || n < 0 {
			return coreusage.Detail{}, false
		}
		switch name {
		case "input_tokens":
			d.InputTokens = n
		case "output_tokens":
			d.OutputTokens = n
		case "total_tokens":
			d.TotalTokens = n
		case "cache_read_tokens":
			d.CacheReadTokens = n
		case "cache_creation_tokens":
			d.CacheCreationTokens = n
		case "reasoning_tokens":
			d.ReasoningTokens = n
		default:
			return coreusage.Detail{}, false
		}
	}
	if d.CacheReadTokens > math.MaxInt64-d.CacheCreationTokens {
		return coreusage.Detail{}, false
	}
	cache := d.CacheReadTokens + d.CacheCreationTokens
	input, output := max(d.InputTokens, cache), max(d.OutputTokens, d.ReasoningTokens)
	if input > math.MaxInt64-output || fields["input_tokens"] != nil && input != d.InputTokens || fields["output_tokens"] != nil && output != d.OutputTokens || fields["total_tokens"] != nil && d.TotalTokens < input+output {
		return coreusage.Detail{}, false
	}
	d.CachedTokens = d.CacheReadTokens
	d.TokenBreakdown = coreusage.NewPartialSubsetTokenBreakdown(input, d.CacheReadTokens, d.CacheCreationTokens, output, d.ReasoningTokens, d.TotalTokens)
	return d, true
}
