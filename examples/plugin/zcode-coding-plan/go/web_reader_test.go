package main

import (
	"testing"
)

func TestWebReaderHistoryKeepsPageContent(t *testing.T) {
	content := `[{"url":"https://example.com/doc","content":"Page body","metadata":{"language":"en"}}]`
	use := map[string]any{"type": "server_tool_use", "id": "reader-1", "name": "webReader", "input": map[string]any{"url": "https://example.com/doc"}}
	result := map[string]any{"type": "tool_result", "tool_use_id": "reader-1", "content": content}
	for _, tc := range []struct {
		name   string
		blocks []any
		valid  bool
	}{
		{"paired", []any{use, result}, true},
		{"orphan", []any{result}, false},
		{"duplicate", []any{use, result, result}, false},
		{"malformed", []any{use, map[string]any{"type": "tool_result", "tool_use_id": "reader-1", "content": "not page JSON"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"model": "glm-5.3", "max_tokens": 1000, "messages": []any{map[string]any{"role": "assistant", "content": tc.blocks}, map[string]any{"role": "user", "content": "Continue"}}}
			out, err := transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "reader-history")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
			if tc.valid {
				messages := out["messages"].([]any)
				blocks := messages[0].(map[string]any)["content"].([]any)
				if blocks[1].(map[string]any)["content"] != content {
					t.Fatal("page content changed")
				}
			}
		})
	}
}
