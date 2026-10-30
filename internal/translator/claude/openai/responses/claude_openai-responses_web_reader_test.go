package responses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

const readerResultContent = `[{"url":"https://example.com/doc","content":"Full page\n第二行","title":"Reference","metadata":{"language":"zh"}}]`

func readerFixture(t *testing.T) ([]string, string) {
	t.Helper()
	result, err := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": "call_reader", "content": readerResultContent})
	if err != nil {
		t.Fatal(err)
	}
	blocks := []string{
		`{"type":"text","text":"Before"}`,
		`{"type":"server_tool_use","id":"call_reader","name":"webReader","input":{"url":"https://example.com/doc"}}`,
		`{"type":"text","text":"Output:"}`,
		string(result),
		`{"type":"text","text":"Answer"}`,
	}
	message := `{"type":"message","id":"reader","role":"assistant","model":"glm-5.3","stop_reason":"end_turn","content":[` + strings.Join(blocks, ",") + `],"usage":{"input_tokens":11,"output_tokens":7}}`
	events := []string{`{"type":"message_start","message":{"type":"message","id":"reader","role":"assistant","content":[],"usage":{"input_tokens":11,"output_tokens":0}}}`}
	for index, block := range blocks {
		startBlock := block
		if gjson.Get(block, "type").String() == "text" {
			startBlock = `{"type":"text","text":""}`
		}
		start, _ := json.Marshal(map[string]any{"type": "content_block_start", "index": index, "content_block": json.RawMessage(startBlock)})
		stop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": index})
		events = append(events, string(start))
		if startBlock != block {
			delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": gjson.Get(block, "text").String()}})
			events = append(events, string(delta))
		}
		events = append(events, string(stop))
	}
	events = append(events, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`, `{"type":"message_stop"}`)
	return events, message
}

func assertReaderOutput(t *testing.T, output gjson.Result) gjson.Result {
	t.Helper()
	items := output.Array()
	if len(items) != 4 {
		t.Fatalf("output count=%d, want 4: %s", len(items), output.Raw)
	}
	reader := items[1]
	if reader.Get("type").String() != "web_search_call" || reader.Get("action.type").String() != "open_page" || reader.Get("action.url").String() != "https://example.com/doc" {
		t.Fatalf("reader action lost: %s", reader.Raw)
	}
	if reader.Get("results.type").String() != "tool_result" || reader.Get("results.content").String() != readerResultContent {
		t.Fatalf("reader result lost: %s", reader.Raw)
	}
	if items[0].Get("content.0.text").String() != "Before" || items[2].Get("content.0.text").String() != "Output:" || items[3].Get("content.0.text").String() != "Answer" {
		t.Fatalf("content order changed: %s", output.Raw)
	}
	return reader
}

func TestWebReaderResponsesStreamAndAggregate(t *testing.T) {
	events, message := readerFixture(t)
	state := &PluginResponseState{}
	var param any = state
	var completed gjson.Result
	done := 0
	for _, event := range events {
		for _, frame := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "glm-5.3", nil, nil, []byte("data: "+event), &param) {
			name, data := parseClaudeResponsesSSEEvent(t, frame)
			if name == "response.output_item.done" && data.Get("item.type").String() == "web_search_call" {
				done++
				if data.Get("item.action.type").String() != "open_page" {
					t.Fatal("done action changed")
				}
			}
			if name == "response.completed" {
				completed = data.Get("response")
			}
		}
	}
	if state.Err != nil || !state.Terminal {
		t.Fatalf("reader stream rejected: %v", state.Err)
	}
	assertReaderOutput(t, completed.Get("output"))
	if done != 1 || completed.Get("usage.total_tokens").Int() != 18 {
		t.Fatalf("terminal/usage mismatch: done=%d response=%s", done, completed.Raw)
	}
	for _, tc := range []struct{ name, body string }{
		{"JSON", message}, {"SSE", "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p any = &PluginResponseState{}
			out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "glm-5.3", nil, nil, []byte(tc.body), &p)
			assertReaderOutput(t, gjson.GetBytes(out, "output"))
			if gjson.GetBytes(out, "usage.total_tokens").Int() != 18 {
				t.Fatal("aggregate usage lost")
			}
		})
	}
}

func TestWebReaderResponsesHistoryAndContextCount(t *testing.T) {
	_, message := readerFixture(t)
	var param any = &PluginResponseState{}
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "glm-5.3", nil, nil, []byte(message), &param)
	reader := assertReaderOutput(t, gjson.GetBytes(out, "output"))
	model := &registry.ModelInfo{ID: "glm-5.3", IsCompat: true, NativeCapabilities: &registry.NativeCapabilities{WebSearch: boolPtr(true), WebSearchReplay: registry.NativeWebSearchReplayBigModel}}
	request := responsesRequestFromItems(`{"type":"message","role":"user","content":"Read"}`, reader.Raw, `{"type":"message","role":"user","content":"Continue"}`)
	replayed, err := ConvertOpenAIResponsesRequestToClaudeWithModelInfo("glm-5.3", request, false, model)
	if err != nil {
		t.Fatal(err)
	}
	check := func(body []byte) {
		t.Helper()
		var use, result gjson.Result
		for _, msg := range gjson.GetBytes(body, "messages").Array() {
			for _, block := range msg.Get("content").Array() {
				switch block.Get("type").String() {
				case "server_tool_use":
					use = block
				case "tool_result":
					result = block
				}
			}
		}
		if use.Get("name").String() != "webReader" || use.Get("input.url").String() != "https://example.com/doc" || result.Get("content").String() != readerResultContent || use.Get("id").String() != result.Get("tool_use_id").String() {
			t.Fatalf("reader replay lost: %s", body)
		}
	}
	check(replayed)
	countBody, err := AppendResponseOutputForTokenCount("glm-5.3", []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"Read"}]}`), []byte(gjson.GetBytes(out, "output").Raw), model)
	if err != nil {
		t.Fatal(err)
	}
	check(countBody)
	// A native Anthropic route must not receive a BigModel-only tool name.
	other, err := ConvertOpenAIResponsesRequestToClaudeWithModelInfo("claude-test", request, false, &registry.ModelInfo{ID: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(other), `"name":"webReader"`) {
		t.Fatal("BigModel reader replay leaked into native Claude")
	}
}
