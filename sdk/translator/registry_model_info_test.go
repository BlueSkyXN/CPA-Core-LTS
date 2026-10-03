package translator

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	modelregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestRegistrySummaryUsesAuthoritativeModelInfo(t *testing.T) {
	for _, pluginTranslation := range []bool{false, true} {
		for _, capability := range []string{"adaptive", "manual", "empty", "legacy"} {
			t.Run(fmt.Sprintf("plugin=%t/%s", pluginTranslation, capability), func(t *testing.T) {
				model := "summary-model-info-" + t.Name()
				peer := &modelregistry.ModelInfo{ID: model, Thinking: &modelregistry.ThinkingSupport{Min: 2048, Max: 32000}}
				global := modelregistry.GetGlobalRegistry()
				global.RegisterClient(model+"-peer", "claude", []*modelregistry.ModelInfo{peer})
				t.Cleanup(func() { global.UnregisterClient(model + "-peer") })
				var selected *modelregistry.ModelInfo
				wantMode, wantBudget := "", int64(0)
				switch capability {
				case "adaptive":
					selected = &modelregistry.ModelInfo{ID: model, Thinking: &modelregistry.ThinkingSupport{Levels: []string{"low", "high", "max"}}}
					wantMode = "adaptive"
				case "manual":
					selected = &modelregistry.ModelInfo{ID: model, Thinking: &modelregistry.ThinkingSupport{Min: 1024, Max: 16000}}
					wantMode, wantBudget = "enabled", 1024
				case "empty":
					selected = &modelregistry.ModelInfo{ID: model}
				case "legacy":
					wantMode, wantBudget = "enabled", 2048
				}
				r := NewRegistry()
				target := []byte(`{"messages":[],"max_tokens":32000}`)
				if pluginTranslation {
					r.SetPluginHooks(&fakePluginHooks{requestTranslateBody: target, requestTranslateOK: true})
				} else {
					r.Register(FormatOpenAIResponse, FormatClaude, func(string, []byte, bool) []byte { return bytes.Clone(target) }, ResponseTransform{})
				}
				out := r.TranslateRequestEnvelope(context.Background(), FormatOpenAIResponse, FormatClaude, RequestEnvelope{
					Model: model, ModelInfo: selected, Body: []byte(`{"input":"hello","reasoning":{"summary":"auto"}}`),
				}).Body
				if got := gjson.GetBytes(out, "thinking.type").String(); got != wantMode {
					t.Errorf("thinking.type = %q, want %q", got, wantMode)
				}
				if got := gjson.GetBytes(out, "thinking.budget_tokens").Int(); got != wantBudget {
					t.Errorf("budget = %d, want %d", got, wantBudget)
				}
				if gjson.GetBytes(out, "thinking.display").Exists() != (wantMode != "") {
					t.Error("unexpected summary visibility")
				}
			})
		}
	}
}

func TestRegistryModelInfoPreservesNormalizerAuthority(t *testing.T) {
	selected := &modelregistry.ModelInfo{ID: "selected-normalizer", Thinking: &modelregistry.ThinkingSupport{Levels: []string{"max"}}}
	for _, pluginTranslation := range []bool{false, true} {
		t.Run(fmt.Sprintf("plugin=%t", pluginTranslation), func(t *testing.T) {
			r := NewRegistry()
			hooks := &fakePluginHooks{}
			if pluginTranslation {
				hooks.requestTranslateBody = []byte(`{"messages":[],"max_tokens":32000}`)
				hooks.requestTranslateOK = true
				hooks.normalizeRequest = func(body []byte) []byte {
					out, _ := sjson.DeleteBytes(body, "reasoning.summary")
					return out
				}
			} else {
				r.Register(FormatOpenAIResponse, FormatClaude, func(string, []byte, bool) []byte {
					return []byte(`{"messages":[],"max_tokens":32000}`)
				}, ResponseTransform{})
				hooks.normalizeRequest = func(body []byte) []byte {
					if gjson.GetBytes(body, "thinking.type").String() != "adaptive" || gjson.GetBytes(body, "thinking.display").String() != "summarized" {
						t.Error("normalizer did not receive the selected model's summary")
					}
					out, _ := sjson.DeleteBytes(body, "thinking")
					return out
				}
			}
			r.SetPluginHooks(hooks)
			out := r.TranslateRequestEnvelope(context.Background(), FormatOpenAIResponse, FormatClaude, RequestEnvelope{
				Model: selected.ID, ModelInfo: selected, Body: []byte(`{"input":"hello","reasoning":{"summary":"auto"}}`),
			}).Body
			if gjson.GetBytes(out, "thinking").Exists() {
				t.Error("normalizer summary removal was reversed")
			}
		})
	}
}

func TestRegistryModelInfoDoesNotChangeMissingTranslation(t *testing.T) {
	r := NewRegistry()
	selected := &modelregistry.ModelInfo{ID: "missing-transform", Thinking: &modelregistry.ThinkingSupport{Levels: []string{"max"}}}
	body := []byte(`{"model":"missing-transform","input":"hello","reasoning":{"summary":"auto"}}`)
	for _, hooks := range []PluginHooks{nil, &fakePluginHooks{requestTranslateOK: false}} {
		r.SetPluginHooks(hooks)
		out := r.TranslateRequestEnvelope(context.Background(), FormatOpenAIResponse, FormatClaude, RequestEnvelope{Model: selected.ID, ModelInfo: selected, Body: bytes.Clone(body)}).Body
		if !bytes.Equal(out, body) {
			t.Error("model metadata added target fields without a translator")
		}
	}
}
