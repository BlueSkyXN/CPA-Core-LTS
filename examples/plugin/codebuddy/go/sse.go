package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	maxSSELineBytes  = 1024 * 1024
	maxSSEEventBytes = 4 * maxSSELineBytes
)

type sseValidator struct {
	buffer       []byte
	data         []byte
	event        string
	started      bool
	doneReceived bool
	choices      map[int]bool
	pendingError error
}

func (v *sseValidator) consume(payload []byte) ([][]byte, error) {
	if v.doneReceived && len(bytes.TrimSpace(payload)) > 0 {
		return nil, fmt.Errorf("CodeBuddy upstream sent data after [DONE]")
	}
	v.buffer = append(v.buffer, payload...)
	var frames [][]byte
	for {
		end := bytes.IndexAny(v.buffer, "\r\n")
		if end < 0 || v.buffer[end] == '\r' && end+1 == len(v.buffer) {
			if len(v.buffer) > maxSSELineBytes {
				return frames, fmt.Errorf("CodeBuddy upstream SSE line exceeds the bounded limit")
			}
			return frames, nil
		}
		if end > maxSSELineBytes {
			return frames, fmt.Errorf("CodeBuddy upstream SSE line exceeds the bounded limit")
		}
		width := 1
		if v.buffer[end] == '\r' && v.buffer[end+1] == '\n' {
			width++
		}
		line := v.buffer[:end]
		v.buffer = v.buffer[end+width:]
		frame, err := v.consumeLine(line)
		if len(frame) > 0 {
			frames = append(frames, frame)
		}
		if err != nil {
			return frames, err
		}
	}
}

func (v *sseValidator) consumeLine(line []byte) ([]byte, error) {
	if !v.started {
		v.started = true
		line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
	}
	if len(line) == 0 {
		return v.dispatchEvent()
	}
	if line[0] == ':' {
		return nil, nil
	}
	field, value, _ := bytes.Cut(line, []byte{':'})
	value = bytes.TrimPrefix(value, []byte{' '})
	switch string(field) {
	case "event":
		v.event = string(value)
	case "data":
		if v.doneReceived {
			return nil, fmt.Errorf("CodeBuddy upstream sent data after [DONE]")
		}
		if len(v.data)+len(value)+1 > maxSSEEventBytes {
			return nil, fmt.Errorf("CodeBuddy upstream SSE event exceeds the bounded limit")
		}
		v.data = append(v.data, value...)
		v.data = append(v.data, '\n')
	}
	return nil, nil
}

func (v *sseValidator) dispatchEvent() ([]byte, error) {
	data, event := bytes.TrimSpace(v.data), v.event
	v.data, v.event = nil, ""
	if len(data) == 0 {
		if event == "error" {
			return nil, codeBuddyUpstreamError(0, 0)
		}
		return nil, nil
	}
	if event == "error" && !json.Valid(data) {
		return nil, codeBuddyUpstreamError(0, 0)
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		if event == "error" {
			return nil, codeBuddyUpstreamError(0, 0)
		}
		v.doneReceived = true
		return nil, v.pendingError
	}
	// ping 等命名事件可以携带普通文本；只将完成事件交给模型协议层。
	switch event {
	case "", "message", "completion", "chunk", "output", "usage", "error":
	default:
		return nil, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !json.Valid(data) || decoder.Decode(&value) != nil {
		return nil, fmt.Errorf("CodeBuddy upstream returned malformed SSE data")
	}
	frame, ok := value.(map[string]any)
	if !ok {
		if event == "error" {
			return nil, codeBuddyUpstreamError(0, 0)
		}
		return nil, nil
	}
	if err := codeBuddyFrameError(frame, event == "error"); err != nil {
		// 失败也可能计费：流式路径先保留同一终止帧的 usage，再发送结构化错误。
		if usage, ok := frame["usage"].(map[string]any); ok && len(usage) > 0 {
			payload, _ := json.Marshal(map[string]any{"choices": []any{}, "usage": usage})
			return payload, err
		}
		if typed, ok := err.(*pluginCallError); ok && typed.code == "upstream_model_error" {
			// WorkBuddy 在 error finish 后继续读取 usage 尾帧；不转发失败的 delta。
			v.pendingError = err
			return nil, nil
		}
		return nil, err
	}
	choices, _ := frame["choices"].([]any)
	usage, _ := frame["usage"].(map[string]any)
	if v.pendingError != nil {
		if len(usage) > 0 {
			payload, _ := json.Marshal(map[string]any{"choices": []any{}, "usage": usage})
			return payload, v.pendingError
		}
		return nil, nil
	}
	if len(choices) == 0 && len(usage) == 0 {
		return nil, nil
	}
	for _, value := range choices {
		choice, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("CodeBuddy upstream completion choice is invalid")
		}
		index := 0
		if value, exists := choice["index"]; exists {
			number, ok := value.(json.Number)
			if !ok {
				return nil, fmt.Errorf("CodeBuddy completion choice index is invalid")
			}
			n, err := number.Int64()
			if err != nil || n < 0 || n >= 16 {
				return nil, fmt.Errorf("CodeBuddy completion choice index exceeds the bounded limit")
			}
			index = int(n)
		}
		if v.choices == nil {
			v.choices = make(map[int]bool)
		}
		finish, _ := choice["finish_reason"].(string)
		v.choices[index] = v.choices[index] || strings.TrimSpace(finish) != ""
	}
	return normalizeCodeBuddyFrame(frame)
}

func (v *sseValidator) finish() ([][]byte, error) {
	var frames [][]byte
	if len(v.buffer) > maxSSELineBytes {
		return nil, fmt.Errorf("CodeBuddy upstream SSE line exceeds the bounded limit")
	}
	if len(v.buffer) > 0 {
		frame, err := v.consumeLine(bytes.TrimSuffix(v.buffer, []byte{'\r'}))
		v.buffer = nil
		if len(frame) > 0 {
			frames = append(frames, frame)
		}
		if err != nil {
			return frames, err
		}
	}
	frame, err := v.dispatchEvent()
	if len(frame) > 0 {
		frames = append(frames, frame)
	}
	if err != nil {
		return frames, err
	}
	if v.pendingError != nil {
		return frames, v.pendingError
	}
	if !v.doneReceived {
		return frames, fmt.Errorf("CodeBuddy upstream stream ended before [DONE]")
	}
	if len(v.choices) == 0 {
		return frames, newPluginCallError("empty_stream", "CodeBuddy upstream returned no completion choices", http.StatusBadGateway, true)
	}
	for _, finished := range v.choices {
		if !finished {
			return frames, newPluginCallError("invalid_upstream_response", "CodeBuddy completion ended without a finish reason", http.StatusBadGateway, false)
		}
	}
	return frames, nil
}
