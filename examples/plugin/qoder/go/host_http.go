package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

type hostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method"`
	URL            string      `json:"url"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type hostHTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id,omitempty"`
}

type hostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

func openHostHTTPStream(caller hostCaller, req hostHTTPRequest) (hostHTTPStreamResponse, error) {
	if caller == nil {
		return hostHTTPStreamResponse{}, fmt.Errorf("Qoder host HTTP callback is unavailable")
	}
	raw, err := caller.Call(pluginabi.MethodHostHTTPDoStream, req)
	if err != nil {
		return hostHTTPStreamResponse{}, fmt.Errorf("open Qoder upstream stream")
	}
	var resp hostHTTPStreamResponse
	if json.Unmarshal(raw, &resp) != nil || strings.TrimSpace(resp.StreamID) == "" {
		return hostHTTPStreamResponse{}, fmt.Errorf("invalid Qoder host stream response")
	}
	return resp, nil
}

func readHostHTTPStream(caller hostCaller, id string) (hostHTTPStreamReadResponse, error) {
	raw, err := caller.Call(pluginabi.MethodHostHTTPStreamRead, map[string]string{"stream_id": id})
	if err != nil {
		return hostHTTPStreamReadResponse{}, fmt.Errorf("read Qoder upstream stream")
	}
	var resp hostHTTPStreamReadResponse
	if json.Unmarshal(raw, &resp) != nil {
		return resp, fmt.Errorf("invalid Qoder host stream chunk")
	}
	return resp, nil
}

func closeHostHTTPStream(caller hostCaller, id string) {
	if caller != nil && id != "" {
		_, _ = caller.Call(pluginabi.MethodHostHTTPStreamClose, map[string]string{"stream_id": id})
	}
}

type hostHTTPResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

func doHostHTTP(caller hostCaller, req hostHTTPRequest) (hostHTTPResponse, error) {
	if caller == nil {
		return hostHTTPResponse{}, fmt.Errorf("Qoder host HTTP callback is unavailable")
	}
	raw, errCall := caller.Call(pluginabi.MethodHostHTTPDo, req)
	if errCall != nil {
		return hostHTTPResponse{}, fmt.Errorf("Qoder host HTTP request failed")
	}
	var response hostHTTPResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return hostHTTPResponse{}, fmt.Errorf("decode Qoder host HTTP response")
	}
	return response, nil
}
