package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

type hostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method"`
	URL            string      `json:"url"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
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
