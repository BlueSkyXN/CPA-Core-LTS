package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const maxErrorBody = 16 * 1024
const errorBodyReadTimeout = 2 * time.Second

func businessCode(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if strings.HasPrefix(value, `"`) {
		if json.Unmarshal(raw, &value) != nil {
			return ""
		}
	}
	if len(value) < 4 || len(value) > 6 || value[0] == '0' {
		return ""
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return ""
		}
	}
	return value
}

func upstreamErrorCode(body []byte) string {
	if len(body) > maxErrorBody {
		return ""
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	var nested map[string]json.RawMessage
	_ = json.Unmarshal(root["error"], &nested)
	for _, raw := range []json.RawMessage{nested["code"], nested["provider_code"], root["code"], root["provider_code"]} {
		if code := businessCode(raw); code != "" {
			return code
		}
	}
	for _, raw := range []json.RawMessage{nested["message"], root["message"], root["error"]} {
		var message string
		if json.Unmarshal(raw, &message) != nil || !strings.HasPrefix(message, "[") {
			continue
		}
		code, _, found := strings.Cut(message[1:], "][")
		if found {
			if code = businessCode(json.RawMessage(code)); code != "" {
				return code
			}
		}
	}
	for _, raw := range []json.RawMessage{nested["type"], root["type"]} {
		var kind string
		if json.Unmarshal(raw, &kind) == nil {
			switch kind {
			case "invalid_request_error", "authentication_error", "permission_error", "not_found_error", "rate_limit_error", "api_error", "overloaded_error":
				return kind
			}
		}
	}
	return ""
}

func diagnosticSource(endpoint string) string {
	if endpoint == upstreamEndpoint("zai") {
		return "zai"
	}
	return "bigmodel"
}

func upstreamProblem(status int, source, bodyCode, fallback string) *apiError {
	err := &apiError{Status: status, Code: "upstream_error", Message: fallback}
	if bodyCode == "" || source != "bigmodel" && source != "zai" {
		return err
	}
	err.FailureReason = source + "_" + bodyCode
	if bodyCode == "1301" {
		err.Message = "[1301] Upstream content safety check blocked the input or generated content"
	} else {
		err.Message = fmt.Sprintf("Upstream rejected the request (code=%s)", bodyCode)
	}
	return err
}

// Error-body deadlines apply only after an HTTP rejection, never to successful streams.
func (r *pluginRuntime) readErrorBody(e *execution, streamID string, timeout time.Duration) []byte {
	var expired atomic.Bool
	timerDone := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		expired.Store(true)
		r.releaseStream(e, streamID)
		close(timerDone)
	})
	defer func() {
		if !timer.Stop() {
			<-timerDone
		}
	}()
	var body []byte
	for {
		chunk, err := r.readChunk(e, streamID)
		if err != nil || expired.Load() || len(body)+len(chunk.Payload) > maxErrorBody {
			return nil
		}
		body = append(body, chunk.Payload...)
		if chunk.Done || json.Valid(body) {
			return body
		}
	}
}

func (r *pluginRuntime) rejectedRequest(e *execution, upstream httpStream, phase, fallback string) error {
	status := upstream.StatusCode
	if status < 400 || status > 599 {
		status = 502
	}
	body := r.readErrorBody(e, upstream.StreamID, errorBodyReadTimeout)
	err := upstreamProblem(status, diagnosticSource(e.signer.endpoint), upstreamErrorCode(body), fallback)
	r.logUpstreamFailure(e, phase, upstream.StatusCode, upstream.Headers, err)
	return err
}

func safeUpstreamTraceID(headers http.Header) string {
	for _, name := range []string{"X-Request-Id", "Request-Id", "X-Trace-Id"} {
		value := headers.Get(name)
		if len(value) < 8 || len(value) > 64 {
			continue
		}
		valid := true
		for _, ch := range value {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F' || ch == '-') {
				valid = false
				break
			}
		}
		if valid {
			return value
		}
	}
	return ""
}

func (r *pluginRuntime) logUpstreamFailure(e *execution, phase string, upstreamStatus int, headers http.Header, err *apiError) {
	reason := err.FailureReason
	if reason == "" {
		reason = err.Code
	}
	message := fmt.Sprintf("Coding Plan upstream failure: phase=%s upstream_http=%d status=%d reason=%s", phase, upstreamStatus, err.Status, reason)
	if traceID := safeUpstreamTraceID(headers); traceID != "" {
		message += " upstream_request_id=" + traceID
	}
	_, _ = r.caller.Call("host.log", map[string]any{
		"host_callback_id": e.req.CallbackID,
		"level":            "warn",
		"message":          message,
		"fields":           map[string]any{"plugin_id": provider},
	})
}

func streamErrorProblem(data []byte, source string) *apiError {
	return upstreamProblem(502, source, upstreamErrorCode(bytes.TrimSpace(data)), "Upstream reported a stream error")
}
