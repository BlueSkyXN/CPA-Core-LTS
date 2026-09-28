package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
)

const provider = "zcode-coding-plan"
const maxBody = 16 * 1024 * 1024
const maxEvent = 4 * 1024 * 1024

type apiError struct {
	Status        int
	Code, Message string
}

func (e *apiError) Error() string                    { return e.Message }
func problem(status int, code, message string) error { return &apiError{status, code, message} }
func safeError(err error) *apiError {
	var e *apiError
	if errors.As(err, &e) {
		return e
	}
	return &apiError{502, "request_failed", "Request failed"}
}
func encode(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
}
func decode(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxBody*2 {
		return problem(400, "invalid_request", "Invalid request size")
	}
	if json.Unmarshal(raw, target) != nil {
		return problem(400, "invalid_request", "Invalid request JSON")
	}
	return nil
}
func envelope(v any, err error) []byte {
	if err != nil {
		e := safeError(err)
		return encode(map[string]any{"ok": false, "error": map[string]any{"code": rpcErrorCode(e), "message": e.Message, "http_status": e.Status, "retryable": false}})
	}
	return encode(map[string]any{"ok": true, "result": v})
}

type executorRequest struct {
	RequestID, ExecutionSessionID, CallerScope, WorkspaceIdentity string
	AuthID, AuthIndex, Model, Format                              string
	Stream                                                        bool
	Payload, StorageJSON, OriginalRequest                         []byte
	Headers                                                       http.Header
	StreamID                                                      string `json:"stream_id"`
	CallbackID                                                    string `json:"host_callback_id"`
}
type cancelRequest struct{ RequestID, ExecutionSessionID, CallerScope, WorkspaceIdentity, Provider, AuthID, AuthIndex, Scope string }
type hostCaller interface {
	Call(string, any) (json.RawMessage, error)
}
type httpRequest struct {
	CallbackID       string      `json:"host_callback_id,omitempty"`
	Method           string      `json:"method"`
	URL              string      `json:"url"`
	Headers          http.Header `json:"headers"`
	Body             []byte      `json:"body,omitempty"`
	DisableRedirects bool        `json:"disable_redirects"`
}
type httpResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}
type httpStream struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	StreamID   string      `json:"stream_id"`
}
type httpChunk struct {
	Payload []byte `json:"payload"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
}

func registration() any {
	return map[string]any{
		"schema_version": 6,
		"metadata":       map[string]any{"Name": provider, "Version": pluginVersion, "Author": "Coding Plan tool maintainers", "GitHubRepository": "https://github.com/BlueSkyXN/CPA-Core-LTS", "SensitiveEndpoints": []any{map[string]any{"Method": "POST", "PathSuffix": "/api/paas/c1f3a7e2/v2/client"}}, "ConfigFields": managementFields()},
		"capabilities":   map[string]any{"auth_provider": true, "auth_import_only": true, "model_provider": true, "executor": true, "execution_canceller": true, "execution_session_closer": true, "provider_readiness": true, "executor_model_scope": "oauth", "executor_input_formats": []string{"claude"}, "executor_output_formats": []string{"claude"}},
	}
}
