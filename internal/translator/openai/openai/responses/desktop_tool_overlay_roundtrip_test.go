package responses

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexapptools"
	"github.com/tidwall/gjson"
)

func TestCodexDesktopToolOverlayOpenAIChatRoundTrip(t *testing.T) {
	originalRequest := codexDesktopToolOverlayRequest(t, "read_thread")
	chatRequest := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k3", originalRequest, false)
	if got := gjson.GetBytes(chatRequest, "tools.0.function.name").String(); got != "codex_app__read_thread" {
		t.Fatalf("flattened name = %q, want codex_app__read_thread; request=%s", got, chatRequest)
	}
	if got := gjson.GetBytes(chatRequest, "tools.0.function.parameters.properties.turnLimit.minimum").Int(); got != 1 {
		t.Fatalf("turnLimit.minimum = %d, want 1; request=%s", got, chatRequest)
	}
	if got := gjson.GetBytes(chatRequest, "tools.0.function.parameters.properties.maxOutputCharsPerItem.maximum").Int(); got != 20000 {
		t.Fatalf("maxOutputCharsPerItem.maximum = %d, want 20000; request=%s", got, chatRequest)
	}

	chatResponse := []byte(`{"id":"chatcmpl_overlay","object":"chat.completion","created":1773896263,"model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_read_1","type":"function","function":{"name":"codex_app__read_thread","arguments":"{\"threadId\":\"019fc266\",\"turnLimit\":3}"}}]},"finish_reason":"tool_calls"}]}`)
	response := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "kimi-k3", originalRequest, chatRequest, chatResponse, nil)
	output := gjson.GetBytes(response, "output.0")
	if got := output.Get("name").String(); got != "read_thread" {
		t.Fatalf("restored name = %q, want read_thread; response=%s", got, response)
	}
	if got := output.Get("namespace").String(); got != "codex_app" {
		t.Fatalf("restored namespace = %q, want codex_app; response=%s", got, response)
	}
	if got := output.Get("call_id").String(); got != "call_read_1" {
		t.Fatalf("call_id = %q, want call_read_1; response=%s", got, response)
	}
	if got := output.Get("arguments").String(); got != `{"threadId":"019fc266","turnLimit":3}` {
		t.Fatalf("arguments = %q; response=%s", got, response)
	}
}

func codexDesktopToolOverlayRequest(t *testing.T, toolName string) []byte {
	t.Helper()
	definitions, err := codexapptools.Select([]string{toolName})
	if err != nil || len(definitions) != 1 {
		t.Fatalf("Select(%s) = %v, %v", toolName, definitions, err)
	}
	definition := definitions[0]
	body, err := json.Marshal(map[string]any{
		"model": "client-model",
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "read it"}},
		"tools": []any{map[string]any{
			"type": "namespace",
			"name": "codex_app",
			"tools": []any{map[string]any{
				"type":        "function",
				"name":        definition.Name,
				"description": definition.Description,
				"parameters":  definition.Parameters,
				"strict":      definition.Strict,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
