package responses

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestResponsesEnvelopeUsesSelectedModelCapabilities(t *testing.T) {
	for _, peerProvider := range []string{"selected-plugin", "claude"} {
		for _, peerFirst := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/peer-first=%t/stream=%t", peerProvider, peerFirst, stream), func(t *testing.T) {
					model := "selected-capability-" + t.Name()
					selected := &registry.ModelInfo{
						ID: model, Name: model, IsCompat: true, UserDefined: true,
						MaxCompletionTokens: 128000,
						Thinking:            &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}},
					}
					peer := &registry.ModelInfo{
						ID: model, MaxCompletionTokens: 4096,
						Thinking: &registry.ThinkingSupport{Min: 1024, Max: 32768},
					}
					reg := registry.GetGlobalRegistry()
					selectedID, peerID := model+"-selected", model+"-peer"
					registerSelected := func() { reg.RegisterClient(selectedID, "selected-plugin", []*registry.ModelInfo{selected}) }
					registerPeer := func() { reg.RegisterClient(peerID, peerProvider, []*registry.ModelInfo{peer}) }
					if peerFirst {
						registerPeer()
						registerSelected()
					} else {
						registerSelected()
						registerPeer()
					}
					t.Cleanup(func() {
						reg.UnregisterClient(selectedID)
						reg.UnregisterClient(peerID)
					})
					check := func() {
						t.Helper()
						for _, tc := range []struct {
							controls string
							max      int64
							effort   string
						}{
							{`"reasoning":{"effort":"max","summary":"auto"}`, 32000, "max"},
							{`"reasoning":{"effort":"max"},"max_output_tokens":64000`, 64000, "max"},
							{`"reasoning":{"effort":"max"},"max_output_tokens":256000`, 128000, "max"},
							{`"reasoning":{"summary":"auto"}`, 32000, ""},
						} {
							body := []byte(fmt.Sprintf(`{"model":%q,"input":"hello",%s}`, model, tc.controls))
							out := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
								Model: model, Body: body, Stream: stream, ModelInfo: selected,
							}).Body
							if got := gjson.GetBytes(out, "output_config.effort").String(); got != tc.effort {
								t.Errorf("effort = %q, want %q", got, tc.effort)
							}
							if got := gjson.GetBytes(out, "max_tokens").Int(); got != tc.max {
								t.Errorf("max_tokens = %d, want %d", got, tc.max)
							}
							if gjson.GetBytes(out, "thinking.type").String() != "adaptive" || gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
								t.Error("selected adaptive thinking was replaced with peer capabilities")
							}
							if gjson.GetBytes(body, "reasoning.summary").Exists() && gjson.GetBytes(out, "thinking.display").String() != "summarized" {
								t.Error("summary intent lost")
							}
							if gjson.GetBytes(out, "stream").Bool() != stream {
								t.Error("stream flag changed")
							}
						}
					}
					check()
					peer.Thinking = &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
					registerPeer()
					check()
					reg.UnregisterClient(peerID)
					check()
					if selected.MaxCompletionTokens != 128000 || !reflect.DeepEqual(selected.Thinking.Levels, []string{"low", "high", "max"}) {
						t.Fatal("translation mutated selected model metadata")
					}
				})
			}
		}
	}
}

func TestResponsesEnvelopeEmptyCapabilitiesAndLegacyFallback(t *testing.T) {
	const model = "selected-empty-capability"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(model+"-peer", "claude", []*registry.ModelInfo{{
		ID: model, MaxCompletionTokens: 4096,
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}},
	}})
	t.Cleanup(func() { reg.UnregisterClient(model + "-peer") })
	body := []byte(`{"input":"hello","reasoning":{"summary":"auto"},"max_output_tokens":64000}`)
	for _, userDefined := range []bool{false, true} {
		selected := &registry.ModelInfo{ID: model, UserDefined: userDefined}
		out := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
			Model: model, Body: body, ModelInfo: selected,
		}).Body
		if gjson.GetBytes(out, "thinking").Exists() || gjson.GetBytes(out, "max_tokens").Int() != 64000 {
			t.Errorf("authoritative empty capability inherited peer fields, user-defined=%t", userDefined)
		}
	}
	legacy := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, model, body, false)
	if gjson.GetBytes(legacy, "thinking.type").String() != "adaptive" || gjson.GetBytes(legacy, "max_tokens").Int() != 4096 {
		t.Fatal("legacy metadata-free translation lost its fallback")
	}
	for _, convert := range []func(string, []byte, bool) []byte{ConvertOpenAIResponsesRequestToClaude, ConvertOpenAIResponsesRequestToClaudeWithCompat} {
		if gjson.GetBytes(convert(model, body, false), "max_tokens").Int() != 4096 {
			t.Error("legacy conversion wrapper changed its fallback")
		}
	}
}

func TestResponsesEffortWithoutThinkingDeclaration(t *testing.T) {
	const model = "effort-undeclared-model"
	body := []byte(`{"input":"hello","reasoning":{"effort":"high"}}`)

	// An authoritative card without any thinking declaration must not invent a
	// budget: the requested effort passes through for downstream validation.
	for _, userDefined := range []bool{false, true} {
		selected := &registry.ModelInfo{ID: model, UserDefined: userDefined}
		out := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
			Model: model, Body: body, ModelInfo: selected,
		}).Body
		if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
			t.Errorf("effort passthrough = %q, want high (user-defined=%t)", got, userDefined)
		}
		if gjson.GetBytes(out, "thinking").Exists() {
			t.Errorf("undeclared thinking invented thinking controls (user-defined=%t)", userDefined)
		}
	}

	// A declared budget-style model (Min/Max without Levels) keeps the budget fallback.
	budgetStyle := &registry.ModelInfo{ID: model, Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000}}
	out := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.RequestEnvelope{
		Model: model, Body: body, ModelInfo: budgetStyle,
	}).Body
	if !gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Error("budget-style thinking declaration lost its budget fallback")
	}

	// Legacy metadata-free translation keeps the historical budget fallback both
	// when the global lookup misses and when it hits a model without thinking data.
	reg := registry.GetGlobalRegistry()
	legacyBody := []byte(`{"input":"hello","reasoning":{"effort":"high"}}`)
	if got := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, model, legacyBody, false); !gjson.GetBytes(got, "thinking.budget_tokens").Exists() {
		t.Error("legacy lookup-miss budget fallback changed")
	}
	reg.RegisterClient(model+"-no-thinking-peer", "claude", []*registry.ModelInfo{{ID: model, MaxCompletionTokens: 4096}})
	t.Cleanup(func() { reg.UnregisterClient(model + "-no-thinking-peer") })
	if got := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, model, legacyBody, false); !gjson.GetBytes(got, "thinking.budget_tokens").Exists() {
		t.Error("legacy no-thinking-hit budget fallback changed")
	}
}
