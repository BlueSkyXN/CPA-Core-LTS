package responses

import (
	"bytes"
	"github.com/tidwall/gjson"
	"testing"
)

func TestCodexAstraControlsSurviveNativeTranslation(t *testing.T) {
	input := []byte(`{"model":"gpt-6-astra","reasoning":{"effort":"medium"},"prompt_cache_key":"stable-key","parallel_tool_calls":false,"top_logprobs":5,"include":["message.output_text.logprobs"],"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"function_call_output","call_id":"pending-previous-turn","output":"done"},{"type":"compaction_trigger"}],"tools":[{"type":"function","name":"slow","async":true,"parameters":{"type":"object","properties":{}}}]}`)
	output := ConvertOpenAIResponsesRequestToCodex("gpt-6-astra", input, true)
	for _, path := range []string{"input", "tools", "reasoning.effort", "prompt_cache_key", "parallel_tool_calls"} {
		if got, want := gjson.GetBytes(output, path).Raw, gjson.GetBytes(input, path).Raw; got != want {
			t.Fatalf("%s changed: got %s want %s", path, got, want)
		}
	}
	if gjson.GetBytes(output, "top_logprobs").Exists() {
		t.Fatal("unsupported top_logprobs forwarded")
	}
	if got := gjson.GetBytes(output, "include").Raw; got != `["reasoning.encrypted_content"]` {
		t.Fatalf("include = %s", got)
	}
	if repeated := ConvertOpenAIResponsesRequestToCodex("gpt-6-astra", output, true); !bytes.Equal(output, repeated) {
		t.Fatal("normalization is not idempotent")
	}
}

func TestCodexParallelToolDefaultAndExplicitValues(t *testing.T) {
	for _, tt := range []struct {
		body string
		want bool
	}{
		{`{"input":"hello"}`, true},
		{`{"input":"hello","parallel_tool_calls":true}`, true},
		{`{"input":"hello","parallel_tool_calls":false}`, false},
	} {
		output := ConvertOpenAIResponsesRequestToCodex("gpt-6-astra", []byte(tt.body), true)
		if got := gjson.GetBytes(output, "parallel_tool_calls").Bool(); got != tt.want {
			t.Fatalf("got %v want %v", got, tt.want)
		}
	}
}
