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
