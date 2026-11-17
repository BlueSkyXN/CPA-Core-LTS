package redisqueue

import (
	"encoding/json"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"testing"
)

func TestCacheObservationQueuePreservesPresenceAndInstance(t *testing.T) {
	withEnabledQueue(t, func() {
		(&usageQueuePlugin{}).HandleUsage(t.Context(), coreusage.Record{Provider: "openai", Model: "synthetic-model", RequestID: "execution", DiagnosticsID: "trace", DiagnosticsInstanceID: "instance", Detail: coreusage.Detail{InputTokens: 12, OutputTokens: 1, CacheObservation: &coreusage.CacheObservation{Version: 1, Read: coreusage.CacheTokenObservation{State: "present", Value: coreusage.Int64Ptr(0)}, Write: coreusage.CacheTokenObservation{State: "absent"}}}})
		p := popSinglePayload(t)
		requireStringField(t, p, "diagnostics_id", "trace")
		requireStringField(t, p, "diagnostics_instance_id", "instance")
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
