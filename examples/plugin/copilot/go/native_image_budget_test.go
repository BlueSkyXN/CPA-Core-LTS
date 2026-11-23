package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	claudechat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/claude"
	responses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type budgetObj = map[string]any

func budgetJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Three source protocols, the same history: two answered batches followed by the
// current parallel batch, a non-image tool result and an optional trailing message.
func budgetFixture(t *testing.T, format string, batches []int, trailing string) pluginapi.ExecutorRequest {
	t.Helper()
	var source []any
	n := 0
	for turn, count := range batches {
		source = append(source, budgetObj{"role": "user", "content": "run the tools"})
		var calls, outputs, chatCalls, results, carriers []any
		for i := 0; i <= count; i++ {
			id := fmt.Sprintf("batch-%d-%d", turn, i)
			chatCalls = append(chatCalls, budgetObj{"id": id, "type": "function", "function": budgetObj{"name": "Read", "arguments": "{}"}})
			var pic budgetObj
			if i < count {
				n++
				u := fmt.Sprintf("https://example.com/fixture-%02d.png", n)
				pic = budgetObj{"type": "image_url", "image_url": budgetObj{"url": u, "detail": "low"}}
			}
			switch format {
			case "openai-response":
				calls = append(calls, budgetObj{"type": "function_call", "call_id": id, "name": "Read", "arguments": "{}"})
				var out any = "text result"
				if pic != nil {
					out = []any{budgetObj{"type": "input_image", "image_url": pic["image_url"].(budgetObj)["url"], "detail": "low"}}
				}
				outputs = append(outputs, budgetObj{"type": "function_call_output", "call_id": id, "output": out})
			case "claude":
				calls = append(calls, budgetObj{"type": "tool_use", "id": id, "name": "Read", "input": budgetObj{}})
				var out any = "text result"
				if pic != nil {
					out = []any{budgetObj{"type": "image", "source": budgetObj{"type": "url", "url": pic["image_url"].(budgetObj)["url"]}}}
				}
				outputs = append(outputs, budgetObj{"type": "tool_result", "tool_use_id": id, "content": out})
			case "openai":
				results = append(results, budgetObj{"role": "tool", "tool_call_id": id, "content": "tool result"})
				if pic != nil {
					carriers = append(carriers, budgetObj{"role": "user", "content": []any{pic}})
				}
			}
		}
		switch format {
		case "openai-response":
			source = append(source, calls...)
			source = append(source, outputs...)
		case "claude":
			source = append(source, budgetObj{"role": "assistant", "content": calls}, budgetObj{"role": "user", "content": outputs})
		case "openai":
			source = append(source, budgetObj{"role": "assistant", "tool_calls": chatCalls})
			source = append(source, results...)
			source = append(source, carriers...)
		}
		if turn < len(batches)-1 {
			source = append(source, budgetObj{"role": "assistant", "content": "Reviewed this batch."})
		}
	}
	if trailing != "" {
		source = append(source, budgetObj{"role": "user", "content": trailing})
	}
	key := "messages"
	if format == "openai-response" {
		key = "input"
	}
	raw := budgetJSON(t, budgetObj{key: source, "model": carbonImageModel, "max_tokens": 32})
	chat := raw
	var err error
	switch format {
	case "openai-response":
		chat, err = responses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(carbonImageModel, raw, true)
	case "claude":
		chat, err = claudechat.ConvertClaudeRequestToOpenAI(carbonImageModel, raw, true)
	}
	if err != nil {
		t.Fatal(err)
	}
	return pluginapi.ExecutorRequest{Format: "chat-completions", SourceFormat: format, Model: carbonImageModel, Payload: chat, OriginalRequest: raw}
}

func budgetWire(t *testing.T, req pluginapi.ExecutorRequest) ([]any, map[string]any) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(req.Payload, &body); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	return msgs, body
}

func TestCarbonBudgetTranslatedProtocols(t *testing.T) {
	for _, format := range []string{"openai-response", "claude", "openai"} {
		t.Run(format, func(t *testing.T) {
			req := budgetFixture(t, format, []int{5, 2, 4}, "")
			msgs, body := budgetWire(t, req)
			var before []any
			for _, m := range msgs {
				v := m.(budgetObj)
				before = append(before, []any{v["role"], v["tool_call_id"], v["tool_calls"]})
			}
			if err := fitCarbonImages(req, msgs); err != nil {
				t.Fatal(err)
			}
			wire := string(budgetJSON(t, body))
			if strings.Contains(wire, "fixture-05.png") || strings.Count(wire, "fixture-") != 10 || strings.Count(wire, carbonImageOmitted) != 1 {
				t.Fatal("wrong occurrence omitted or typed image count")
			}
			for _, n := range []int{1, 4, 6, 7, 8, 9, 10, 11} {
				if !strings.Contains(wire, fmt.Sprintf("fixture-%02d.png", n)) {
					t.Fatal("selected image missing", n)
				}
			}
			var after []any
			for _, m := range msgs {
				v := m.(budgetObj)
				after = append(after, []any{v["role"], v["tool_call_id"], v["tool_calls"]})
			}
			if string(budgetJSON(t, before)) != string(budgetJSON(t, after)) {
				t.Fatal("tool batch or original message positions changed")
			}
			for _, suffix := range []string{"continue", "继续"} {
				next := budgetFixture(t, format, []int{5, 2, 4}, suffix)
				nmsgs, nb := budgetWire(t, next)
				if err := fitCarbonImages(next, nmsgs); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(budgetJSON(t, nb)), "fixture-05.png") {
					t.Fatal("text-only retry changed selection")
				}
			}
		})
	}
}

func TestCarbonBudgetRejectsInsteadOfGuessing(t *testing.T) {
	for _, format := range []string{"openai-response", "claude", "openai"} {
		for _, tc := range []struct {
			name    string
			batches []int
			text    string
		}{{"all-current", []int{11}, ""}, {"unknown-reference", []int{5, 2, 4}, "compare the top left corner with the first screenshot"}} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				req := budgetFixture(t, format, tc.batches, tc.text)
				msgs, body := budgetWire(t, req)
				before := string(budgetJSON(t, body))
				err := fitCarbonImages(req, msgs)
				e, ok := err.(*pluginCallError)
				if !ok || e.statusCode != http.StatusBadRequest || e.retryable || strings.Contains(e.Error(), "fixture-") {
					t.Fatalf("unexpected budget error: %v", err)
				}
				if string(budgetJSON(t, body)) != before {
					t.Fatal("failure partially mutated the wire")
				}
			})
		}
	}
}
