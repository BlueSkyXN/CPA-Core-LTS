package pluginhost

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// Only the selected-auth Execute call installs this sink. The synchronous RPC
// decoder leaves its original error type intact for routing, cooldown and HTTP.
type partialUsageContextKey struct{}
type executorPartialUsage struct {
	detail   coreusage.Detail
	observed bool
	late     <-chan guardedPluginCallResult
}

func executorPartialUsageFromContext(ctx context.Context) *executorPartialUsage {
	if ctx == nil {
		return nil
	}
	partial, _ := ctx.Value(partialUsageContextKey{}).(*executorPartialUsage)
	return partial
}

// A nested host callback may execute another plugin. It must not inherit the
// outer RPC's write target, even if the nested execution has no usage owner.
func withoutExecutorPartialUsage(ctx context.Context) context.Context {
	if executorPartialUsageFromContext(ctx) != nil {
		return context.WithValue(ctx, partialUsageContextKey{}, (*executorPartialUsage)(nil))
	}
	return ctx
}

func (p *executorPartialUsage) observe(envelope pluginabi.Envelope) {
	if !envelope.OK && envelope.Error != nil && len(envelope.Error.PartialUsage) > 0 {
		p.detail, p.observed = parseExecutorPartialUsage(envelope.Error.PartialUsage)
	}
}

func (p executorPartialUsage) publishFailure(ctx context.Context, reporter *helps.UsageReporter, err error) {
	if p.late == nil {
		p.publish(ctx, reporter, err)
		return
	}

	// The RPC can finish after cancellation has been returned to the caller.
	// Allow its cancellation window for accounting, using the original timing and identity.
	reporter.StopLatencyMeasurement()
	ctx = coreusage.CaptureIdentity(ctx)
	timer := time.NewTimer(pluginExecutionLifecycleTimeout)
	go func() {
		defer timer.Stop()
		select {
		case result := <-p.late:
			var envelope pluginabi.Envelope
			if result.err == nil && result.recovered == nil && json.Unmarshal(result.response, &envelope) == nil {
				p.observe(envelope)
			}
		case <-timer.C:
		}
		p.publish(ctx, reporter, err)
	}()
}

func (p executorPartialUsage) publish(ctx context.Context, reporter *helps.UsageReporter, err error) {
	if p.observed {
		reporter.SetUsageProvenance(coreusage.UsageProvenanceProviderReportedUnverified)
	}
	reporter.PublishFailureWithDetail(ctx, p.detail, err)
}

func parseExecutorPartialUsage(raw []byte) (coreusage.Detail, bool) {
	// Six scalar counters fit comfortably; never retain arbitrary plugin metadata.
	var fields map[string]json.RawMessage
	if len(raw) > 1024 || json.Unmarshal(raw, &fields) != nil || len(fields) == 0 {
		return coreusage.Detail{}, false
	}
	var d coreusage.Detail
	var hasCacheRead, hasCacheWrite bool
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
			hasCacheRead = true
		case "cache_creation_tokens":
			d.CacheCreationTokens = n
			hasCacheWrite = true
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
	obs := &coreusage.CacheObservation{
		Version: 1,
		Read:    coreusage.CacheTokenObservation{State: "absent"},
		Write:   coreusage.CacheTokenObservation{State: "absent"},
	}
	if hasCacheRead {
		read := d.CacheReadTokens
		obs.Read = coreusage.CacheTokenObservation{State: "present", Value: &read, Source: "partial_usage.cache_read_tokens"}
	}
	if hasCacheWrite {
		write := d.CacheCreationTokens
		obs.Write = coreusage.CacheTokenObservation{State: "present", Value: &write, Source: "partial_usage.cache_creation_tokens"}
	}
	d.CacheObservation = obs
	return d, true
}
