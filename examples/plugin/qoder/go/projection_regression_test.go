package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func projectionEvent(seq uint64, kind pluginapi.AgentEventType, payload any) pluginapi.AgentEventV1 {
	raw, _ := json.Marshal(payload)
	return pluginapi.AgentEventV1{SchemaVersion: 1, Sequence: seq, Type: kind, RequestID: "regression", Provider: "qoder", Timestamp: time.Now(), Payload: raw}
}
func TestQoderProjectionRetainsReasoningAndFinishReason(t *testing.T) {
	for _, reason := range []string{"length", "content_filter", "stop"} {
		p := newEventProjection("regression", "fixture")
		ev := projectionEvent(1, pluginapi.AgentEventReasoningDelta, map[string]any{"text": "reason "})
		chunk, _, err := p.streamChunk(ev)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err = json.Unmarshal(chunk, &parsed); err != nil {
			t.Fatal(err)
		}
		delta := parsed["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		if delta["reasoning_content"] != "reason " {
			t.Fatalf("reasoning dropped: %s", chunk)
		}
		p.consume(projectionEvent(2, pluginapi.AgentEventMessageDelta, map[string]any{"text": "answer"}))
		last, done, err := p.streamChunk(projectionEvent(3, pluginapi.AgentEventTurnCompleted, map[string]any{"state": "completed", "finish_reason": reason}))
		if err != nil || !done {
			t.Fatalf("terminal %v %v", done, err)
		}
		if err = json.Unmarshal(last, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed["choices"].([]any)[0].(map[string]any)["finish_reason"] != reason {
			t.Fatalf("stream finish lost: %s", last)
		}
		response, err := p.nonStreamResponse()
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(response.Payload, &parsed); err != nil {
			t.Fatal(err)
		}
		choice := parsed["choices"].([]any)[0].(map[string]any)
		message := choice["message"].(map[string]any)
		if choice["finish_reason"] != reason || message["reasoning_content"] != "reason " || message["content"] != "answer" {
			t.Fatalf("non-stream loss: %s", response.Payload)
		}
	}
}
func TestQoderProjectionPreservesLateToolIdentityAndText(t *testing.T) {
	p := newEventProjection("regression", "fixture")
	events := []pluginapi.AgentEventV1{
		projectionEvent(1, pluginapi.AgentEventMessageDelta, map[string]any{"text": "calling"}),
		projectionEvent(2, pluginapi.AgentEventToolStarted, map[string]any{"index": 0}),
		projectionEvent(3, pluginapi.AgentEventToolUpdated, map[string]any{"index": 0, "tool_call_id": "call-late", "name": "lookup", "partial_json": "{\"q\":\"hello "}),
		// Native SDK tool updates have no index and must not corrupt client tool 0.
		projectionEvent(4, pluginapi.AgentEventToolUpdated, map[string]any{"partial_json": "corrupt"}),
		projectionEvent(5, pluginapi.AgentEventToolUpdated, map[string]any{"index": 0, "partial_json": "world\"}"}),
		projectionEvent(6, pluginapi.AgentEventTurnCompleted, map[string]any{"state": "completed"}),
	}
	for _, event := range events {
		if _, _, err := p.streamChunk(event); err != nil {
			t.Fatal(err)
		}
	}
	response, err := p.nonStreamResponse()
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(response.Payload, &body); err != nil {
		t.Fatal(err)
	}
	choice := body["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	call := message["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if message["content"] != "calling" || call["id"] != "call-late" || fn["name"] != "lookup" || fn["arguments"] != "{\"q\":\"hello world\"}" || choice["finish_reason"] != "tool_calls" {
		t.Fatalf("tool lost: %s", response.Payload)
	}
}

func TestQoderProjectionRequestErrorsHaveClientStatus(t *testing.T) {
	for code, want := range map[string]int{"invalid_request": 400, "direct_request_too_large": 413, "frame_too_large": 413} {
		p := newEventProjection("regression", "fixture")
		p.terminal = &pluginapi.AgentTerminalPayloadV1{State: pluginapi.AgentTerminalFailed, Code: code}
		err, ok := p.terminalError().(*pluginCallError)
		if !ok || err.statusCode != want {
			t.Fatalf("%s: %#v", code, err)
		}
	}
}
