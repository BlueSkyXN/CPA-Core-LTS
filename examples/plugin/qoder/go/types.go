package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginIdentifier     = "qoder"
	pluginName           = "cpa-provider-qoder"
	pluginVersion        = "0.1.0"
	runnerProtocol       = 1
	runnerPATEnv         = "CPA_QODER_RUNNER_PAT"
	maxRunnerFrameBytes  = 1024 * 1024
	maxExecutorBodyBytes = 4 * 1024 * 1024
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type pluginCallError struct {
	code       string
	message    string
	statusCode int
	retryable  bool
}

func (e *pluginCallError) Error() string {
	if e == nil {
		return "Qoder plugin call failed"
	}
	return e.message
}

func newPluginCallError(code, message string, statusCode int, retryable bool) error {
	return &pluginCallError{code: code, message: message, statusCode: statusCode, retryable: retryable}
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(err error) []byte {
	callErr, ok := err.(*pluginCallError)
	if !ok {
		callErr = &pluginCallError{code: "plugin_error", message: "Qoder plugin call failed", statusCode: http.StatusInternalServerError}
	}
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{
		Code: callErr.code, Message: callErr.message, Retryable: callErr.retryable, HTTPStatus: callErr.statusCode,
	}})
	return raw
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ModelProvider          bool                         `json:"model_provider"`
	AuthProvider           bool                         `json:"auth_provider"`
	Executor               bool                         `json:"executor"`
	ExecutionCanceller     bool                         `json:"execution_canceller"`
	ProviderReadiness      bool                         `json:"provider_readiness"`
	ExecutionSessionCloser bool                         `json:"execution_session_closer"`
	ExecutorModelScope     pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats   []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats  []string                     `json:"executor_output_formats"`
	ManagementAPI          bool                         `json:"management_api"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID string `json:"stream_id,omitempty"`
}

type rpcAuthModelRequest struct {
	pluginapi.AuthModelRequest
}

type managementRegistrationResponse struct {
	Routes []managementRoute `json:"routes,omitempty"`
}

type managementRoute struct {
	Method string `json:"Method"`
	Path   string `json:"Path"`
}

type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcStreamResponse struct {
	Headers http.Header `json:"headers,omitempty"`
}

func decodeRequest[T any](raw []byte, target *T) error {
	if len(raw) == 0 {
		return fmt.Errorf("request is empty")
	}
	return json.Unmarshal(raw, target)
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
