package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPluginResponseStateToolsAndThinking(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		state := &PluginResponseState{}
		var param any = state
		reason := "tool_use"
		args := `{"q":"x"}`
		if truncated {
			reason = "max_tokens"
			args = `{"q":`
		}
		events := []string{
			`{"type":"message_start","message":{"type":"message","id":"mixed","role":"assistant","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"synthetic-"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":{}}}`,
		}
		if truncated {
			events = append(events, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`)
		} else {
			events = append(events, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"x\"}"}}`)
		}
		events = append(events, `{"type":"content_block_stop","index":1}`, `{"type":"message_delta","delta":{"stop_reason":"`+reason+`"},"usage":{"output_tokens":8}}`, `{"type":"message_stop"}`)
		var last []byte
		for _, ev := range events {
			for _, frame := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "fixture", nil, nil, []byte("data: "+ev), &param) {
				for _, line := range strings.Split(string(frame), "\n") {
					if strings.HasPrefix(line, "data:") {
						last = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
					}
				}
			}
		}
		if state.Err != nil || !state.Terminal {
			t.Fatalf("state=%+v", state)
		}
		if gjson.GetBytes(last, "response.output.0.type").String() != "reasoning" || gjson.GetBytes(last, "response.output.1.call_id").String() != "call-1" || gjson.GetBytes(last, "response.output.1.arguments").String() != args {
			t.Fatal("tool/reasoning semantics lost")
		}
		if gjson.GetBytes(last, "response.output.0.encrypted_content").String() != "synthetic-opaque" {
			t.Error("fragmented thinking signature lost")
		}
		transcript := "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
		var aggregateParam any
		aggregate := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "fixture", nil, nil, []byte(transcript), &aggregateParam)
		if gjson.GetBytes(aggregate, "output.0.encrypted_content").String() != "synthetic-opaque" {
			t.Error("aggregate thinking signature lost")
		}
		want := "completed"
		if truncated {
			want = "incomplete"
		}
		if gjson.GetBytes(last, "response.status").String() != want {
			t.Error("wrong terminal state")
		}
	}
}

func TestPluginResponseStateInitialContent(t *testing.T) {
	state := &PluginResponseState{}
	var param any = state
	var last []byte
	for _, event := range []string{
		`{"type":"message_start","message":{"type":"message","id":"seed","role":"assistant","content":[]}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"initial"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"lookup","input":{"q":"seed"}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		`{"type":"message_stop"}`,
	} {
		for _, frame := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "fixture", nil, nil, []byte("data: "+event), &param) {
			for _, line := range strings.Split(string(frame), "\n") {
				if strings.HasPrefix(line, "data:") {
					last = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				}
			}
		}
	}
	if state.Err != nil || gjson.GetBytes(last, "response.output.0.content.0.text").String() != "initial" || gjson.GetBytes(last, "response.output.1.arguments").String() != `{"q":"seed"}` {
		t.Fatal("initial stream content lost")
	}
}

func TestPluginResponseStateRejectsMalformedContent(t *testing.T) {
	for _, event := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"unknown","text":"lost"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"orphan"}}`,
		`{"type":"message_stop"}`,
	} {
		state := &PluginResponseState{}
		var param any = state
		ConvertClaudeResponseToOpenAIResponses(context.Background(), "fixture", nil, nil, []byte(`data: {"type":"message_start","message":{"type":"message","id":"fixture","role":"assistant","content":[]}}`), &param)
		ConvertClaudeResponseToOpenAIResponses(context.Background(), "fixture", nil, nil, []byte("data: "+event), &param)
		if state.Err == nil {
			t.Error("malformed content accepted")
		}
	}
}

// TestPluginResponseStopReasons covers the stop reasons that previously failed
// the plugin validator with "invalid or unsupported Anthropic plugin response"
// even though the translator maps them: GLM's refusal/sensitive safety stops
// and Anthropic's pause_turn / model_context_window_exceeded.
func TestPluginResponseStopReasons(t *testing.T) {
	for _, tc := range []struct {
		stop       string
		wantStatus string
		wantEvent  string
	}{
		{stop: "end_turn", wantStatus: "completed", wantEvent: "response.completed"},
		{stop: "tool_use", wantStatus: "completed", wantEvent: "response.completed"},
		{stop: "max_tokens", wantStatus: "incomplete", wantEvent: "response.incomplete"},
		{stop: "refusal", wantStatus: "incomplete", wantEvent: "response.incomplete"},
		{stop: "sensitive", wantStatus: "incomplete", wantEvent: "response.incomplete"},
		{stop: "pause_turn", wantStatus: "completed", wantEvent: "response.completed"},
		// Upstream #6862: a filled context window maps to incomplete
		// max_output_tokens like max_tokens, preserving truncated content,
		// tool calls and usage instead of failing the response.
		{stop: "model_context_window_exceeded", wantStatus: "incomplete", wantEvent: "response.incomplete"},
	} {
		state := &PluginResponseState{}
		var param any = state
		var last []byte
		for _, event := range []string{
			`{"type":"message_start","message":{"type":"message","id":"stop-fixture","role":"assistant","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"` + tc.stop + `"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		} {
			for _, frame := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "fixture", nil, nil, []byte("data: "+event), &param) {
				for _, line := range strings.Split(string(frame), "\n") {
					if strings.HasPrefix(line, "data:") {
						last = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
					}
				}
			}
		}
		if state.Err != nil || !state.Terminal {
			t.Fatalf("stop=%q state=%+v", tc.stop, state)
		}
		if got := gjson.GetBytes(last, "type").String(); got != tc.wantEvent {
			t.Errorf("stop=%q terminal event=%q want %q", tc.stop, got, tc.wantEvent)
		}
		if got := gjson.GetBytes(last, "response.status").String(); got != tc.wantStatus {
			t.Errorf("stop=%q response.status=%q want %q", tc.stop, got, tc.wantStatus)
		}
		switch tc.stop {
		case "refusal", "sensitive":
			if got := gjson.GetBytes(last, "response.incomplete_details.reason").String(); got != "content_filter" {
				t.Errorf("stop=%q incomplete_details.reason=%q want content_filter", tc.stop, got)
			}
		case "model_context_window_exceeded":
			if got := gjson.GetBytes(last, "response.incomplete_details.reason").String(); got != "max_output_tokens" {
				t.Errorf("stop=%q incomplete_details.reason=%q want max_output_tokens", tc.stop, got)
			}
		case "pause_turn":
			if gjson.GetBytes(last, "response.end_turn").Exists() && gjson.GetBytes(last, "response.end_turn").Bool() {
				t.Errorf("stop=pause_turn end_turn must be false")
			}
		}
		// The non-stream aggregate must agree with the streamed terminal state.
		transcript := []string{
			`{"type":"message_start","message":{"type":"message","id":"stop-fixture","role":"assistant","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"` + tc.stop + `"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		var aggregateParam any = &PluginResponseState{}
		aggregate := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "fixture", nil, nil, []byte("data: "+strings.Join(transcript, "\n\ndata: ")+"\n\n"), &aggregateParam)
		if !gjson.GetBytes(aggregate, "status").Exists() {
			t.Fatalf("stop=%q aggregate missing status", tc.stop)
		}
		if got := gjson.GetBytes(aggregate, "status").String(); got != tc.wantStatus {
			t.Errorf("stop=%q aggregate status=%q want %q", tc.stop, got, tc.wantStatus)
		}
	}
}

// TestPluginResponsePauseTurnSearchStatus verifies a pause_turn response keeps
// an unfinished server search in_progress, and that a continuation response
// delivering the orphan web_search_tool_result under the same id is accepted
// and surfaces the result.
func TestPluginResponsePauseTurnSearchStatus(t *testing.T) {
	state := &PluginResponseState{}
	var param any = state
	var items []string
	for _, event := range []string{
		`{"type":"message_start","message":{"type":"message","id":"pause-fixture","role":"assistant","content":[],"usage":{"input_tokens":9,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_p1","name":"web_search","input":{"query":"evidence"}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	} {
		for _, frame := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "fixture", nil, nil, []byte("data: "+event), &param) {
			for _, line := range strings.Split(string(frame), "\n") {
				if strings.HasPrefix(line, "data:") {
					items = append(items, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				}
			}
		}
	}
	if state.Err != nil || !state.Terminal {
		t.Fatalf("paused turn rejected: %+v", state)
	}
	done := ""
	for _, item := range items {
		if gjson.Get(item, "type").String() == "response.output_item.done" && gjson.Get(item, "item.type").String() == "web_search_call" {
			done = item
		}
	}
	if done == "" {
		t.Fatal("paused web_search_call item never finalized")
	}
	if got := gjson.Get(done, "item.status").String(); got != "in_progress" {
		t.Fatalf("paused search status=%q want in_progress", got)
	}

	continuation := &PluginResponseState{}
	var cParam any = continuation
	var cItems []string
	for _, event := range []string{
		`{"type":"message_start","message":{"type":"message","id":"pause-cont","role":"assistant","content":[],"usage":{"input_tokens":9,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_p1","content":[{"type":"web_search_result","url":"https://example.com/pause","title":"Evidence"}]}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"summary"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		`{"type":"message_stop"}`,
	} {
		for _, frame := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "fixture", nil, nil, []byte("data: "+event), &cParam) {
			for _, line := range strings.Split(string(frame), "\n") {
				if strings.HasPrefix(line, "data:") {
					cItems = append(cItems, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				}
			}
		}
	}
	if continuation.Err != nil || !continuation.Terminal {
		t.Fatalf("orphan result continuation rejected: %+v", continuation)
	}
	sawResult := false
	for _, item := range cItems {
		if gjson.Get(item, "type").String() == "response.output_item.done" && gjson.Get(item, "item.type").String() == "web_search_call" {
			if gjson.Get(item, "item.results.0.url").String() == "https://example.com/pause" {
				sawResult = true
			}
		}
	}
	if !sawResult {
		t.Fatal("continuation dropped the orphan search result")
	}
}
