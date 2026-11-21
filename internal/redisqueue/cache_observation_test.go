package redisqueue

import (
	"encoding/json"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"strings"
	"testing"
)

func TestCacheObservationQueuePreservesPresenceAndInstance(t *testing.T) {
	withEnabledQueue(t, func() {
		want := coreusage.CacheRouteKey{Kind: "pck", Digest: strings.Repeat("a", 64), Scope: strings.Repeat("b", 64)}
		route := &coreusage.CacheRouteSummary{Version: 1, AttemptCount: 1, References: []coreusage.CacheRouteReference{{CacheRouteKey: want, Method: "outbound"}}}
		(&usageQueuePlugin{}).HandleUsage(t.Context(), coreusage.Record{Provider: "openai", Model: "synthetic-model", RequestID: "execution", DiagnosticsID: "trace", DiagnosticsInstanceID: "instance", CacheRoute: route, Detail: coreusage.Detail{InputTokens: 12, OutputTokens: 1, CacheObservation: &coreusage.CacheObservation{Version: 1, Read: coreusage.CacheTokenObservation{State: "present", Value: coreusage.Int64Ptr(0)}, Write: coreusage.CacheTokenObservation{State: "absent"}}}})
		p := popSinglePayload(t)
		requireStringField(t, p, "diagnostics_id", "trace")
		requireStringField(t, p, "diagnostics_instance_id", "instance")
		var cacheRoute coreusage.CacheRouteSummary
		if err := json.Unmarshal(p["cache_route"], &cacheRoute); err != nil || !cacheRoute.Matches(want) {
			t.Fatalf("queue route=%+v: %v", cacheRoute, err)
		}
		var obs map[string]any
		if err := json.Unmarshal(p["cache_observation"], &obs); err != nil {
			t.Fatal(err)
		}
		read := obs["read"].(map[string]any)
		write := obs["write"].(map[string]any)
		if read["state"] != "present" || read["value"] != float64(0) || write["state"] != "absent" {
			t.Fatalf("observation=%v", obs)
		}
		if _, exists := write["value"]; exists {
			t.Fatal("absent write became zero")
		}
	})
}
