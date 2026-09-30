package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func toolRequest(tools any) []byte {
	return encode(map[string]any{
		"model": "glm-5.3", "max_tokens": 100,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"tools":    tools,
	})
}

func TestClientToolsPreserved(t *testing.T) {
	tools := []any{
		map[string]any{"name": "read_file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
		map[string]any{"type": "custom", "name": "mcp__search__web_search", "input_schema": map[string]any{"type": "object"}, "cache_control": map[string]any{"type": "ephemeral"}},
		map[string]any{"name": "web_search", "input_schema": map[string]any{"type": "object"}},
	}
	result, err := transformExecution(executorRequest{Payload: toolRequest(tools)}, defaultConfig(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encode(result["tools"]), encode(tools)) {
		t.Fatal("client-executed tools changed")
	}
	result, err = transformExecution(executorRequest{Payload: toolRequest([]any{})}, defaultConfig(), "session")
	if err != nil || len(result["tools"].([]any)) != 0 {
		t.Fatal("empty tool list rejected", err)
	}
}

func TestInvalidToolDefinitions(t *testing.T) {
	for _, tt := range []struct {
		name, message string
		tools         any
	}{
		{"null", "tools must be an array", nil},
		{"object", "tools must be an array", map[string]any{}},
		{"entry", "tools[0] must be an object", []any{true}},
		{"missing name", "tools[0].name must be a non-empty string", []any{map[string]any{"input_schema": map[string]any{}}}},
		{"empty name", "tools[0].name must be a non-empty string", []any{map[string]any{"name": " \t", "input_schema": map[string]any{}}}},
		{"missing schema", "tools[0].input_schema must be an object", []any{map[string]any{"name": "read_file"}}},
		{"array schema", "tools[0].input_schema must be an object", []any{map[string]any{"name": "read_file", "input_schema": []any{}}}},
		{"invalid type", "tools[0].type must be a non-empty string when present", []any{map[string]any{"type": false, "name": "read_file", "input_schema": map[string]any{}}}},
		{"empty type", "tools[0].type must be a non-empty string when present", []any{map[string]any{"type": "", "name": "read_file", "input_schema": map[string]any{}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := transformExecution(executorRequest{Payload: toolRequest(tt.tools)}, defaultConfig(), "session")
			var api *apiError
			if !errors.As(err, &api) || api.Status != 400 || api.Code != "invalid_request" || api.Message != tt.message {
				t.Fatalf("invalid tool error = %v, want %s", err, tt.message)
			}
		})
	}
}

func TestNativeToolsRejectedBeforeCallbacks(t *testing.T) {
	for _, typ := range []string{"web_search_20250305", "web_search_20260209", "web_fetch_20250910", "bash_20250124", "synthetic-private-type"} {
		for _, schema := range []bool{false, true} {
			t.Run(typ+map[bool]string{false: "/native", true: "/forged-schema"}[schema], func(t *testing.T) {
				pub, priv, _ := ed25519.GenerateKey(rand.Reader)
				h := &fakeHost{private: priv, public: pub, reads: map[string]int{}}
				r := newRuntime(h)
				defer r.stop()
				if _, err := r.dispatch("plugin.register", managementRegistration(map[string]any{"host_logging_disabled": true})); err != nil {
					t.Fatal(err)
				}
				tool := map[string]any{"type": typ, "name": "synthetic-private-name"}
				if schema {
					tool["input_schema"] = map[string]any{"type": "object"}
				}
				req := executorRequest{RequestID: "native-tool", CallbackID: "callback", AuthID: "synthetic-auth", Format: "claude", StorageJSON: inlineStorage(), Payload: toolRequest([]any{tool})}
				_, err := r.execute(req)
				var api *apiError
				if !errors.As(err, &api) || api.Status != 400 || api.Code != "unsupported_tools" || rpcErrorCode(api) != "request_scoped" {
					t.Fatalf("native tool error = %v", err)
				}
				if !strings.Contains(api.Message, "client-executed") || strings.Contains(api.Message, "synthetic-private") {
					t.Fatal("tool error lacks remediation or echoes caller-controlled values")
				}
				if strings.HasPrefix(typ, "web_search_") && !strings.Contains(api.Message, "provider-native WebSearch") {
					t.Fatal("web search error lacks client capability guidance")
				}
				if h.handshakes != 0 || h.models != 0 || h.closed != 0 || len(r.active) != 0 {
					t.Fatal("rejected tool reached a callback or retained an execution")
				}
			})
		}
	}
}

func TestNativeToolHistoryRejected(t *testing.T) {
	for _, typ := range []string{"server_tool_use", "web_search_tool_result", "web_fetch_tool_result"} {
		body := map[string]any{
			"model": "glm-5.3", "max_tokens": 100,
			"messages": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": typ}}}},
		}
		_, err := transformExecution(executorRequest{Payload: encode(body)}, defaultConfig(), "session")
		var api *apiError
		if !errors.As(err, &api) || api.Status != 400 || api.Code != "unsupported_content" || !strings.Contains(api.Message, "new conversation") {
			t.Fatalf("native history error = %v", err)
		}
	}
}

func TestRemovedOriginalNativeToolsAreNotRestored(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`)
	original := []byte(`{"tools":[{"type":"web_search","name":"web_search"}]}`)
	result, err := transformExecution(executorRequest{Payload: body, OriginalRequest: original}, defaultConfig(), "session")
	if err != nil || result["tools"] != nil {
		t.Fatal("original tool definition restored or rejected a normalized request", err)
	}
}
