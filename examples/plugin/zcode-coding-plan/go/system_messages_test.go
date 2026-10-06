package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestNativeSystemMessagesPreserved(t *testing.T) {
	for _, tt := range []struct {
		name, messages string
	}{
		{"text", `[{"role":"user","content":"Summarize the conversation"},{"role":"system","content":"保留文件路径与待办事项。\nDo not call tools."}]`},
		{"text blocks", `[{"role":"user","content":"Summarize"},{"role":"system","content":[{"type":"text","text":"Preserve <tags> and \\paths","cache_control":{"type":"ephemeral"}}]}]`},
		{"consecutive system messages", `[{"role":"user","content":"Summarize"},{"role":"system","content":"Keep decisions"},{"role":"system","content":[{"type":"text","text":"Keep unresolved questions"}]}]`},
		{"turn scoped history", `[{"role":"user","content":"First turn"},{"role":"system","content":"Old reminder","clear_at":"next_user_message"},{"role":"assistant","content":"Done"},{"role":"user","content":"Summarize"},{"role":"system","content":[{"type":"text","text":"Current reminder"}],"clear_at":"next_user_message"}]`},
		{"per message effort", `[{"role":"system","content":[],"output_config":{"effort":"medium"}},{"role":"user","content":"First turn"},{"role":"assistant","content":"Done"},{"role":"system","content":[],"output_config":{"effort":"xhigh"}},{"role":"user","content":"Summarize"}]`},
		{"tool result history", `[{"role":"user","content":"Read the file"},{"role":"assistant","content":[{"type":"thinking","thinking":"Read first","signature":"synthetic-opaque"},{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"File contents"}]},{"role":"system","content":"Summarize without more tools"}]`},
		{"tool changes", `[{"role":"user","content":"Summarize"},{"role":"system","content":[{"type":"text","text":"Only use the available tools"},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}}},{"type":"tool_removal","tool":{"type":"tool_reference","name":"read_file"}},{"type":"tool_addition","tool":{"type":"tool_reference","name":"read_file"}}]}]`},
		{"MCP tool references", `[{"role":"user","content":"Summarize"},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"mcp_tool_reference","server_name":"synthetic-server","name":"lookup"}},{"type":"tool_removal","tool":{"type":"mcp_toolset_reference","server_name":"synthetic-server"}}]}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"glm-5.3","max_tokens":100,"system":[{"type":"text","text":"Keep the caller system prompt"}],"messages":%s}`, tt.messages))
			before := bytes.Clone(body)
			var want map[string]any
			if err := json.Unmarshal(body, &want); err != nil {
				t.Fatal(err)
			}
			for _, original := range [][]byte{nil, body} {
				got, err := transformExecution(executorRequest{Format: "claude", Payload: body, OriginalRequest: original}, defaultConfig(), "synthetic-session")
				if err != nil {
					t.Fatal(err)
				}
				delete(got, "metadata")
				if !reflect.DeepEqual(got, want) {
					t.Fatal("native message structure, controls, or top-level system changed")
				}
				if !bytes.Equal(body, before) {
					t.Fatal("caller payload was mutated")
				}
			}
		})
	}
}

func TestNativeSystemMessagesDoNotRestoreOriginalHistory(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","max_tokens":100,"messages":[{"role":"user","content":"Summarize"}]}`)
	original := []byte(`{"model":"glm-5.3","max_tokens":100,"messages":[{"role":"user","content":"Summarize"},{"role":"system","content":"synthetic-removed"}]}`)
	got, err := transformExecution(executorRequest{Payload: body, OriginalRequest: original}, defaultConfig(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encode(got), []byte("synthetic-removed")) || len(got["messages"].([]any)) != 1 {
		t.Fatal("original history was restored")
	}
}

func TestInvalidNativeSystemMessagesRejectedBeforeCallbacks(t *testing.T) {
	for _, tt := range []struct {
		name, messages, code string
	}{
		{"unknown role", `[{"role":"synthetic-private-role","content":"private"}]`, "invalid_request"},
		{"developer role", `[{"role":"developer","content":"private"}]`, "invalid_request"},
		{"non-object message", `[true]`, "invalid_request"},
		{"invalid content", `[{"role":"system","content":true}]`, "unsupported_content"},
		{"invalid text", `[{"role":"system","content":[{"type":"text","text":42}]}]`, "invalid_request"},
		{"image in system", `[{"role":"system","content":[{"type":"image","source":{"type":"url","url":"https://example.invalid/image.png"}}]}]`, "unsupported_content"},
		{"thinking in system", `[{"role":"system","content":[{"type":"thinking","thinking":"private"}]}]`, "unsupported_content"},
		{"missing tool reference", `[{"role":"system","content":[{"type":"tool_removal"}]}]`, "invalid_request"},
		{"empty tool name", `[{"role":"system","content":[{"type":"tool_removal","tool":{"type":"tool_reference","name":" "}}]}]`, "invalid_request"},
		{"missing MCP server", `[{"role":"system","content":[{"type":"tool_removal","tool":{"type":"mcp_toolset_reference"}}]}]`, "invalid_request"},
		{"unknown tool reference", `[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"synthetic-private-reference"}}]}]`, "unsupported_content"},
		{"definition in removal", `[{"role":"system","content":[{"type":"tool_removal","tool":{"type":"tool_definition","definition":{"name":"lookup","input_schema":{}}}}]}]`, "invalid_request"},
		{"malformed inline tool", `[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"synthetic-private-name"}}}]}]`, "invalid_request"},
		{"unsupported inline server tool", `[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"type":"web_fetch_20250910","name":"synthetic-private-name","input_schema":{}}}}]}]`, "unsupported_tools"},
		{"unpaired history", `[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]},{"role":"system","content":"Summarize"}]`, "invalid_request"},
		{"tool change in user", `[{"role":"user","content":[{"type":"tool_removal","tool":{"type":"tool_reference","name":"lookup"}}]}]`, "unsupported_content"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			h := &fakeHost{private: priv, public: pub, reads: map[string]int{}}
			r := newRuntime(h)
			defer r.stop()
			if _, err = r.dispatch("plugin.register", managementRegistration(map[string]any{"host_logging_disabled": true})); err != nil {
				t.Fatal(err)
			}
			body := []byte(fmt.Sprintf(`{"model":"glm-5.3","max_tokens":100,"messages":%s}`, tt.messages))
			_, err = r.execute(executorRequest{RequestID: "system-message", CallbackID: "callback", AuthID: "synthetic-auth", Format: "claude", StorageJSON: inlineStorage(), Payload: body})
			var api *apiError
			if !errors.As(err, &api) || api.Status != 400 || api.Code != tt.code || rpcErrorCode(api) != "request_scoped" {
				t.Fatalf("error = %v, want request-scoped 400 %s", err, tt.code)
			}
			if strings.Contains(api.Message, "synthetic-private") {
				t.Fatal("error echoed caller content")
			}
			if h.handshakes != 0 || h.models != 0 || len(r.active) != 0 {
				t.Fatal("invalid request reached upstream or retained an execution")
			}
		})
	}
}
