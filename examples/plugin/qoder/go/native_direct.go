package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const maxNativeResponseBytes = 4 * 1024 * 1024

type nativeReader struct {
	caller    hostCaller
	execution *nativeExecution
	id        string
	buffer    []byte
	done      bool
	err       error
}

func (reader *nativeReader) Read(out []byte) (int, error) {
	for len(reader.buffer) == 0 {
		if reader.execution != nil && reader.execution.isCanceled() {
			return 0, nativeCanceled()
		}
		if reader.err != nil {
			return 0, reader.err
		}
		if reader.done {
			return 0, io.EOF
		}
		chunk, err := readHostHTTPStream(reader.caller, reader.id)
		if err != nil || chunk.Error != "" {
			reader.err = newPluginCallError("connection_lifecycle", "Qoder upstream stream failed", 0, true)
		}
		reader.buffer, reader.done = chunk.Payload, chunk.Done
	}
	n := copy(out, reader.buffer)
	reader.buffer = reader.buffer[n:]
	return n, nil
}

func nativeRequestPayload(req pluginapi.ExecutorRequest) ([]byte, error) {
	if req.Format != "" && req.Format != "chat-completions" && req.Format != "openai" {
		return nil, newPluginCallError("unsupported_format", "Qoder accepts host-translated Chat Completions only", 400, false)
	}
	if err := validateCanonicalModel(req.Model); err != nil {
		return nil, err
	}
	if len(req.Payload) == 0 || len(req.Payload) > maxExecutorBodyBytes {
		return nil, newPluginCallError("invalid_request", "Qoder direct request is empty or oversized", 400, false)
	}
	var body map[string]any
	if json.Unmarshal(req.Payload, &body) != nil || body == nil {
		return nil, newPluginCallError("invalid_request", "Qoder direct requires a JSON object", 400, false)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, newPluginCallError("invalid_request", "Qoder direct requires messages", 400, false)
	}
	if n, exists := body["n"]; exists && n != nil && n != float64(1) {
		return nil, newPluginCallError("invalid_request", "Qoder direct supports only n=1", 400, false)
	}
	body["model"], body["stream"] = req.Model, true
	options, _ := body["stream_options"].(map[string]any)
	if options == nil {
		options = map[string]any{}
	}
	options["include_usage"] = true
	body["stream_options"] = options
	metadata, _ := body["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	ctx, _ := metadata["context"].(map[string]any)
	if ctx == nil {
		ctx = map[string]any{}
	}
	ctx["request_id"], ctx["request_set_id"] = req.RequestID, req.RequestID
	ctx["session_id"], ctx["task_id"], ctx["client_type"] = effectiveExecutionSessionID(req), "common", "cpa-qoder-direct"
	metadata["context"] = ctx
	body["metadata"] = metadata
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > maxExecutorBodyBytes {
		return nil, newPluginCallError("direct_request_too_large", "Qoder direct request exceeds the body limit", 413, false)
	}
	return raw, nil
}

func (r *pluginRuntime) openNative(req rpcExecutorRequest) (*nativeExecution, hostHTTPStreamResponse, error) {
	body, err := nativeRequestPayload(req.ExecutorRequest)
	if err != nil {
		return nil, hostHTTPStreamResponse{}, err
	}
	auth, err := parseStoredAuth(req.StorageJSON)
	if err != nil {
		return nil, hostHTTPStreamResponse{}, newPluginCallError("invalid_auth", "Qoder direct credential is invalid", 401, false)
	}
	exec, err := r.registerNative(req, auth)
	if err != nil {
		return nil, hostHTTPStreamResponse{}, err
	}
	failed := true
	defer func() {
		if failed {
			r.releaseNative(exec)
		}
	}()
	cfg := r.loadedConfig()
	if cfg.DirectEndpoint == "" || validateDirectURL(cfg.DirectEndpoint, "direct_endpoint") != nil {
		return nil, hostHTTPStreamResponse{}, newPluginCallError("direct_endpoint_required", "Qoder direct endpoint is not configured", 503, false)
	}
	models, err := r.nativeModels(auth, req.HostCallbackID, cfg)
	if err != nil {
		return nil, hostHTTPStreamResponse{}, err
	}
	allowed := false
	for _, model := range models {
		if model.ID == req.Model {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, hostHTTPStreamResponse{}, newPluginCallError("unsupported_model", "Qoder model must match an exact catalog ID", 400, false)
	}
	if exec.isCanceled() {
		return nil, hostHTTPStreamResponse{}, nativeCanceled()
	}
	state, err := r.nativeToken(auth, req.HostCallbackID, cfg, "")
	if err != nil {
		return nil, hostHTTPStreamResponse{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if exec.isCanceled() {
			return nil, hostHTTPStreamResponse{}, nativeCanceled()
		}
		headers := http.Header{"Authorization": {"Bearer " + state.Token}, "Accept": {"text/event-stream"}, "Content-Type": {"application/json"}, "X-Request-ID": {req.RequestID}, "X-Session-ID": {effectiveExecutionSessionID(req.ExecutorRequest)}}
		response, err := openHostHTTPStream(r.caller, hostHTTPRequest{HostCallbackID: req.HostCallbackID, Method: http.MethodPost, URL: cfg.DirectEndpoint, Headers: headers, Body: body})
		if err != nil {
			return nil, response, newPluginCallError("connection_lifecycle", "Qoder direct connection failed", 0, true)
		}
		if !exec.bind(r.caller, response.StreamID) {
			return nil, response, nativeCanceled()
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			raw, _ := io.ReadAll(io.LimitReader(&nativeReader{caller: r.caller, execution: exec, id: response.StreamID}, 8192))
			exec.closeUpstream(r.caller, false)
			if exec.isCanceled() {
				return nil, response, nativeCanceled()
			}
			if attempt == 0 && auth.isPAT() && cfg.DirectTokenMode != "bearer" && qoderAuthRejected(response.StatusCode, raw) {
				state, err = r.nativeToken(auth, req.HostCallbackID, cfg, state.Token)
				if err != nil {
					return nil, response, err
				}
				continue
			}
			return nil, response, qoderUpstreamError(response.StatusCode, raw)
		}
		contentType := strings.ToLower(response.Headers.Get("Content-Type"))
		if !strings.Contains(contentType, "text/event-stream") && !strings.Contains(contentType, "application/json") {
			return nil, response, newPluginCallError("invalid_upstream_response", "Qoder direct returned an unsupported content type", 502, false)
		}
		failed = false
		return exec, response, nil
	}
	return nil, hostHTTPStreamResponse{}, newPluginCallError("auth_expired", "Qoder authentication was rejected", 401, false)
}

func (r *pluginRuntime) executeNative(req rpcExecutorRequest) (pluginapi.ExecutorResponse, error) {
	exec, upstream, err := r.openNative(req)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	defer r.releaseNative(exec)
	decoder := newNativeDecoder(req.RequestID, req.Model, nil)
	if err := r.consumeNative(exec, upstream, decoder); err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	if exec.isCanceled() {
		return pluginapi.ExecutorResponse{}, nativeCanceled()
	}
	if err := decoder.finish(); err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	return decoder.projection.nonStreamResponse()
}

func (r *pluginRuntime) executeNativeStream(req rpcExecutorRequest) (rpcStreamResponse, error) {
	exec, upstream, err := r.openNative(req)
	if err != nil {
		return rpcStreamResponse{}, err
	}
	go func() {
		decoder := newNativeDecoder(req.RequestID, req.Model, func(raw []byte) error {
			if exec.isCanceled() {
				return nativeCanceled()
			}
			if err := emitPluginStream(r.caller, req.StreamID, raw); err != nil {
				exec.closeUpstream(r.caller, true)
				return nativeCanceled()
			}
			return nil
		})
		err := r.consumeNative(exec, upstream, decoder)
		if err != nil && !exec.isCanceled() {
			_ = decoder.emitFailureUsage()
		}
		if exec.isCanceled() {
			err = nativeCanceled()
		}
		// 在终结帧发出前释放会话占用，允许客户端立即提交下一轮。
		r.releaseNative(exec)
		if err == nil {
			err = decoder.finish()
		}
		if err != nil {
			callErr, ok := err.(*pluginCallError)
			if !ok {
				callErr = &pluginCallError{code: "direct_upstream_error", message: "Qoder direct stream failed", statusCode: 502}
			}
			closePluginStream(r.caller, req.StreamID, callErr.message, callErr.code, callErr.retryable, callErr.statusCode)
		} else {
			closePluginStream(r.caller, req.StreamID, "", "", false, 0)
		}
	}()
	return rpcStreamResponse{Headers: http.Header{"Content-Type": {"text/event-stream"}}}, nil
}

func (r *pluginRuntime) consumeNative(exec *nativeExecution, upstream hostHTTPStreamResponse, decoder *nativeDecoder) error {
	reader := &nativeReader{caller: r.caller, execution: exec, id: upstream.StreamID}
	if strings.Contains(strings.ToLower(upstream.Headers.Get("Content-Type")), "application/json") {
		raw, err := io.ReadAll(io.LimitReader(reader, maxNativeResponseBytes+1))
		if err != nil {
			return err
		}
		if len(raw) > maxNativeResponseBytes {
			return nativeResponseTooLarge()
		}
		_, err = decoder.data(raw)
		if err == nil && !decoder.sawResult {
			return nativeInvalidResponse()
		}
		return err
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxRunnerFrameBytes)
	var event bytes.Buffer
	total := 0
	consume := func() (bool, error) {
		raw := bytes.TrimSpace(event.Bytes())
		if len(raw) == 0 {
			event.Reset()
			return false, nil
		}
		done, err := decoder.data(raw)
		event.Reset()
		return done, err
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		total += len(line) + 1
		if total > maxNativeResponseBytes {
			return nativeResponseTooLarge()
		}
		if len(line) == 0 {
			done, err := consume()
			if done || err != nil {
				return err
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			if event.Len() > 0 {
				event.WriteByte('\n')
			}
			event.Write(bytes.TrimSpace(line[5:]))
		}
	}
	if scanner.Err() != nil {
		if exec.isCanceled() {
			return nativeCanceled()
		}
		return newPluginCallError("connection_lifecycle", "Qoder upstream stream read failed", 0, true)
	}
	done, err := consume()
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	return newPluginCallError("stream_truncated", "Qoder direct stream ended before [DONE]", 502, true)
}

func nativeInvalidResponse() error {
	return newPluginCallError("invalid_upstream_response", "Qoder direct returned an invalid response", 502, false)
}
func nativeResponseTooLarge() error {
	return newPluginCallError("stream_too_large", "Qoder direct response exceeds the bounded limit", 502, false)
}
