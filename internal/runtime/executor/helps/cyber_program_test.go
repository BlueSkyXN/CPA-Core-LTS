package helps

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestResponseCyberProgram(t *testing.T) {
	for _, tt := range []struct{ name, fields, want string }{
		{"blue", `"access_programs":{"cyber":"daybreak_blue"}`, "daybreak_blue"},
		{"blue with null error", `"error":null,"access_programs":{"cyber":"daybreak_blue"}`, "daybreak_blue"},
		{"standard", `"access_programs":{"cyber":"standard"}`, "standard"},
		{"standard null", `"access_programs":null`, "standard"},
		{"missing", `"model":"gpt-daybreak-blue-latest"`, ""},
		{"empty object", `"access_programs":{}`, "unknown"},
		{"null cyber", `"access_programs":{"cyber":null}`, "unknown"},
		{"future", `"access_programs":{"cyber":"daybreak_red"}`, "unknown"},
		{"malformed", `"access_programs":true`, "unknown"},
		{"blank", `"access_programs":{"cyber":" "}`, "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, tokens := range []string{"", `,"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}`} {
				response := `{` + tt.fields + tokens + `}`
				event := `{"type":"response.completed","response":` + response + `}`
				codex, _ := ParseCodexUsage([]byte(event))
				openai := ParseOpenAIUsage([]byte(response))
				stream, _ := ParseOpenAIStreamUsage([]byte("data: " + response))
				for name, got := range map[string]usage.Detail{"codex": codex, "openai": openai, "stream": stream} {
					if got.ResponseCyberProgram != tt.want {
						t.Fatalf("%s program = %q, want %q", name, got.ResponseCyberProgram, tt.want)
					}
					if tokens != "" && got.TotalTokens != 3 {
						t.Fatalf("%s lost tokens: %+v", name, got)
					}
				}
			}
		})
	}
	for _, payload := range []string{
		`{"type":"response.output_item.done","access_programs":{"cyber":"daybreak_blue"}}`,
		`{"error":{"code":"access_program_not_enabled"},"access_programs":{"cyber":"daybreak_blue"}}`,
		`{"response":{"access_programs":{"cyber":"daybreak_blue"}}`,
	} {
		if got := extractResponseCyberProgram([]byte(payload)); got != "" {
			t.Fatalf("non-response program = %q", got)
		}
	}
}

func TestResponseCyberProgramStreamAndReporter(t *testing.T) {
	for _, protocol := range []string{"openai", "codex", "openai-response"} {
		t.Run(protocol, func(t *testing.T) {
			var b StreamUsageBuffer
			for _, event := range []string{
				`{"access_programs":{"cyber":"daybreak_blue"}}`,
				`{"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`,
				`{"access_programs":{"cyber":"standard"}}`,
			} {
				if protocol != "openai" {
					event = `{"type":"response.completed","response":` + event + `}`
				}
				ObservePluginExecutorStreamUsage(protocol, []byte("data: "+event+"\n\n"), &b)
			}
			detail, ok := b.Detail()
			if !ok || detail.ResponseCyberProgram != "standard" || detail.TotalTokens != 3 {
				t.Fatalf("stream detail = %+v, ok=%t", detail, ok)
			}
		})
	}
	for _, final := range []string{"standard", "unknown"} {
		r := NewUsageReporter(context.Background(), "codex", "base", nil)
		r.ObserveCodexResponseModel([]byte(`data: {"type":"response.created","response":{"access_programs":{"cyber":"daybreak_blue"}}}`))
		r.ObserveCodexResponseModel([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"access_programs":{"cyber":%q}}}`, final)))
		r.ObserveCodexResponseModel([]byte(`{"type":"response.created","response":{"access_programs":{"cyber":"daybreak_blue"}}}`))
		if got := r.buildRecord(usage.Detail{}, false).ResponseCyberProgram; got != final {
			t.Fatalf("no-usage terminal = %q, want %q", got, final)
		}
		if got := r.buildRecordForModel("image-tool", usage.Detail{ResponseCyberProgram: "daybreak_blue"}, false, usage.Failure{}).ResponseCyberProgram; got != "" {
			t.Fatalf("side model inherited program: %q", got)
		}
	}
	r := NewUsageReporter(context.Background(), "codex", "gpt-daybreak-blue-latest", nil)
	if got := r.buildRecord(usage.Detail{}, true).ResponseCyberProgram; got != "" {
		t.Fatalf("failed request inferred program: %q", got)
	}
	merged := MergeStreamUsageDetail(usage.Detail{ResponseCyberProgram: "daybreak_blue"}, usage.Detail{OutputTokens: 1})
	if merged.ResponseCyberProgram != "daybreak_blue" {
		t.Fatal("merged stream lost program")
	}
}
