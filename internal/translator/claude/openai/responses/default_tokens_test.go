package responses

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func TestResponsesDefaultOutputBudget(t *testing.T) {
	for _, tc := range []struct {
		name, model, control string
		info                 *registry.ModelInfo
		want                 int64
	}{
		{name: "claude", model: "claude-opus-4-6", want: 64000},
		{name: "fable", model: "claude-fable-5-1", want: 64000},
		{name: "unknown", model: "unknown-output-budget", want: 64000},
		{name: "selected plugin", model: "glm-5.3", info: &registry.ModelInfo{MaxCompletionTokens: 128000}, want: 64000},
		{name: "selected smaller limit", model: "glm-5.3", info: &registry.ModelInfo{MaxCompletionTokens: 8192}, want: 8192},
		{name: "catalog smaller limit", model: "claude-3-5-haiku-20241022", want: 8192},
		{name: "null uses default", model: "claude-opus-4-6", control: `,"max_output_tokens":null`, want: 64000},
		{name: "explicit smaller", model: "claude-opus-4-6", control: `,"max_output_tokens":4096`, want: 4096},
		{name: "explicit larger", model: "claude-opus-4-6", control: `,"max_output_tokens":128000`, want: 128000},
		{name: "explicit above model limit", model: "glm-5.3", info: &registry.ModelInfo{MaxCompletionTokens: 128000}, control: `,"max_output_tokens":256000`, want: 128000},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"input":"hello"%s}`, tc.model, tc.control))
				out := ConvertOpenAIResponsesRequestToClaudeWithModelInfo(tc.model, body, stream, tc.info)
				if got := gjson.GetBytes(out, "max_tokens").Int(); got != tc.want {
					t.Fatalf("max_tokens = %d, want %d", got, tc.want)
				}
			})
		}
	}
}
