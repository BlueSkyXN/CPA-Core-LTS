package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

type hostCaller interface {
	Call(method string, payload any) (json.RawMessage, error)
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

func emitPluginStream(caller hostCaller, streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return fmt.Errorf("Qoder plugin stream ID is empty")
	}
	_, errCall := caller.Call(pluginabi.MethodHostStreamEmit, pluginStreamEmitRequest{StreamID: streamID, Payload: payload})
	if errCall != nil {
		return fmt.Errorf("emit Qoder stream chunk")
	}
	return nil
}

func closePluginStream(caller hostCaller, streamID, errorMessage, errorCode string, retryable bool, httpStatus int) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = caller.Call(pluginabi.MethodHostStreamClose, pluginStreamCloseRequest{
		StreamID: streamID, Error: strings.TrimSpace(errorMessage), ErrorCode: strings.TrimSpace(errorCode),
		Retryable: retryable, HTTPStatus: httpStatus,
	})
}
