package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const maxCodeBuddyResponseBytes = 4 * 1024 * 1024

func (r *pluginRuntime) execute(raw []byte) (pluginapi.ExecutorResponse, error) {
	execution, err := r.openExecution(raw, false)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	defer func() {
		execution.closeUpstream(r.caller)
		r.releaseExecution(execution)
		execution.signalDone()
	}()
	collector := completionCollector{fields: make(map[string]json.RawMessage), choices: make(map[int]*collectedChoice)}
	if err = r.readExecution(execution, collector.accept); err != nil {
		var typed *pluginCallError
		if errors.As(err, &typed) {
			return pluginapi.ExecutorResponse{}, err
		}
		return pluginapi.ExecutorResponse{}, newPluginCallError("invalid_upstream_response", err.Error(), http.StatusBadGateway, false)
	}
	if execution.canceled() {
		return pluginapi.ExecutorResponse{}, newPluginCallError(codeBuddyConnectionLifecycleErrorCode, "CodeBuddy request canceled", 0, true)
	}
	body, err := collector.result()
	if err != nil {
		return pluginapi.ExecutorResponse{}, newPluginCallError("invalid_upstream_response", err.Error(), http.StatusBadGateway, false)
	}
	return pluginapi.ExecutorResponse{Payload: body, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

type collectedFunction struct {
	id, name, arguments strings.Builder
	kind                string
}

type collectedChoice struct {
	content, reasoning, refusal          strings.Builder
	hasContent, hasReasoning, hasRefusal bool
	finish                               string
	tools                                map[int]*collectedFunction
	function                             *collectedFunction
}

type completionCollector struct {
	bytes   int
	fields  map[string]json.RawMessage
	choices map[int]*collectedChoice
}

func (c *completionCollector) accept(raw []byte) error {
	c.bytes += len(raw)
	if c.bytes > maxCodeBuddyResponseBytes {
		return errors.New("CodeBuddy non-stream response exceeds the bounded limit")
	}
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil {
		return errors.New("CodeBuddy completion frame is invalid")
	}
	for _, key := range []string{"id", "created", "model", "system_fingerprint", "service_tier", "usage"} {
		if value := frame[key]; len(value) > 0 && string(value) != "null" {
			c.fields[key] = value
		}
	}
	var choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content   *string `json:"content"`
			Reasoning *string `json:"reasoning_content"`
			Refusal   *string `json:"refusal"`
			Function  *struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function_call"`
			Tools []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	}
	if value := frame["choices"]; len(value) > 0 && json.Unmarshal(value, &choices) != nil {
		return errors.New("CodeBuddy completion choices are invalid")
	}
	for _, value := range choices {
		if value.Index < 0 || value.Index >= 16 {
			return errors.New("CodeBuddy completion choice index exceeds the bounded limit")
		}
		choice := c.choices[value.Index]
		if choice == nil {
			choice = &collectedChoice{tools: make(map[int]*collectedFunction)}
			c.choices[value.Index] = choice
		}
		if value.FinishReason != "" {
			choice.finish = value.FinishReason
		}
		if value.Delta.Content != nil {
			choice.hasContent = true
			choice.content.WriteString(*value.Delta.Content)
		}
		if value.Delta.Reasoning != nil {
			choice.hasReasoning = true
			choice.reasoning.WriteString(*value.Delta.Reasoning)
		}
		if value.Delta.Refusal != nil {
			choice.hasRefusal = true
			choice.refusal.WriteString(*value.Delta.Refusal)
		}
		if fn := value.Delta.Function; fn != nil {
			if choice.function == nil {
				choice.function = &collectedFunction{}
			}
			choice.function.name.WriteString(fn.Name)
			choice.function.arguments.WriteString(fn.Arguments)
		}
		for _, tool := range value.Delta.Tools {
			if tool.Index < 0 || tool.Index >= 128 {
				return errors.New("CodeBuddy tool index exceeds the bounded limit")
			}
			fn := choice.tools[tool.Index]
			if fn == nil {
				fn = &collectedFunction{kind: "function"}
				choice.tools[tool.Index] = fn
			}
			fn.id.WriteString(tool.ID)
			fn.name.WriteString(tool.Function.Name)
			fn.arguments.WriteString(tool.Function.Arguments)
			if tool.Type != "" {
				fn.kind = tool.Type
			}
		}
	}
	return nil
}

func (c *completionCollector) result() ([]byte, error) {
	if len(c.choices) == 0 {
		return nil, errors.New("CodeBuddy completion has no choices")
	}
	indices := make([]int, 0, len(c.choices))
	for index := range c.choices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	choices := make([]any, 0, len(indices))
	for _, index := range indices {
		choice := c.choices[index]
		if choice.finish == "" {
			return nil, errors.New("CodeBuddy completion ended without a finish reason")
		}
		message := map[string]any{"role": "assistant", "content": nil}
		if choice.hasContent {
			message["content"] = choice.content.String()
		}
		if choice.hasReasoning {
			message["reasoning_content"] = choice.reasoning.String()
		}
		if choice.hasRefusal {
			message["refusal"] = choice.refusal.String()
		}
		if fn := choice.function; fn != nil {
			message["function_call"] = map[string]string{"name": fn.name.String(), "arguments": fn.arguments.String()}
		}
		toolIndices := make([]int, 0, len(choice.tools))
		for idx := range choice.tools {
			toolIndices = append(toolIndices, idx)
		}
		sort.Ints(toolIndices)
		var calls []any
		for _, idx := range toolIndices {
			fn := choice.tools[idx]
			calls = append(calls, map[string]any{"id": fn.id.String(), "type": fn.kind, "function": map[string]string{"name": fn.name.String(), "arguments": fn.arguments.String()}})
		}
		if len(calls) > 0 {
			message["tool_calls"] = calls
		}
		choices = append(choices, map[string]any{"index": index, "message": message, "finish_reason": choice.finish})
	}
	c.fields["object"] = json.RawMessage(`"chat.completion"`)
	c.fields["choices"], _ = json.Marshal(choices)
	return json.Marshal(c.fields)
}
