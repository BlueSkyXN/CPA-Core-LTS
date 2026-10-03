package chat_completions

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestChatEnvelopeSelectedCapabilities(t *testing.T) {
	const model = "chat-selected-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(model+"-peer", "claude", []*registry.ModelInfo{{ID: model, MaxCompletionTokens: 4096, Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}})
	t.Cleanup(func() { reg.UnregisterClient(model + "-peer") })
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			info   *registry.ModelInfo
			effort string
			budget bool
			max    int64
		}{
			{"selected", &registry.ModelInfo{ID: model, MaxCompletionTokens: 128000, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}}}, "max", false, 64000},
			{"undeclared", &registry.ModelInfo{ID: model}, "max", false, 64000},
			{"budget", &registry.ModelInfo{ID: model, Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000}}, "", true, 64000},
			{"empty-thinking", &registry.ModelInfo{ID: model, Thinking: &registry.ThinkingSupport{}}, "", true, 64000},
			{"small-limit", &registry.ModelInfo{ID: model, MaxCompletionTokens: 2048}, "max", false, 2048},
		} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				body := []byte(`{"messages":[{"role":"user","content":"hello"}],"reasoning_effort":" MAX ","max_completion_tokens":64000}`)
				out := tr.TranslateRequestEnvelope(context.Background(), tr.FormatOpenAI, tr.FormatClaude, tr.RequestEnvelope{Model: model, Body: body, Stream: stream, ModelInfo: tc.info}).Body
				if gjson.GetBytes(out, "output_config.effort").String() != tc.effort || gjson.GetBytes(out, "thinking.budget_tokens").Exists() != tc.budget || gjson.GetBytes(out, "max_tokens").Int() != tc.max || gjson.GetBytes(out, "stream").Bool() != stream {
					t.Fatalf("unexpected translation: %s", out)
				}
				if tc.info.Thinking == nil && gjson.GetBytes(out, "thinking").Exists() {
					t.Fatal("undeclared capability invented thinking")
				}
				if tc.info.Thinking != nil && gjson.GetBytes(out, "thinking.display").String() != "summarized" {
					t.Fatal("Chat effort display intent lost")
				}
			})
		}
	}
	for _, convert := range []func(string, []byte, bool) []byte{ConvertOpenAIRequestToClaude, ConvertOpenAIRequestToClaudeWithCompat} {
		out := convert(model, []byte(`{"reasoning_effort":"max","max_tokens":64000}`), false)
		if gjson.GetBytes(out, "output_config.effort").String() != "high" || gjson.GetBytes(out, "max_tokens").Int() != 64000 {
			t.Fatal("legacy wrapper changed")
		}
	}
	for _, compat := range []bool{false, true} {
		out := tr.TranslateRequestEnvelope(context.Background(), tr.FormatOpenAI, tr.FormatClaude, tr.RequestEnvelope{Model: model, ModelInfo: &registry.ModelInfo{ID: model, IsCompat: compat}, Body: []byte(`{"messages":[{"role":"assistant","content":"answer","reasoning_content":"public thought"}]}`)}).Body
		if (gjson.GetBytes(out, "messages.0.content.0.type").String() == "thinking") != compat {
			t.Fatal("compat reasoning replay changed")
		}
	}
}
