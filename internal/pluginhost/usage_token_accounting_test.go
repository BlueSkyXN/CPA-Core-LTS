package pluginhost

import (
	"context"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestUsageAdapterPreservesCanonicalOutputAccounting(t *testing.T) {
	var got pluginapi.UsageRecord
	plugin := usagePluginFunc(func(_ context.Context, r pluginapi.UsageRecord) { got = r })
	host := newHostWithRecords(capabilityRecord{id: "accounting", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: plugin}}})
	(&usageAdapter{host: host, pluginID: "accounting"}).HandleUsage(context.Background(), coreusage.Record{Provider: "gemini", Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 100, ReasoningTokens: 40, TotalTokens: 150, TokenBreakdown: coreusage.NewSeparateReasoningTokenBreakdown(10, 0, 0, 100, 40, 150)}})
	b := got.Detail.TokenBreakdown
	if b == nil || b.SchemaVersion != 2 || b.Output.TotalTokens != 140 || b.Output.NonReasoningTokens != 100 || got.Detail.OutputTokens != 100 {
		t.Fatalf("plugin accounting=%+v raw=%d", b, got.Detail.OutputTokens)
	}
}
