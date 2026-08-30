package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const maxSSELineBytes = 1024 * 1024

func (r *pluginRuntime) executeStream(raw []byte) (rpcStreamResponse, error) {
	var req rpcExecutorRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return rpcStreamResponse{}, newPluginCallError("invalid_request", "CodeBuddy executor request is invalid", http.StatusBadRequest, false)
	}
	if !req.Stream {
		return rpcStreamResponse{}, newPluginCallError("stream_required", "CodeBuddy hy3-preview-agent supports streaming requests only", http.StatusBadRequest, false)
	}
	if strings.TrimSpace(req.Model) != codeBuddyModel {
		return rpcStreamResponse{}, newPluginCallError("unsupported_model", "CodeBuddy G1 supports hy3-preview-agent only", http.StatusBadRequest, false)
	}
	if strings.TrimSpace(req.HostCallbackID) == "" {
		return rpcStreamResponse{}, newPluginCallError("invalid_stream", "CodeBuddy stream requires a host callback context", http.StatusBadRequest, false)
	}
	auth, errAuth := parseStoredAuth(req.StorageJSON)
	if errAuth != nil {
		return rpcStreamResponse{}, newPluginCallError("invalid_auth", errAuth.Error(), http.StatusUnauthorized, false)
	}
	body, errPayload := codeBuddyRequestPayload(req.Payload)
	if errPayload != nil {
		return rpcStreamResponse{}, errPayload
	}
	execution, errRegister := r.registerExecution(req.RequestID, req.StreamID)
	if errRegister != nil {
		return rpcStreamResponse{}, errRegister
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
	upstream, errOpen := openHostHTTPStream(r.caller, hostHTTPRequest{
		HostCallbackID: req.HostCallbackID,
		Method:         http.MethodPost,
		URL:            cfg.Endpoint,
		Headers:        headers,
		Body:           body,
	})
	if errOpen != nil {
		return rpcStreamResponse{}, newPluginCallError("upstream_unavailable", "CodeBuddy upstream connection failed", http.StatusBadGateway, true)
	}
	if execution.bindUpstream(r.caller, upstream.StreamID) {
		execution.finish(r.caller, "CodeBuddy stream canceled")
		return rpcStreamResponse{}, newPluginCallError("request_canceled", "CodeBuddy request was canceled", 499, false)
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		execution.closeUpstream(r.caller)
		status := upstream.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return rpcStreamResponse{}, newPluginCallError("upstream_error", fmt.Sprintf("CodeBuddy upstream returned HTTP %d", status), status, status == 429 || status >= 500)
	}
	if !strings.Contains(strings.ToLower(upstream.Headers.Get("Content-Type")), "text/event-stream") {
		execution.closeUpstream(r.caller)
		return rpcStreamResponse{}, newPluginCallError("invalid_upstream_response", "CodeBuddy upstream did not return text/event-stream", http.StatusBadGateway, true)
	}

	releaseOnError = false
	go r.forwardStream(execution)
	return rpcStreamResponse{Headers: http.Header{"Content-Type": {"text/event-stream"}}}, nil
}

func codeBuddyRequestPayload(raw []byte) ([]byte, error) {
	var body map[string]any
	if errDecode := json.Unmarshal(raw, &body); errDecode != nil || body == nil {
		return nil, newPluginCallError("invalid_request", "CodeBuddy request body must be a JSON object", http.StatusBadRequest, false)
	}
	if model, ok := body["model"].(string); ok && strings.TrimSpace(model) != "" && strings.TrimSpace(model) != codeBuddyModel {
		return nil, newPluginCallError("unsupported_model", "CodeBuddy G1 supports hy3-preview-agent only", http.StatusBadRequest, false)
	}
	if stream, exists := body["stream"]; exists {
		streamEnabled, ok := stream.(bool)
		if !ok || !streamEnabled {
			return nil, newPluginCallError("stream_required", "CodeBuddy hy3-preview-agent supports streaming requests only", http.StatusBadRequest, false)
		}
	}
	body["model"] = codeBuddyModel
	body["stream"] = true
	out, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, newPluginCallError("invalid_request", "CodeBuddy request body could not be encoded", http.StatusBadRequest, false)
	}
	return out, nil
}

func (r *pluginRuntime) forwardStream(execution *activeExecution) {
	validator := &sseValidator{}
	errorMessage := ""
	defer func() {
		execution.closeUpstream(r.caller)
		execution.finish(r.caller, errorMessage)
		r.releaseExecution(execution)
	}()

	for {
		if execution.canceled() {
			errorMessage = "CodeBuddy stream canceled"
			return
		}
		chunk, errRead := readHostHTTPStream(r.caller, execution.upstreamID())
		if errRead != nil {
			if execution.canceled() {
				errorMessage = "CodeBuddy stream canceled"
			} else {
				errorMessage = "CodeBuddy upstream stream read failed"
			}
			return
		}
		if chunk.Error != "" {
			errorMessage = "CodeBuddy upstream stream failed"
			return
		}
		if len(chunk.Payload) > 0 {
			frames, errValidate := validator.consume(chunk.Payload)
			if errValidate != nil {
				errorMessage = errValidate.Error()
				return
			}
			for _, frame := range frames {
				if errEmit := emitPluginStream(r.caller, execution.pluginStreamID, frame); errEmit != nil {
					errorMessage = "CodeBuddy downstream stream closed"
					return
				}
			}
		}
		if chunk.Done {
			if execution.canceled() {
				errorMessage = "CodeBuddy stream canceled"
			} else {
				frames, errFinish := validator.finish()
				if errFinish != nil {
					errorMessage = errFinish.Error()
					return
				}
				for _, frame := range frames {
					if errEmit := emitPluginStream(r.caller, execution.pluginStreamID, frame); errEmit != nil {
						errorMessage = "CodeBuddy downstream stream closed"
						return
					}
				}
			}
			return
		}
	}
}

type sseValidator struct {
	buffer       []byte
	doneReceived bool
}

func (v *sseValidator) consume(payload []byte) ([][]byte, error) {
	if v.doneReceived && len(bytes.TrimSpace(payload)) > 0 {
		return nil, fmt.Errorf("CodeBuddy upstream sent data after [DONE]")
	}
	v.buffer = append(v.buffer, payload...)
	if len(v.buffer) > maxSSELineBytes && !bytes.Contains(v.buffer, []byte{'\n'}) {
		return nil, fmt.Errorf("CodeBuddy upstream SSE line exceeds the bounded limit")
	}
	var frames [][]byte
	for {
		lineEnd := bytes.IndexByte(v.buffer, '\n')
		if lineEnd < 0 {
			return frames, nil
		}
		if lineEnd > maxSSELineBytes {
			return nil, fmt.Errorf("CodeBuddy upstream SSE line exceeds the bounded limit")
		}
		line := bytes.TrimSuffix(v.buffer[:lineEnd], []byte{'\r'})
		v.buffer = v.buffer[lineEnd+1:]
		frame, errLine := v.consumeLine(line)
		if errLine != nil {
			return nil, errLine
		}
		if len(frame) > 0 {
			frames = append(frames, frame)
		}
	}
}

func (v *sseValidator) consumeLine(line []byte) ([]byte, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) || !bytes.HasPrefix(line, []byte("data:")) {
		return nil, nil
	}
	if v.doneReceived {
		return nil, fmt.Errorf("CodeBuddy upstream sent data after [DONE]")
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if bytes.Equal(data, []byte("[DONE]")) {
		v.doneReceived = true
		return nil, nil
	}
	if len(data) == 0 {
		return nil, nil
	}
	var frame map[string]any
	if errDecode := json.Unmarshal(data, &frame); errDecode != nil {
		return nil, fmt.Errorf("CodeBuddy upstream returned malformed SSE data")
	}
	if _, hasError := frame["error"]; hasError {
		return nil, fmt.Errorf("CodeBuddy upstream stream reported an error")
	}
	normalized, errNormalize := normalizeCodeBuddyFrame(frame)
	if errNormalize != nil {
		return nil, errNormalize
	}
	return normalized, nil
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

func (v *sseValidator) finish() ([][]byte, error) {
	var frames [][]byte
	if len(bytes.TrimSpace(v.buffer)) > 0 {
		frame, errLine := v.consumeLine(bytes.TrimSpace(v.buffer))
		if errLine != nil {
			return nil, errLine
		}
		if len(frame) > 0 {
			frames = append(frames, frame)
		}
	}
	if !v.doneReceived {
		return nil, fmt.Errorf("CodeBuddy upstream stream ended before [DONE]")
	}
	return frames, nil
}
