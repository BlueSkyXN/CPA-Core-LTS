package responses

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/diagnostics"
	"testing"
)

func TestCacheUsageMappingAndCompatibilityEvidence(t *testing.T) {
	for _, provided := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "reported"}[provided], func(t *testing.T) {
			store := diagnostics.NewStore()
			ctx := store.Begin(t.Context(), "request", "caller", "", "model", "responses", []byte(`{}`))
			ctx = diagnostics.WithExecution(ctx, "execution")
			extra := ""
			if provided {
				extra = `,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":12,"cache_write_tokens_details":{"ttl_30m_tokens":12}}`
			}
			usage := `"usage":{"prompt_tokens":20,"completion_tokens":1,"total_tokens":21` + extra + `}`
			raw := []byte(`{"id":"chat-result","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],` + usage + `}`)
			var param any
			out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(ctx, "model", []byte(`{}`), []byte(`{}`), raw, &param)
			if got := ojson.GetBytes(out, "usage.input_tokens_details.cache_write_tokens"); got.Exists() != provided || (provided && got.Int() != 12) {
				t.Fatalf("JSON cache write=%s", got.Raw)
			}
			param = nil
			chunk := []byte(`data: {"id":"chat-result","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],` + usage + `}`)
			events := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "model", []byte(`{}`), []byte(`{}`), chunk, &param)
			events = append(events, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "model", []byte(`{}`), []byte(`{}`), []byte("data: [DONE]"), &param)...)
			var terminal ojson.Result
			for _, event := range events {
				kind, payload := parseOpenAIResponsesSSEEvent(t, event)
				if kind == "response.completed" {
					terminal = payload
				}
			}
			if v := terminal.Get("response.usage.input_tokens_details.cached_tokens"); !v.Exists() || v.Int() != 0 {
				t.Fatal("legacy integer wire field missing")
			}
			if v := terminal.Get("response.usage.input_tokens_details.cache_write_tokens_details.ttl_30m_tokens"); v.Exists() != provided || (provided && v.Int() != 12) {
				t.Fatalf("TTL mapping=%s", v.Raw)
			}
			trace, _ := store.Get(diagnostics.FromContext(ctx).ID())
			if provided && len(trace.Events) != 0 {
				t.Fatal("actual zero mislabeled default")
			}
			if !provided && (len(trace.Events) != 1 || trace.Events[0].Result != "compatibility_default" || trace.Events[0].ExecutionID != "execution") {
				t.Fatalf("default provenance=%+v", trace.Events)
			}
		})
	}
}
