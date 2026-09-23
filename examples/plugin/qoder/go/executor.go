package main

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (r *pluginRuntime) execute(raw []byte) (pluginapi.ExecutorResponse, error) {
	var req rpcExecutorRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return pluginapi.ExecutorResponse{}, newPluginCallError("invalid_request", "Qoder executor request is invalid", http.StatusBadRequest, false)
	}
	if req.Stream {
		return pluginapi.ExecutorResponse{}, newPluginCallError("invalid_request", "Qoder non-stream executor received a streaming request", http.StatusBadRequest, false)
	}
	return r.executeNative(req)
}

func (r *pluginRuntime) executeStream(raw []byte) (rpcStreamResponse, error) {
	var req rpcExecutorRequest
	if errDecode := decodeRequest(raw, &req); errDecode != nil {
		return rpcStreamResponse{}, newPluginCallError("invalid_request", "Qoder executor request is invalid", http.StatusBadRequest, false)
	}
	if !req.Stream || strings.TrimSpace(req.StreamID) == "" {
		return rpcStreamResponse{}, newPluginCallError("invalid_stream", "Qoder stream requires stream=true and a stream ID", http.StatusBadRequest, false)
	}
	return r.executeNativeStream(req)
}
