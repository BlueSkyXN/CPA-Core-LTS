package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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

func testPATAuthJSON() []byte {
	return []byte(`{"type":"codebuddy","auth_mode":"pat","pat":"` + testSecret + `","label":"CodeBuddy Test PAT"}`)
}

func TestRegistrationDeclaresCodeBuddyG1Capabilities(t *testing.T) {
	got := pluginRegistration()
	if got.SchemaVersion != pluginabi.SchemaVersionExecutionLifecycle {
		t.Fatalf("schema version = %d", got.SchemaVersion)
	}
	if got.Metadata.Name == "" || got.Metadata.Version == "" || got.Metadata.Author == "" || got.Metadata.GitHubRepository == "" {
		t.Fatalf("required host metadata is incomplete: %#v", got.Metadata)
	}
	if !got.Capabilities.AuthProvider || !got.Capabilities.ModelProvider || !got.Capabilities.Executor || !got.Capabilities.ExecutionCanceller || !got.Capabilities.ProviderReadiness || !got.Capabilities.ManagementAPI {
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
	patRaw, _ := json.Marshal(pluginapi.AuthParseRequest{FileName: "codebuddy-pat.json", RawJSON: testPATAuthJSON()})
	pat, errPAT := parseAuthRequest(patRaw)
	if errPAT != nil || !pat.Handled || pat.Auth.Label != "CodeBuddy Test PAT" {
		t.Fatalf("PAT auth = %#v, err=%v", pat, errPAT)
	}
}

func TestModelsForAuthReturnsVerifiedExactModels(t *testing.T) {
	host := newFakeHost()
	runtime := newPluginRuntime(host)
	raw, _ := json.Marshal(rpcAuthModelRequest{AuthModelRequest: pluginapi.AuthModelRequest{StorageJSON: testAuthJSON()}, HostCallbackID: "catalog"})
	resp, errModels := runtime.modelsForAuth(raw)
	if errModels != nil {
		t.Fatalf("modelsForAuth() error = %v", errModels)
	}
	if resp.Provider != pluginIdentifier || len(resp.Models) != 2 || resp.Models[0].ID != codeBuddyModel || resp.Models[1].ID != codeBuddyPreviewModel {
		t.Fatalf("model response = %#v", resp)
	}
	for _, model := range resp.Models {
		if got := strings.Join(model.SupportedInputModalities, ","); got != "text" {
			t.Fatalf("model %s input modalities = %q, want text", model.ID, got)
		}
	}
}

func TestParseCodeBuddyCatalogSupportsCurrentAndCLIAgentShapes(t *testing.T) {
	current := []byte(`{"code":0,"data":{"enterpriseId":"enterprise-1","models":[{"id":"auto","name":"Auto"},{"id":"hy3","name":"Hy3","supportsImages":true,"supportsReasoning":true,"onlyReasoning":true,"maxInputTokens":192000,"maxOutputTokens":64000},{"id":"codewise-completions"}],"agents":[{"name":"craft","models":["auto","hy3"]},{"name":"ask","models":["hy3"]},{"name":"CodeCompletion","models":["codewise-completions"]}]}}`)
	catalog, errCurrent := parseCodeBuddyCatalog(current)
	if errCurrent != nil || len(catalog.Models) != 2 || catalog.Models[0].ID != "auto" || catalog.Models[1].ID != "hy3" || catalog.EnterpriseID != "enterprise-1" {
		t.Fatalf("current catalog = %#v, err=%v", catalog, errCurrent)
	}
	if got := strings.Join(catalog.Models[1].SupportedInputModalities, ","); got != "text,image" {
		t.Fatalf("hy3 modalities = %q", got)
	}
	if _, exposed := catalog.Allowed["codewise-completions"]; exposed {
		t.Fatal("completion-only model was exposed through the Chat provider")
	}
	cli := []byte(`{"code":0,"data":{"models":[{"id":"hy3","name":"Hy3"},{"id":"blocked","name":"Blocked"}],"agents":[{"name":"cli","models":["hy3"]}]}}`)
	filtered, errFiltered := parseCodeBuddyCatalog(cli)
	if errFiltered != nil || len(filtered.Models) != 1 || filtered.Models[0].ID != "hy3" {
		t.Fatalf("CLI catalog = %#v, err=%v", filtered, errFiltered)
	}
	if _, errUnsupportedShape := parseCodeBuddyCatalog([]byte(`{"code":0,"data":{"models":[{"id":"hy3"}],"agents":[{"name":"CodeCompletion","models":["hy3"]}]}}`)); errUnsupportedShape == nil {
		t.Fatal("catalog without a CLI or craft agent was treated as a Chat catalog")
	}
}

func TestCodeBuddyQuotaParsesPrecisePackagesAndEmptyResponse(t *testing.T) {
	if _, errMissing := parseCodeBuddyQuota([]byte(`{"code":0,"data":{}}`)); errMissing == nil {
		t.Fatal("billing response without an Accounts field was treated as zero quota")
	}
	empty, errEmpty := parseCodeBuddyQuota([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":null,"TotalCount":0,"TotalDosage":0}}}}`))
	if errEmpty != nil || empty.Status != "available" || empty.TotalExact != "0" || empty.Remaining == nil || *empty.Remaining != 0 {
		t.Fatalf("empty quota = %#v, err=%v", empty, errEmpty)
	}
	packages := []byte(`{"code":0,"data":{"Accounts":[{"PackageName":"base","Status":"active","CapacitySizePrecise":"1000.5000","CapacityUsedPrecise":"250.2500","CapacityRemainPrecise":"750.2500"},{"PackageName":"addon","Status":"active","CapacitySizePrecise":"2.25","CapacityUsedPrecise":"1.25","CapacityRemainPrecise":"1.00"}]}}`)
	quota, errPackages := parseCodeBuddyQuota(packages)
	if errPackages != nil || quota.Status != "available" || quota.TotalExact != "1002.75" || quota.UsedExact != "251.5" || quota.RemainingExact != "751.25" {
		t.Fatalf("package quota = %#v, err=%v", quota, errPackages)
	}
}

func TestCodeBuddyManagementSummaryUsesAuthIndexAndDoesNotExposeSecret(t *testing.T) {
	host := newFakeHost()
	runtime := newPluginRuntime(host)
	raw, errMarshal := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/plugins/codebuddy/summary",
			Query:  url.Values{"auth_index": {"1"}},
		},
		HostCallbackID: "management-callback",
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response, errHandle := runtime.handleManagement(raw)
	if errHandle != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("management response = %#v, err=%v", response, errHandle)
	}
	if strings.Contains(string(response.Body), testSecret) {
		t.Fatal("management response leaked secret")
	}
	var summary codeBuddySummary
	if errDecode := json.Unmarshal(response.Body, &summary); errDecode != nil {
		t.Fatal(errDecode)
	}
	if summary.Provider != pluginIdentifier || summary.AuthIndex != "1" || summary.Quota.Status != "available" || summary.Credential.Fingerprint == "" {
		t.Fatalf("summary = %#v", summary)
	}
	second, errSecond := runtime.handleManagement(raw)
	if errSecond != nil || !strings.Contains(string(second.Body), `"cached":true`) {
		t.Fatalf("cached summary = %s, err=%v", second.Body, errSecond)
	}
	if codeBuddySummaryCacheKey(codeBuddyAuth{APIKey: "same"}, pluginConfig{}, "1") == codeBuddySummaryCacheKey(codeBuddyAuth{APIKey: "same"}, pluginConfig{}, "2") {
		t.Fatal("summary cache key ignored auth index")
	}
}

func TestRequestPayloadPreservesToolsImagesAndConversationContext(t *testing.T) {
	raw := []byte(`{
		"model":"hy3",
		"messages":[
			{"role":"system","content":"Use the supplied context."},
			{"role":"user","content":"Remember marker ALPHA."},
			{"role":"assistant","content":"Stored."},
			{"role":"user","content":[
				{"type":"text","text":"Identify the image and call lookup."},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AA==","detail":"high"}}
			]}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"Return a marker","parameters":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}}}],
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"stream":true
	}`)
	payload, errPayload := codeBuddyRequestPayload(raw, codeBuddyModel)
	if errPayload != nil {
		t.Fatal(errPayload)
	}
	var body map[string]any
	if errDecode := json.Unmarshal(payload, &body); errDecode != nil {
		t.Fatal(errDecode)
	}
	messages, okMessages := body["messages"].([]any)
	tools, okTools := body["tools"].([]any)
	if !okMessages || len(messages) != 4 || !okTools || len(tools) != 1 {
		t.Fatalf("payload lost messages or tools: %s", payload)
	}
	last := messages[3].(map[string]any)
	content := last["content"].([]any)
	image := content[1].(map[string]any)["image_url"].(map[string]any)
	if image["url"] != "data:image/png;base64,AA==" || image["detail"] != "high" {
		t.Fatalf("image content changed: %#v", image)
	}
	choice, okChoice := body["tool_choice"].(string)
	if !okChoice || choice != "lookup" || body["model"] != codeBuddyModel || body["stream"] != true {
		t.Fatalf("tool choice or enforced fields changed: %s", payload)
	}
	if _, errMismatch := codeBuddyRequestPayload(raw, codeBuddyPreviewModel); errMismatch == nil {
		t.Fatal("payload model mismatch was accepted")
	}
	previewPayload, errPreview := codeBuddyRequestPayload(
		[]byte(`{"model":"hy3-preview-agent","messages":[{"role":"user","content":"reply OK"}],"stream":true}`),
		codeBuddyPreviewModel,
	)
	if errPreview != nil {
		t.Fatal(errPreview)
	}
	var previewBody map[string]any
	if errDecode := json.Unmarshal(previewPayload, &previewBody); errDecode != nil || previewBody["model"] != codeBuddyPreviewModel {
		t.Fatalf("preview payload = %s, err=%v", previewPayload, errDecode)
	}
}

func TestRequestPayloadNormalizesCodeBuddyToolChoice(t *testing.T) {
	raw := []byte(`{"model":"hy3","messages":[{"role":"user","content":"call lookup"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":{"type":"function","function":{"name":"lookup"}},"stream":true}`)
	payload, errPayload := codeBuddyRequestPayload(raw, codeBuddyModel)
	if errPayload != nil {
		t.Fatal(errPayload)
	}
	var body map[string]any
	if errDecode := json.Unmarshal(payload, &body); errDecode != nil {
		t.Fatal(errDecode)
	}
	if choice, ok := body["tool_choice"].(string); !ok || choice != "lookup" {
		t.Fatalf("tool_choice = %#v, want vendor function name string", body["tool_choice"])
	}

	for _, choice := range []string{"auto", "none", "required"} {
		payload, errPayload = codeBuddyRequestPayload([]byte(`{"model":"hy3","messages":[{"role":"user","content":"hello"}],"tool_choice":"`+choice+`","stream":true}`), codeBuddyModel)
		if errPayload != nil {
			t.Fatalf("string choice %q: %v", choice, errPayload)
		}
		if errDecode := json.Unmarshal(payload, &body); errDecode != nil || body["tool_choice"] != choice {
			t.Fatalf("string tool_choice = %#v, want %q", body["tool_choice"], choice)
		}
	}

	if _, errInvalid := codeBuddyRequestPayload([]byte(`{"model":"hy3","messages":[{"role":"user","content":"hello"}],"tool_choice":{"type":"function"},"stream":true}`), codeBuddyModel); errInvalid == nil {
		t.Fatal("invalid function tool_choice was accepted")
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

func TestSSERejectsOversizedUnterminatedTailAfterShortLine(t *testing.T) {
	validator := &sseValidator{}
	payload := append([]byte(": keepalive\n"), bytes.Repeat([]byte{'x'}, maxSSELineBytes+1)...)
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

func TestConfigureResumesQuiescedRuntimeWithRetainedExecution(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.active["persisted"] = &activeExecution{done: make(chan struct{})}
	runtime.quiesce()
	if errConfigure := runtime.configure(nil); errConfigure != nil {
		t.Fatalf("configure() error = %v", errConfigure)
	}
	runtime.mu.Lock()
	accepting := runtime.accepting
	runtime.mu.Unlock()
	if !accepting {
		t.Fatal("successful reconfigure did not resume a quiesced runtime with a retained execution")
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
			Payload:      []byte(`{"model":"hy3","messages":[{"role":"user","content":"reply OK"}],"stream":true}`),
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
	httpRequests       []hostHTTPRequest
	catalogResponse    hostHTTPResponse
	billingResponse    hostHTTPResponse
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
		catalogResponse: hostHTTPResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"code":0,"data":{"models":[{"id":"hy3","name":"Hy3"},{"id":"hy3-preview-agent","name":"Hy3 Preview Agent"}],"agents":[{"name":"craft","models":["hy3","hy3-preview-agent"]}]}}`),
		},
		billingResponse: hostHTTPResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[],"TotalCount":0,"TotalDosage":0}}}}`),
		},
		upstreamClosed: make(chan struct{}),
		pluginClosed:   make(chan struct{}),
	}
}

func (h *fakeHost) Call(method string, payload any) (json.RawMessage, error) {
	switch method {
	case pluginabi.MethodHostHTTPDo:
		req, ok := payload.(hostHTTPRequest)
		if !ok {
			return nil, errors.New("unexpected HTTP request")
		}
		h.mu.Lock()
		h.httpRequests = append(h.httpRequests, req)
		response := h.catalogResponse
		if strings.Contains(req.URL, "/v2/billing/") {
			response = h.billingResponse
		}
		h.mu.Unlock()
		return marshalFakeResult(response)
	case pluginabi.MethodHostAuthGet:
		return marshalFakeResult(pluginapi.HostAuthGetResponse{AuthIndex: "1", Name: "codebuddy.json", JSON: testAuthJSON()})
	case pluginabi.MethodHostAuthGetRuntime:
		return marshalFakeResult(pluginapi.HostAuthGetRuntimeResponse{Auth: pluginapi.HostAuthFileEntry{AuthIndex: "1", Name: "codebuddy.json", Label: "CodeBuddy Test"}})
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
