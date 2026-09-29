package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

func TestApplySummaryConfigWithModelInfo(t *testing.T) {
	const model = "selected-summary-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(model+"-peer", "claude", []*registry.ModelInfo{{ID: model, Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}})
	t.Cleanup(func() { reg.UnregisterClient(model + "-peer") })
	manual := &registry.ModelInfo{ID: model, Thinking: &registry.ThinkingSupport{Min: 1024, Max: 16000}}
	empty := &registry.ModelInfo{ID: model}
	for _, tc := range []struct {
		name    string
		info    *registry.ModelInfo
		body    string
		summary SummaryConfig
		mode    string
		budget  int64
		display string
	}{
		{"legacy", nil, `{"max_tokens":32000}`, SummaryConfig{Mode: SummaryEnabled}, "adaptive", 0, "summarized"},
		{"manual", manual, `{"max_tokens":32000}`, SummaryConfig{Mode: SummaryEnabled}, "enabled", 1024, "summarized"},
		{"empty", empty, `{"max_tokens":32000}`, SummaryConfig{Mode: SummaryEnabled}, "", 0, ""},
		{"budget-does-not-fit", manual, `{"max_tokens":512}`, SummaryConfig{Mode: SummaryEnabled}, "", 0, ""},
		{"disabled-summary", manual, `{"max_tokens":32000}`, SummaryConfig{Mode: SummaryDisabled}, "", 0, ""},
		{"omit-active-summary", manual, `{"thinking":{"type":"enabled","budget_tokens":2048}}`, SummaryConfig{Mode: SummaryDisabled}, "enabled", 2048, "omitted"},
		{"explicit-disabled-thinking", manual, `{"thinking":{"type":"disabled"}}`, SummaryConfig{Mode: SummaryEnabled}, "disabled", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := ApplySummaryConfigWithModelInfo([]byte(tc.body), "claude", model, tc.info, tc.summary)
			if gjson.GetBytes(out, "thinking.type").String() != tc.mode || gjson.GetBytes(out, "thinking.budget_tokens").Int() != tc.budget || gjson.GetBytes(out, "thinking.display").String() != tc.display {
				t.Fatalf("unexpected selected-model summary: %s", out)
			}
		})
	}
	if manual.Thinking.Min != 1024 || manual.Thinking.Max != 16000 || empty.Thinking != nil {
		t.Fatal("summary application mutated model capabilities")
	}
}
