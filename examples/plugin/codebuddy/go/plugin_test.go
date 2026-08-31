package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const testSecret = "unit-test-codebuddy-secret"

func testAuthJSON() []byte {
	return []byte(`{"type":"codebuddy","auth_mode":"api_key","api_key":"` + testSecret + `"}`)
}

func TestRegistrationDeclaresCodeBuddyG1Capabilities(t *testing.T) {
	got := pluginRegistration()
	if got.SchemaVersion != pluginabi.SchemaVersionExecutionLifecycle {
		t.Fatalf("schema version = %d", got.SchemaVersion)
	}
	if got.Metadata.Name == "" || got.Metadata.Version == "" || got.Metadata.Author == "" || got.Metadata.GitHubRepository == "" {
		t.Fatalf("required host metadata is incomplete: %#v", got.Metadata)
	}
	if !got.Capabilities.AuthProvider || !got.Capabilities.ModelProvider || !got.Capabilities.Executor || !got.Capabilities.ExecutionCanceller || !got.Capabilities.ProviderReadiness {
		t.Fatalf("required capabilities are missing: %#v", got.Capabilities)
	}
	if got.Capabilities.ExecutionSessionCloser {
		t.Fatal("CodeBuddy G1 must not advertise execution_session_closer")
	}
	if got.Capabilities.ExecutorModelScope != pluginapi.ExecutorModelScopeOAuth {
		t.Fatalf("executor scope = %q", got.Capabilities.ExecutorModelScope)
	}
}

func TestParseAuthRecognizesOnlyValidCodeBuddyAPIKeys(t *testing.T) {
	validRaw, _ := json.Marshal(pluginapi.AuthParseRequest{FileName: "codebuddy.json", RawJSON: testAuthJSON()})
	valid, errValid := parseAuthRequest(validRaw)
	if errValid != nil || !valid.Handled {
		t.Fatalf("valid auth = %#v, err=%v", valid, errValid)
	}
	if !bytes.Equal(valid.Auth.StorageJSON, testAuthJSON()) || valid.Auth.Provider != pluginIdentifier {
		t.Fatalf("parsed auth = %#v", valid.Auth)
	}
	metadataRaw, _ := json.Marshal(valid.Auth.Metadata)
	if bytes.Contains(metadataRaw, []byte(testSecret)) {
		t.Fatal("secret leaked into auth metadata")
	}

	otherRaw, _ := json.Marshal(pluginapi.AuthParseRequest{RawJSON: []byte(`{"type":"qoder","api_key":"ignored"}`)})
	other, errOther := parseAuthRequest(otherRaw)
	if errOther != nil || other.Handled {
		t.Fatalf("other auth = %#v, err=%v", other, errOther)
	}

	missingRaw, _ := json.Marshal(pluginapi.AuthParseRequest{RawJSON: []byte(`{"type":"codebuddy","auth_mode":"api_key"}`)})
	if _, errMissing := parseAuthRequest(missingRaw); errMissing == nil || strings.Contains(errMissing.Error(), testSecret) {
		t.Fatalf("missing-key error = %v", errMissing)
	}
}

func TestModelsForAuthReturnsOnlyVerifiedModel(t *testing.T) {
	raw, _ := json.Marshal(rpcAuthModelRequest{AuthModelRequest: pluginapi.AuthModelRequest{StorageJSON: testAuthJSON()}})
	resp, errModels := modelsForAuth(raw)
	if errModels != nil {
		t.Fatalf("modelsForAuth() error = %v", errModels)
	}
	if resp.Provider != pluginIdentifier || len(resp.Models) != 1 || resp.Models[0].ID != codeBuddyModel {
		t.Fatalf("model response = %#v", resp)
	}
}

func TestReadinessIsSecretSafeAndRequiresSelectedAuth(t *testing.T) {
	runtime := newPluginRuntime(newFakeHost())
	ready := runtime.readiness(pluginapi.ReadinessRequest{AuthID: "auth-1", StorageJSON: testAuthJSON()})
	if !ready.Ready || readinessState(ready, pluginapi.ReadinessLevelRunnerInstalled) != pluginapi.ReadinessStateReady || readinessState(ready, pluginapi.ReadinessLevelSessionReady) != pluginapi.ReadinessStateUnsupported {
		t.Fatalf("readiness = %#v", ready)
	}
	raw, _ := json.Marshal(ready)
	if bytes.Contains(raw, []byte(testSecret)) {
		t.Fatal("secret leaked into readiness diagnostics")
	}

	notReady := runtime.readiness(pluginapi.ReadinessRequest{AuthID: "auth-1", StorageJSON: []byte(`{"type":"codebuddy","auth_mode":"api_key"}`)})
	if notReady.Ready || readinessState(notReady, pluginapi.ReadinessLevelAuthReady) != pluginapi.ReadinessStateNotReady {
		t.Fatalf("invalid-auth readiness = %#v", notReady)
	}
}

func readinessState(resp pluginapi.ReadinessResponse, level pluginapi.ReadinessLevel) pluginapi.ReadinessState {
	for _, check := range resp.Checks {
		if check.Level == level {
			return check.State
		}
	}
	return ""
}

func TestNonStreamReturnsStableClientErrorWithoutUpstreamCall(t *testing.T) {
	host := newFakeHost()
	runtime := newPluginRuntime(host)
	raw, errDispatch := runtime.dispatch(pluginabi.MethodExecutorExecute, []byte(`{}`))
	if errDispatch == nil || raw != nil {
		t.Fatalf("dispatch raw=%s err=%v", raw, errDispatch)
	}
	encoded := errorEnvelope(errDispatch)
	var env envelope
	if errDecode := json.Unmarshal(encoded, &env); errDecode != nil {
		t.Fatal(errDecode)
	}
	if env.Error == nil || env.Error.Code != "stream_required" || env.Error.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("error envelope = %#v", env.Error)
	}
	if host.openCount() != 0 {
		t.Fatal("non-stream request contacted upstream")
	}
}

func TestExecuteStreamUsesExactHeadersAndForwardsSSE(t *testing.T) {
	host := newFakeHost()
	host.reads = []hostHTTPStreamReadResponse{
		{Payload: []byte("data: {\"id\":\"one\",\"choices\":[]}" + "\n\n")},
		{Payload: []byte("data: [DO")},
		{Payload: []byte("NE]\n\n"), Done: true},
	}
	runtime := newPluginRuntime(host)
	resp, errExecute := runtime.executeStream(executorRequestJSON(t, "req-success"))
	if errExecute != nil {
		t.Fatalf("executeStream() error = %v", errExecute)
	}
	if resp.Headers.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("headers = %#v", resp.Headers)
	}
	closeEvent := host.waitPluginClose(t)
	if closeEvent.Error != "" {
		t.Fatalf("plugin close error = %q", closeEvent.Error)
	}
	request := host.lastOpenRequest()
	if request.Method != http.MethodPost || request.URL != defaultCodeBuddyEndpoint {
		t.Fatalf("upstream request = %#v", request)
	}
	if request.Headers.Get("Authorization") != "Bearer "+testSecret || request.Headers.Get("X-API-Key") != testSecret {
		t.Fatal("dual CodeBuddy authentication headers were not sent")
	}
	if request.Headers.Get("Accept") != "text/event-stream" || request.Headers.Get("Content-Type") != "application/json" || request.Headers.Get("User-Agent") == "" {
		t.Fatalf("upstream headers = %#v", redactedHeaders(request.Headers))
	}
	var body map[string]any
	if errDecode := json.Unmarshal(request.Body, &body); errDecode != nil || body["model"] != codeBuddyModel || body["stream"] != true {
		t.Fatalf("upstream body = %#v err=%v", body, errDecode)
	}
	emitted := host.emittedPayloads()
	if len(emitted) != 1 || !json.Valid(emitted[0]) || bytes.Contains(emitted[0], []byte("data:")) || bytes.Contains(bytes.Join(emitted, nil), []byte("[DONE]")) {
		t.Fatalf("emitted provider payloads = %q", emitted)
	}
}

func TestExecuteStreamMapsHTTPErrorWithoutLeakingBody(t *testing.T) {
	host := newFakeHost()
	host.openResponse.StatusCode = http.StatusTooManyRequests
	runtime := newPluginRuntime(host)
	_, errExecute := runtime.executeStream(executorRequestJSON(t, "req-http-error"))
	callErr, ok := errExecute.(*pluginCallError)
	if !ok || callErr.statusCode != http.StatusTooManyRequests || !callErr.retryable {
		t.Fatalf("HTTP error = %#v", errExecute)
	}
	if strings.Contains(errExecute.Error(), testSecret) {
		t.Fatal("secret leaked into HTTP error")
	}
	if host.httpCloseCount() != 1 {
		t.Fatalf("HTTP close count = %d", host.httpCloseCount())
	}
}

func TestMalformedSSEClosesStreamWithBoundedError(t *testing.T) {
	host := newFakeHost()
	host.reads = []hostHTTPStreamReadResponse{{Payload: []byte("data: not-json\n\n"), Done: true}}
	runtime := newPluginRuntime(host)
	if _, errExecute := runtime.executeStream(executorRequestJSON(t, "req-malformed")); errExecute != nil {
		t.Fatalf("executeStream() error = %v", errExecute)
	}
	closed := host.waitPluginClose(t)
	if closed.Error != "CodeBuddy upstream returned malformed SSE data" || strings.Contains(closed.Error, testSecret) {
		t.Fatalf("close error = %q", closed.Error)
	}
}

func TestSSERejectsDataAfterDoneInSameChunk(t *testing.T) {
	validator := &sseValidator{}
	_, errConsume := validator.consume([]byte("data: [DONE]\n\ndata: {\"choices\":[]}\n\n"))
	if errConsume == nil || !strings.Contains(errConsume.Error(), "after [DONE]") {
		t.Fatalf("consume() error = %v", errConsume)
	}
}

func TestSSERejectsOversizedTerminatedLine(t *testing.T) {
	validator := &sseValidator{}
	payload := append(bytes.Repeat([]byte{'x'}, maxSSELineBytes+1), '\n')
	_, errConsume := validator.consume(payload)
	if errConsume == nil || !strings.Contains(errConsume.Error(), "bounded limit") {
		t.Fatalf("consume() error = %v", errConsume)
	}
}

func TestSSENormalizesNonTerminalFinishReasonAndEmptyFunctionCall(t *testing.T) {
	validator := &sseValidator{}
	frames, errConsume := validator.consume([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\",\"function_call\":{\"name\":\"\",\"arguments\":\"\"},\"tool_calls\":[],\"reasoning_content\":\"\",\"refusal\":\"\",\"extra_fields\":null},\"finish_reason\":\"\"}]}\n\n"))
	if errConsume != nil || len(frames) != 1 {
		t.Fatalf("consume() frames=%q error=%v", frames, errConsume)
	}
	var frame map[string]any
	if errDecode := json.Unmarshal(frames[0], &frame); errDecode != nil {
		t.Fatal(errDecode)
	}
	choice := frame["choices"].([]any)[0].(map[string]any)
	if _, exists := choice["finish_reason"]; exists {
		t.Fatalf("finish_reason = %#v, want omitted", choice["finish_reason"])
	}
	delta := choice["delta"].(map[string]any)
	if _, exists := delta["function_call"]; exists {
		t.Fatalf("function_call = %#v, want omitted", delta["function_call"])
	}
	for _, key := range []string{"tool_calls", "reasoning_content", "refusal", "extra_fields"} {
		if _, exists := delta[key]; exists {
			t.Fatalf("%s = %#v, want omitted", key, delta[key])
		}
	}
}

func TestExecuteStreamRequiresHostCallbackContext(t *testing.T) {
	host := newFakeHost()
	runtime := newPluginRuntime(host)
	var req rpcExecutorRequest
	if errDecode := json.Unmarshal(executorRequestJSON(t, "req-no-callback"), &req); errDecode != nil {
		t.Fatal(errDecode)
	}
	req.HostCallbackID = ""
	raw, _ := json.Marshal(req)
	_, errExecute := runtime.executeStream(raw)
	if errExecute == nil || !strings.Contains(errExecute.Error(), "host callback") {
		t.Fatalf("executeStream() error = %v", errExecute)
	}
	if host.openCount() != 0 {
		t.Fatal("missing callback request contacted upstream")
	}
}

func TestCancelIsIdempotentUnderRace(t *testing.T) {
	host := newFakeHost()
	host.blockReads = true
	runtime := newPluginRuntime(host)
	if _, errExecute := runtime.executeStream(executorRequestJSON(t, "req-cancel")); errExecute != nil {
		t.Fatalf("executeStream() error = %v", errExecute)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.cancel("req-cancel")
		}()
	}
	wg.Wait()
	closed := host.waitPluginClose(t)
	if closed.Error != "CodeBuddy stream canceled" {
		t.Fatalf("close error = %q", closed.Error)
	}
	rawClose, errMarshal := json.Marshal(closed)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var closeFields map[string]any
	if errDecode := json.Unmarshal(rawClose, &closeFields); errDecode != nil {
		t.Fatal(errDecode)
	}
	if closeFields["error_code"] != "connection_lifecycle" || closeFields["retryable"] != true {
		t.Fatalf("cancel close lifecycle fields = %s, want typed retryable connection_lifecycle", rawClose)
	}
	if _, exists := closeFields["http_status"]; exists {
		t.Fatalf("cancel close unexpectedly attached HTTP status: %s", rawClose)
	}
	if host.httpCloseCount() != 1 || host.pluginCloseCount() != 1 {
		t.Fatalf("close counts http=%d plugin=%d", host.httpCloseCount(), host.pluginCloseCount())
	}
}

func TestQuiesceCancelsAndDrainsActiveStream(t *testing.T) {
	host := newFakeHost()
	host.blockReads = true
	runtime := newPluginRuntime(host)
	if _, errExecute := runtime.executeStream(executorRequestJSON(t, "req-quiesce")); errExecute != nil {
		t.Fatalf("executeStream() error = %v", errExecute)
	}
	runtime.quiesceAndWait()
	closed := host.waitPluginClose(t)
	if closed.Error != "CodeBuddy stream canceled" {
		t.Fatalf("close error = %q", closed.Error)
	}
	if _, errExecute := runtime.executeStream(executorRequestJSON(t, "req-after-quiesce")); errExecute == nil {
		t.Fatal("quiescing runtime accepted a new stream")
	}
}

func TestSecretDoesNotAppearInRegistrationReadinessOrErrors(t *testing.T) {
	runtime := newPluginRuntime(newFakeHost())
	values := []any{
		pluginRegistration(),
		runtime.readiness(pluginapi.ReadinessRequest{AuthID: "auth", StorageJSON: testAuthJSON()}),
		envelope{OK: false, Error: &envelopeError{Code: "invalid_auth", Message: "selected CodeBuddy credential is invalid", HTTPStatus: 401}},
	}
	for _, value := range values {
		raw, errMarshal := json.Marshal(value)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if bytes.Contains(raw, []byte(testSecret)) {
			t.Fatalf("secret leaked from %T", value)
		}
	}
}

func executorRequestJSON(t *testing.T, requestID string) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(rpcExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			RequestID:    requestID,
			AuthID:       "auth-1",
			AuthIndex:    "1",
			AuthProvider: pluginIdentifier,
			Model:        codeBuddyModel,
			Stream:       true,
			Payload:      []byte(`{"model":"hy3-preview-agent","messages":[{"role":"user","content":"reply OK"}],"stream":true}`),
			StorageJSON:  testAuthJSON(),
		},
		StreamID:       "plugin-" + requestID,
		HostCallbackID: "callback-" + requestID,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return raw
}

func redactedHeaders(headers http.Header) http.Header {
	out := headers.Clone()
	if out.Get("Authorization") != "" {
		out.Set("Authorization", "[REDACTED]")
	}
	if out.Get("X-API-Key") != "" {
		out.Set("X-API-Key", "[REDACTED]")
	}
	return out
}

type fakeHost struct {
	mu                 sync.Mutex
	openResponse       hostHTTPStreamResponse
	openRequests       []hostHTTPRequest
	reads              []hostHTTPStreamReadResponse
	readIndex          int
	emitted            [][]byte
	pluginCloses       []pluginStreamCloseRequest
	httpCloses         int
	blockReads         bool
	upstreamClosed     chan struct{}
	upstreamClosedOnce sync.Once
	pluginClosed       chan struct{}
	pluginClosedOnce   sync.Once
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		openResponse: hostHTTPStreamResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": {"text/event-stream"}},
			StreamID:   "upstream-1",
		},
		upstreamClosed: make(chan struct{}),
		pluginClosed:   make(chan struct{}),
	}
}

func (h *fakeHost) Call(method string, payload any) (json.RawMessage, error) {
	switch method {
	case pluginabi.MethodHostHTTPDoStream:
		req, ok := payload.(hostHTTPRequest)
		if !ok {
			return nil, errors.New("unexpected open request")
		}
		h.mu.Lock()
		h.openRequests = append(h.openRequests, req)
		resp := h.openResponse
		h.mu.Unlock()
		return marshalFakeResult(resp)
	case pluginabi.MethodHostHTTPStreamRead:
		h.mu.Lock()
		block := h.blockReads
		if !block && h.readIndex < len(h.reads) {
			resp := h.reads[h.readIndex]
			h.readIndex++
			h.mu.Unlock()
			return marshalFakeResult(resp)
		}
		h.mu.Unlock()
		if block {
			<-h.upstreamClosed
			return marshalFakeResult(hostHTTPStreamReadResponse{Done: true})
		}
		return marshalFakeResult(hostHTTPStreamReadResponse{Done: true})
	case pluginabi.MethodHostHTTPStreamClose:
		h.mu.Lock()
		h.httpCloses++
		h.mu.Unlock()
		h.upstreamClosedOnce.Do(func() { close(h.upstreamClosed) })
		return marshalFakeResult(struct{}{})
	case pluginabi.MethodHostStreamEmit:
		req, ok := payload.(pluginStreamEmitRequest)
		if !ok {
			return nil, errors.New("unexpected emit request")
		}
		h.mu.Lock()
		h.emitted = append(h.emitted, bytes.Clone(req.Payload))
		h.mu.Unlock()
		return marshalFakeResult(struct{}{})
	case pluginabi.MethodHostStreamClose:
		req, ok := payload.(pluginStreamCloseRequest)
		if !ok {
			return nil, errors.New("unexpected plugin close request")
		}
		h.mu.Lock()
		h.pluginCloses = append(h.pluginCloses, req)
		h.mu.Unlock()
		h.pluginClosedOnce.Do(func() { close(h.pluginClosed) })
		return marshalFakeResult(struct{}{})
	default:
		return nil, errors.New("unexpected host method")
	}
}

func marshalFakeResult(value any) (json.RawMessage, error) {
	raw, errMarshal := json.Marshal(value)
	return json.RawMessage(raw), errMarshal
}

func (h *fakeHost) waitPluginClose(t *testing.T) pluginStreamCloseRequest {
	t.Helper()
	select {
	case <-h.pluginClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for plugin stream close")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pluginCloses) == 0 {
		t.Fatal("plugin stream close was not recorded")
	}
	return h.pluginCloses[len(h.pluginCloses)-1]
}

func (h *fakeHost) lastOpenRequest() hostHTTPRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.openRequests) == 0 {
		return hostHTTPRequest{}
	}
	return h.openRequests[len(h.openRequests)-1]
}

func (h *fakeHost) emittedPayloads() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([][]byte, 0, len(h.emitted))
	for _, payload := range h.emitted {
		out = append(out, bytes.Clone(payload))
	}
	return out
}

func (h *fakeHost) openCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.openRequests)
}

func (h *fakeHost) httpCloseCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.httpCloses
}

func (h *fakeHost) pluginCloseCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pluginCloses)
}
