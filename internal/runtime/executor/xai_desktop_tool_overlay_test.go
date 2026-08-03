package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexapptools"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAIExecutorCodexDesktopToolOverlayRoundTrip(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		upstreamBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"name\":\"codex_app__read_thread\",\"call_id\":\"call_read_1\",\"arguments\":\"{\\\"threadId\\\":\\\"019fc266\\\",\\\"turnLimit\\\":3}\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_overlay\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	payload := xaiCodexDesktopToolOverlayRequest(t)
	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	response, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	tool := gjson.GetBytes(upstreamBody, `tools.#(name=="codex_app__read_thread")`)
	if !tool.Exists() {
		t.Fatalf("flattened codex_app tool missing: %s", upstreamBody)
	}
	if got := tool.Get("type").String(); got != "function" {
		t.Fatalf("upstream tool type = %q, want function", got)
	}
	if got := tool.Get("parameters.properties.turnLimit.maximum").Int(); got != 10 {
		t.Fatalf("turnLimit.maximum = %d, want 10; tool=%s", got, tool.Raw)
	}
	if got := tool.Get("parameters.properties.maxOutputCharsPerItem.maximum").Int(); got != 20000 {
		t.Fatalf("maxOutputCharsPerItem.maximum = %d, want 20000; tool=%s", got, tool.Raw)
	}

	output := gjson.GetBytes(response.Payload, "output.0")
	if got := output.Get("name").String(); got != "read_thread" {
		t.Fatalf("restored name = %q, want read_thread; response=%s", got, response.Payload)
	}
	if got := output.Get("namespace").String(); got != "codex_app" {
		t.Fatalf("restored namespace = %q, want codex_app; response=%s", got, response.Payload)
	}
	if got := output.Get("call_id").String(); got != "call_read_1" {
		t.Fatalf("call_id = %q, want call_read_1; response=%s", got, response.Payload)
	}
	if got := output.Get("arguments").String(); got != `{"threadId":"019fc266","turnLimit":3}` {
		t.Fatalf("arguments = %q; response=%s", got, response.Payload)
	}
}

func xaiCodexDesktopToolOverlayRequest(t *testing.T) []byte {
	t.Helper()
	definitions, err := codexapptools.Select([]string{"read_thread"})
	if err != nil || len(definitions) != 1 {
		t.Fatalf("select read_thread: definitions=%v error=%v", definitions, err)
	}
	definition := definitions[0]
	payload, err := json.Marshal(map[string]any{
		"model": "grok-4.5",
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
	return payload
}
