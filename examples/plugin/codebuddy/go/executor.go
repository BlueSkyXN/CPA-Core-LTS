package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const codeBuddyConnectionLifecycleErrorCode = "connection_lifecycle"

func (r *pluginRuntime) executeStream(raw []byte) (rpcStreamResponse, error) {
	execution, err := r.openExecution(raw, true)
	if err != nil {
		return rpcStreamResponse{}, err
	}
	go r.forwardStream(execution)
	return rpcStreamResponse{Headers: http.Header{"Content-Type": {"text/event-stream"}}}, nil
}

func (r *pluginRuntime) openExecution(raw []byte, stream bool) (*activeExecution, error) {
	var req rpcExecutorRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return nil, newPluginCallError("invalid_request", "CodeBuddy executor request is invalid", http.StatusBadRequest, false)
	}
	if req.Stream != stream || strings.TrimSpace(req.RequestID) == "" || stream && strings.TrimSpace(req.StreamID) == "" {
		return nil, newPluginCallError("invalid_request", "CodeBuddy request ID and stream mode must match the executor call", http.StatusBadRequest, false)
	}
	if !stream {
		req.StreamID = ""
	}
	model := strings.TrimSpace(req.Model)
	if strings.TrimSpace(req.HostCallbackID) == "" {
		return nil, newPluginCallError("invalid_stream", "CodeBuddy request requires a host callback context", http.StatusBadRequest, false)
	}
	auth, errAuth := parseStoredAuth(req.StorageJSON)
	if errAuth != nil {
		return nil, newPluginCallError("invalid_auth", errAuth.Error(), http.StatusUnauthorized, false)
	}
	hints, errModel := r.codeBuddyModelHintsForAuth(auth, model, req.HostCallbackID)
	if errModel != nil {
		return nil, errModel
	}
	body, errPayload := codeBuddyRequestPayload(req.Payload, model, hints)
	if errPayload != nil {
		return nil, errPayload
	}
	execution, errRegister := r.registerExecution(req.RequestID, req.StreamID)
	if errRegister != nil {
		return nil, errRegister
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			execution.signalDone()
			r.releaseExecution(execution)
		}
	}()

	cfg := r.loadedConfig()
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+auth.APIKey)
	headers.Set("X-API-Key", auth.APIKey)
	headers.Set("Accept", "text/event-stream")
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", cfg.UserAgent)
	headers.Set("X-Product", "SaaS")
	upstream, errOpen := openHostHTTPStream(r.caller, hostHTTPRequest{
		HostCallbackID: req.HostCallbackID,
		Method:         http.MethodPost,
		URL:            cfg.Endpoint,
		Headers:        headers,
		Body:           body,
	})
	if errOpen != nil {
		return nil, newPluginCallError("upstream_unavailable", "CodeBuddy upstream connection failed", http.StatusBadGateway, true)
	}
	if execution.bindUpstream(r.caller, upstream.StreamID) {
		execution.finish(r.caller, "CodeBuddy stream canceled", codeBuddyConnectionLifecycleErrorCode, true, 0)
		return nil, newPluginCallError(codeBuddyConnectionLifecycleErrorCode, "CodeBuddy request was canceled", 0, true)
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		errUpstream := r.codeBuddyHTTPError(execution, upstream.StatusCode)
		execution.closeUpstream(r.caller)
		return nil, errUpstream
	}
	if !strings.Contains(strings.ToLower(upstream.Headers.Get("Content-Type")), "text/event-stream") {
		errUpstream := r.codeBuddyHTTPError(execution, upstream.StatusCode)
		execution.closeUpstream(r.caller)
		return nil, errUpstream
	}

	releaseOnError = false
	return execution, nil
}

func codeBuddyRequestPayload(raw []byte, model string, hints ...codeBuddyModelHints) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, newPluginCallError("unsupported_model", "CodeBuddy model is required", http.StatusBadRequest, false)
	}
	var body map[string]any
	if errDecode := json.Unmarshal(raw, &body); errDecode != nil || body == nil {
		return nil, newPluginCallError("invalid_request", "CodeBuddy request body must be a JSON object", http.StatusBadRequest, false)
	}
	if payloadModel, ok := body["model"].(string); ok && strings.TrimSpace(payloadModel) != "" && strings.TrimSpace(payloadModel) != model {
		return nil, newPluginCallError("unsupported_model", "CodeBuddy payload model must match the selected exact model ID", http.StatusBadRequest, false)
	}
	if stream, exists := body["stream"]; exists {
		if _, ok := stream.(bool); !ok {
			return nil, newPluginCallError("invalid_request", "CodeBuddy stream must be a boolean", http.StatusBadRequest, false)
		}
	}
	if errToolChoice := normalizeCodeBuddyToolChoice(body); errToolChoice != nil {
		return nil, errToolChoice
	}
	maxTokensField := ""
	if len(hints) > 0 {
		maxTokensField = hints[0].MaxTokensField
	}
	normalizeCodeBuddyMaxTokens(body, model, maxTokensField)
	body["model"] = model
	body["stream"] = true
	options, _ := body["stream_options"].(map[string]any)
	if options == nil {
		options = map[string]any{}
	}
	options["include_usage"] = true
	body["stream_options"] = options
	out, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, newPluginCallError("invalid_request", "CodeBuddy request body could not be encoded", http.StatusBadRequest, false)
	}
	return out, nil
}

// CodeBuddy's chat request schema accepts a string tool_choice. Core's OpenAI
// translator may use the standard object form for an explicitly selected
// function, so normalize that representation at the provider boundary while
// preserving auto/none/required string choices.
func normalizeCodeBuddyToolChoice(body map[string]any) error {
	raw, exists := body["tool_choice"]
	if !exists || raw == nil {
		delete(body, "tool_choice")
		return nil
	}
	if choice, ok := raw.(string); ok {
		if strings.TrimSpace(choice) == "" {
			delete(body, "tool_choice")
		}
		return nil
	}
	choice, ok := raw.(map[string]any)
	if !ok {
		return newPluginCallError("invalid_request", "CodeBuddy tool_choice must be a string or function object", http.StatusBadRequest, false)
	}
	typeName, _ := choice["type"].(string)
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	if typeName == "auto" || typeName == "none" || typeName == "required" {
		body["tool_choice"] = typeName
		return nil
	}
	if typeName != "function" {
		return newPluginCallError("invalid_request", "CodeBuddy tool_choice function type is required", http.StatusBadRequest, false)
	}
	function, ok := choice["function"].(map[string]any)
	if !ok {
		return newPluginCallError("invalid_request", "CodeBuddy tool_choice function is required", http.StatusBadRequest, false)
	}
	name, _ := function["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return newPluginCallError("invalid_request", "CodeBuddy tool_choice function name is required", http.StatusBadRequest, false)
	}
	body["tool_choice"] = name
	return nil
}

func (r *pluginRuntime) forwardStream(execution *activeExecution) {
	defer func() {
		execution.closeUpstream(r.caller)
		r.releaseExecution(execution)
	}()
	err := r.readExecution(execution, func(frame []byte) error {
		if err := emitPluginStream(r.caller, execution.pluginStreamID, frame); err != nil {
			return newPluginCallError(codeBuddyConnectionLifecycleErrorCode, "CodeBuddy downstream stream closed", 0, true)
		}
		return nil
	})
	execution.closeUpstream(r.caller)
	if err != nil {
		if typed, ok := err.(*pluginCallError); ok {
			execution.finish(r.caller, typed.message, typed.code, typed.retryable, typed.statusCode)
		} else {
			execution.finish(r.caller, err.Error(), "", false, 0)
		}
	} else {
		execution.finish(r.caller, "", "", false, 0)
	}
}

// 流式和非流式共用读取、[DONE] 校验与取消逻辑，不重放生成请求。
func (r *pluginRuntime) readExecution(execution *activeExecution, accept func([]byte) error) error {
	validator := &sseValidator{}
	emit := func(frames [][]byte) error {
		for _, frame := range frames {
			if err := accept(frame); err != nil {
				return err
			}
		}
		return nil
	}
	canceled := func() error {
		return newPluginCallError(codeBuddyConnectionLifecycleErrorCode, "CodeBuddy stream canceled", 0, true)
	}
	for {
		if execution.canceled() {
			return canceled()
		}
		chunk, errRead := readHostHTTPStream(r.caller, execution.upstreamID())
		if errRead != nil {
			if execution.canceled() {
				return canceled()
			}
			return fmt.Errorf("CodeBuddy upstream stream read failed")
		}
		if execution.canceled() {
			return canceled()
		}
		if len(chunk.Payload) > 0 {
			frames, errValidate := validator.consume(chunk.Payload)
			if err := emit(frames); err != nil {
				return err
			}
			if errValidate != nil {
				return errValidate
			}
		}
		if chunk.Error != "" {
			return fmt.Errorf("CodeBuddy upstream stream failed")
		}
		if validator.doneReceived || chunk.Done {
			frames, err := validator.finish()
			if emitErr := emit(frames); emitErr != nil {
				return emitErr
			}
			return err
		}
	}
}

func normalizeCodeBuddyFrame(frame map[string]any) ([]byte, error) {
	if choices, ok := frame["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, okChoice := rawChoice.(map[string]any)
			if !okChoice {
				continue
			}
			if finishReason, okFinish := choice["finish_reason"].(string); okFinish && strings.TrimSpace(finishReason) == "" {
				delete(choice, "finish_reason")
			} else if value, exists := choice["finish_reason"]; exists && value == nil {
				delete(choice, "finish_reason")
			}
			delta, okDelta := choice["delta"].(map[string]any)
			if !okDelta {
				continue
			}
			functionCall, okFunctionCall := delta["function_call"].(map[string]any)
			if okFunctionCall && strings.TrimSpace(fmt.Sprint(functionCall["name"])) == "" && strings.TrimSpace(fmt.Sprint(functionCall["arguments"])) == "" {
				delete(delta, "function_call")
			} else if value, exists := delta["function_call"]; exists && value == nil {
				delete(delta, "function_call")
			}
			if toolCalls, okToolCalls := delta["tool_calls"].([]any); okToolCalls && len(toolCalls) == 0 {
				delete(delta, "tool_calls")
			}
			for _, key := range []string{"reasoning_content", "refusal"} {
				if value, okValue := delta[key].(string); okValue && value == "" {
					delete(delta, key)
				}
			}
			if value, exists := delta["extra_fields"]; exists && value == nil {
				delete(delta, "extra_fields")
			}
		}
	}
	normalized, errMarshal := json.Marshal(frame)
	if errMarshal != nil {
		return nil, fmt.Errorf("CodeBuddy upstream frame could not be normalized")
	}
	return normalized, nil
}
