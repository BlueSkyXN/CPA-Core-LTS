package diff

import (
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPayloadExecutionScopeChangesDetected(t *testing.T) {
	before := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "scope-model"}}, Params: map[string]any{"service_tier": "flex"}}}}}
	for _, mutate := range []func(*config.PayloadModelRule){
		func(rule *config.PayloadModelRule) { rule.AuthKind = "apikey" },
		func(rule *config.PayloadModelRule) { rule.RequestPaths = []string{"/v1/responses"} },
	} {
		after := before.CloneForRuntime()
		mutate(&after.Payload.Override[0].Models[0])
		changes := BuildConfigChangeDetails(before, after)
		if !slices.Contains(changes, "payload.override: updated (1 -> 1 rules)") {
			t.Fatalf("scope-only change missed: %v", changes)
		}
		if before.Payload.Override[0].Models[0].AuthKind != "" || before.Payload.Override[0].Models[0].RequestPaths != nil {
			t.Fatal("snapshot change mutated prior config")
		}
	}
}
