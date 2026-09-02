package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

type hostCaller interface {
	Call(method string, payload any) (json.RawMessage, error)
}

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

type hostHTTPResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type hostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type hostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type hostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type pluginStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type pluginStreamCloseRequest struct {
	StreamID   string `json:"stream_id"`
	Error      string `json:"error,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func doHostHTTP(caller hostCaller, req hostHTTPRequest) (hostHTTPResponse, error) {
	if caller == nil {
		return hostHTTPResponse{}, fmt.Errorf("CodeBuddy host HTTP callback is unavailable")
	}
	raw, errCall := caller.Call(pluginabi.MethodHostHTTPDo, req)
	if errCall != nil {
		return hostHTTPResponse{}, fmt.Errorf("CodeBuddy host HTTP request failed")
	}
	var response hostHTTPResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return hostHTTPResponse{}, fmt.Errorf("decode CodeBuddy host HTTP response")
	}
	return response, nil
}

func openHostHTTPStream(caller hostCaller, req hostHTTPRequest) (hostHTTPStreamResponse, error) {
	raw, errCall := caller.Call(pluginabi.MethodHostHTTPDoStream, req)
	if errCall != nil {
		return hostHTTPStreamResponse{}, fmt.Errorf("open CodeBuddy upstream stream")
	}
	var resp hostHTTPStreamResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return hostHTTPStreamResponse{}, fmt.Errorf("decode CodeBuddy upstream stream response")
	}
	resp.StreamID = strings.TrimSpace(resp.StreamID)
	if resp.StreamID == "" {
		return hostHTTPStreamResponse{}, fmt.Errorf("CodeBuddy upstream stream returned no stream ID")
	}
	return resp, nil
}

func readHostHTTPStream(caller hostCaller, streamID string) (hostHTTPStreamReadResponse, error) {
	raw, errCall := caller.Call(pluginabi.MethodHostHTTPStreamRead, hostHTTPStreamReadRequest{StreamID: streamID})
	if errCall != nil {
		return hostHTTPStreamReadResponse{}, fmt.Errorf("read CodeBuddy upstream stream")
	}
	var resp hostHTTPStreamReadResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return hostHTTPStreamReadResponse{}, fmt.Errorf("decode CodeBuddy upstream stream chunk")
	}
	return resp, nil
}

func closeHostHTTPStream(caller hostCaller, streamID string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = caller.Call(pluginabi.MethodHostHTTPStreamClose, hostHTTPStreamCloseRequest{StreamID: streamID})
}

func emitPluginStream(caller hostCaller, streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return fmt.Errorf("CodeBuddy plugin stream ID is empty")
	}
	_, errCall := caller.Call(pluginabi.MethodHostStreamEmit, pluginStreamEmitRequest{StreamID: streamID, Payload: payload})
	if errCall != nil {
		return fmt.Errorf("emit CodeBuddy stream chunk")
	}
	return nil
}

func closePluginStream(caller hostCaller, streamID, errorMessage, errorCode string, retryable bool, httpStatus int) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = caller.Call(pluginabi.MethodHostStreamClose, pluginStreamCloseRequest{
		StreamID:   streamID,
		Error:      strings.TrimSpace(errorMessage),
		ErrorCode:  strings.TrimSpace(errorCode),
		Retryable:  retryable,
		HTTPStatus: httpStatus,
	})
}
