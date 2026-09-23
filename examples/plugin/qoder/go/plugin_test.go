package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAuthModesAndSecretSafeErrors(t *testing.T) {
	secret := "pt-test-secret-never-log"
	patJSON := []byte(`{"type":"qoder","auth_mode":"pat","pat":"` + secret + `"}`)
	pat, errPAT := parseStoredAuth(patJSON)
	if errPAT != nil || pat.AuthMode != "pat" || pat.PAT != secret {
		t.Fatalf("parse PAT = %#v, %v", pat, errPAT)
	}
	direct, errDirect := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","transport":"direct_openai","pat":"pt-fixture"}`))
	if errDirect != nil || direct.AuthMode != "pat" || direct.PAT != "pt-fixture" {
		t.Fatalf("parse direct compatibility transport = %#v, %v", direct, errDirect)
	}
	if _, errSDK := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","transport":"sdk_cli","pat":"pt-fixture"}`)); errSDK == nil {
		t.Fatal("sdk_cli transport was accepted")
	}
	if _, errUnknown := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","transport":"carrier_pigeon","pat":"pt-fixture"}`)); errUnknown == nil {
		t.Fatal("unknown transport was accepted")
	}
	legacy, errLegacy := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","access_token":"pt-legacy","account_id":"legacy-account"}`))
	if errLegacy != nil || legacy.tokenSource() != "pt-legacy" {
		t.Fatalf("legacy access_token = %#v, err=%v", legacy, errLegacy)
	}
	legacyBearer, errLegacyBearer := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","access_token":"legacy-bearer"}`))
	if errLegacyBearer != nil || legacyBearer.isPAT() {
		t.Fatalf("legacy bearer access_token = %#v, err=%v", legacyBearer, errLegacyBearer)
	}
	if _, errLocal := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"local_cli","profile_id":"cn-main","config_dir":"/tmp/qoder-cn"}`)); errLocal == nil {
		t.Fatal("local_cli auth was accepted")
	}
	if _, errLocalPAT := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"local_cli","pat":"pt-invalid-mix","profile_id":"cn-main","config_dir":"/tmp/qoder-cn"}`)); errLocalPAT == nil {
		t.Fatal("local_cli with the new pat field was accepted")
	}
	if _, errMissingMode := parseStoredAuth([]byte(`{"type":"qoder","pat":"pt-fixture"}`)); errMissingMode == nil {
		t.Fatal("missing auth_mode was accepted")
	}
	if _, errWhitespace := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","pat":" pt-valid "}`)); errWhitespace == nil {
		t.Fatal("PAT with surrounding whitespace was accepted")
	}
	if _, errLegacyWhitespace := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","access_token":" pt-legacy "}`)); errLegacyWhitespace == nil {
		t.Fatal("legacy access_token with surrounding whitespace was accepted")
	}
	if _, errExplicitOpaquePAT := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","pat":"opaque","access_token":"opaque"}`)); errExplicitOpaquePAT == nil {
		t.Fatal("explicit non-pt PAT was accepted through the legacy access_token field")
	}
	safe := string(errorEnvelope(newPluginCallError("invalid_auth", "Qoder authentication failed", 401, false)))
	if strings.Contains(safe, secret) {
		t.Fatalf("error envelope leaked secret: %s", safe)
	}
}

func TestConfigureResumesQuiescedRuntime(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.quiesce()
	if errConfigure := runtime.configure(nil); errConfigure != nil {
		t.Fatalf("configure() error = %v", errConfigure)
	}
	runtime.mu.Lock()
	accepting := runtime.accepting
	runtime.mu.Unlock()
	if !accepting {
		t.Fatal("successful reconfigure did not resume a quiesced runtime with a retained session")
	}
}

func TestCanonicalModelIDsStayExact(t *testing.T) {
	found := false
	for _, id := range canonicalQoderModelIDs {
		if id == "qfmodel" {
			found = true
		}
	}
	if !found {
		t.Fatal("qfmodel canonical model missing")
	}
	if errAlias := validateCanonicalModel("qwen3.8-flash"); errAlias == nil {
		t.Fatal("normalized guessed alias was accepted")
	}
	if errDisplay := validateCanonicalModel("Qwen3.8-Flash"); errDisplay == nil {
		t.Fatal("display name was accepted as an executable model ID")
	}
	if errExact := validateCanonicalModel("qfmodel"); errExact != nil {
		t.Fatalf("exact canonical ID rejected: %v", errExact)
	}
	if errLive := validateCanonicalModel("brand-new-live-model"); errLive != nil {
		t.Fatalf("live catalog ID rejected: %v", errLive)
	}
	if errEmpty := validateCanonicalModel("  "); errEmpty == nil {
		t.Fatal("empty model was accepted")
	}
}
func TestLiveModelMetadataPreservesVisionReasoningAndContext(t *testing.T) {
	model := qoderModelInfo(qoderCatalogModel{
		ID: "qmodel_38max", DisplayName: "Qwen3.8-Max", IsVL: true, IsReasoning: true,
		MaxInputTokens: 128000, MaxOutputTokens: 32768,
		ReasoningEfforts: []string{"low", "high"}, SupportsDisabled: true,
		AvailableContextWindows: []int64{128000, 200000}, DefaultContextWindow: 128000,
	})
	if strings.Join(model.SupportedInputModalities, ",") != "text,image" || model.ContextLength != 200000 {
		t.Fatalf("model modalities/context = %#v / %d", model.SupportedInputModalities, model.ContextLength)
	}
	if model.InputTokenLimit != 128000 || model.OutputTokenLimit != 32768 || model.MaxCompletionTokens != 32768 {
		t.Fatalf("model token limits = %#v", model)
	}
	if model.Thinking == nil || !model.Thinking.ZeroAllowed || strings.Join(model.Thinking.Levels, ",") != "low,high" {
		t.Fatalf("model thinking = %#v", model.Thinking)
	}
}

func TestNativeDirectConfigValidation(t *testing.T) {
	direct, errDirect := decodePluginConfig([]byte(`
direct_endpoint: https://api2-v2.example.test/model/v1/chat/completions
openapi_endpoint: https://openapi.example.test
direct_models:
  - id: qfmodel
    display_name: Qwen3.8-Flash
`))
	if errDirect != nil || direct.Transport != "direct_openai" || len(direct.DirectModels) != 1 || direct.DirectAuthEndpoint != "https://openapi.example.test" {
		t.Fatalf("direct config = %#v, %v", direct, errDirect)
	}
	empty, errEmpty := decodePluginConfig(nil)
	if errEmpty != nil || empty.Transport != "direct_openai" ||
		empty.DirectEndpoint != "https://gateway.qoder.com.cn/model/v1/chat/completions" ||
		empty.DirectModelsEndpoint != "https://gateway.qoder.com.cn/algo/api/v2/model/list" ||
		empty.OpenAPIEndpoint != "https://openapi.qoder.com.cn" ||
		empty.DirectCatalogFormat != "qoder" || empty.DirectTokenMode != "auto" {
		t.Fatalf("empty config native defaults = %#v, %v", empty, errEmpty)
	}
	if _, errSDKCLI := decodePluginConfig([]byte("transport: sdk_cli\n")); errSDKCLI == nil {
		t.Fatal("sdk_cli transport config was accepted")
	}
	if _, errBadTransport := decodePluginConfig([]byte("transport: carrier_pigeon\n")); errBadTransport == nil {
		t.Fatal("unknown transport config was accepted")
	}
	compatTransport, errCompat := decodePluginConfig([]byte("transport: direct_openai\n"))
	if errCompat != nil || compatTransport.Transport != "direct_openai" {
		t.Fatalf("direct_openai compatibility transport = %#v, %v", compatTransport, errCompat)
	}
	if _, errNoOpenAPI := decodePluginConfig([]byte(`
direct_endpoint: https://api2-v2.example.test/model/v1/chat/completions
direct_models:
  - id: qfmodel
`)); errNoOpenAPI != nil {
		t.Fatalf("config without an auth endpoint was rejected: %v", errNoOpenAPI)
	}
	bearer, errBearer := decodePluginConfig([]byte(`
direct_endpoint: https://api2-v2.example.test/model/v1/chat/completions
direct_token_mode: bearer
direct_auth_endpoint: https://openapi.example.test
direct_models:
  - id: qfmodel
`))
	if errBearer != nil || bearer.DirectTokenMode != "bearer" || bearer.OpenAPIEndpoint != "https://openapi.example.test" || bearer.DirectAuthEndpoint != bearer.OpenAPIEndpoint {
		t.Fatalf("legacy bearer config = %#v, %v", bearer, errBearer)
	}
	if _, errPlainHTTP := decodePluginConfig([]byte("direct_endpoint: http://api.example.test/v1\n")); errPlainHTTP == nil {
		t.Fatal("plain-HTTP non-loopback endpoint was accepted")
	}
	if _, errLoopback := decodePluginConfig([]byte("direct_endpoint: http://127.0.0.1:8317/v1\n")); errLoopback != nil {
		t.Fatalf("loopback HTTP endpoint was rejected: %v", errLoopback)
	}
}
func TestRegistrationDeclaresSchema5LifecycleCapabilities(t *testing.T) {
	registration := pluginRegistration()
	if registration.SchemaVersion != pluginabi.SchemaVersion || registration.SchemaVersion < pluginabi.SchemaVersionExecutionLifecycle {
		t.Fatalf("schema version = %d", registration.SchemaVersion)
	}
	capabilities := registration.Capabilities
	if !capabilities.AuthProvider || !capabilities.ModelProvider || !capabilities.Executor ||
		!capabilities.ExecutionCanceller || !capabilities.ExecutionSessionCloser || !capabilities.ProviderReadiness || !capabilities.ManagementAPI {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if registration.Metadata.GitHubRepository != "https://github.com/BlueSkyXN/CPA-Core-LTS" {
		t.Fatalf("metadata = %#v", registration.Metadata)
	}
}

func TestSessionKeyIncludesAllOwnershipDimensions(t *testing.T) {
	auth := qoderAuth{Type: "qoder", AuthMode: "pat", PAT: "pt-profile-test"}
	base := pluginapi.ExecutorRequest{
		RequestID: "request-1", ExecutionSessionID: "session-1", CallerScope: "caller-1", WorkspaceIdentity: "workspace-1",
		AuthID: "auth-1", AuthIndex: "index-1",
	}
	want := executionSessionKey(base, auth)
	mutations := []func(*pluginapi.ExecutorRequest){
		func(req *pluginapi.ExecutorRequest) { req.AuthID = "auth-2" },
		func(req *pluginapi.ExecutorRequest) { req.AuthIndex = "index-2" },
		func(req *pluginapi.ExecutorRequest) { req.ExecutionSessionID = "session-2" },
		func(req *pluginapi.ExecutorRequest) { req.CallerScope = "caller-2" },
		func(req *pluginapi.ExecutorRequest) { req.WorkspaceIdentity = "workspace-2" },
	}
	for index, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if got := executionSessionKey(candidate, auth); got == want {
			t.Fatalf("mutation %d did not change session key", index)
		}
	}
	if executionSessionKey(base, qoderAuth{Type: "qoder", AuthMode: "pat", PAT: "pt-profile-other"}) == want {
		t.Fatal("token source mutation did not change session key")
	}
}

func TestCancelMatchesAllSuppliedOwnershipDimensions(t *testing.T) {
	session := &executionIdentity{
		authID: "auth-1", authIndex: "index-1", executionSessionID: "session-1",
		callerScope: "caller-1", workspaceIdentity: "workspace-1",
	}
	base := pluginapi.CancelExecutionRequest{
		ExecutionSessionID: "session-1", CallerScope: "caller-1", WorkspaceIdentity: "workspace-1",
		Provider: pluginIdentifier, AuthID: "auth-1", AuthIndex: "index-1",
	}
	if !cancelMatches(session, base) {
		t.Fatal("matching cancellation ownership was rejected")
	}
	mutations := []func(*pluginapi.CancelExecutionRequest){
		func(req *pluginapi.CancelExecutionRequest) { req.ExecutionSessionID = "session-2" },
		func(req *pluginapi.CancelExecutionRequest) { req.CallerScope = "caller-2" },
		func(req *pluginapi.CancelExecutionRequest) { req.WorkspaceIdentity = "workspace-2" },
		func(req *pluginapi.CancelExecutionRequest) { req.Provider = "other" },
		func(req *pluginapi.CancelExecutionRequest) { req.AuthID = "auth-2" },
		func(req *pluginapi.CancelExecutionRequest) { req.AuthIndex = "index-2" },
	}
	for index, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if cancelMatches(session, candidate) {
			t.Fatalf("ownership mismatch %d was accepted", index)
		}
	}
}

func TestAuthScopeCloseRequiresEverySuppliedIdentity(t *testing.T) {
	session := &executionIdentity{authID: "auth-1", authIndex: "index-1"}
	if !closeMatches(session, pluginapi.CloseExecutionSessionRequest{
		Scope: pluginapi.ExecutionSessionCloseScopeAuth, AuthID: "auth-1", AuthIndex: "index-1",
	}) {
		t.Fatal("matching auth scope close was rejected")
	}
	if closeMatches(session, pluginapi.CloseExecutionSessionRequest{
		Scope: pluginapi.ExecutionSessionCloseScopeAuth, AuthID: "auth-1", AuthIndex: "index-2",
	}) {
		t.Fatal("partially matching auth scope close was accepted")
	}
}

func TestProjectionNonStreamAndHostFramedStreamChunks(t *testing.T) {
	now := time.Now().UTC()
	input, output, total := int64(3), int64(2), int64(5)
	events := []pluginapi.AgentEventV1{
		testAgentEvent(1, pluginapi.AgentEventSessionCreated, map[string]any{"native_session_id": "native-1"}, now),
		testAgentEvent(2, pluginapi.AgentEventTurnStarted, map[string]any{}, now),
		testAgentEvent(3, pluginapi.AgentEventMessageDelta, pluginapi.AgentTextDeltaV1{Text: "O"}, now),
		testAgentEvent(4, pluginapi.AgentEventMessageDelta, pluginapi.AgentTextDeltaV1{Text: "K"}, now),
		testAgentEvent(5, pluginapi.AgentEventUsageUpdated, pluginapi.AgentUsageV1{InputTokens: &input, OutputTokens: &output, TotalTokens: &total, Provenance: "provider_reported_unverified"}, now),
		testAgentEvent(6, pluginapi.AgentEventTurnCompleted, pluginapi.AgentTerminalPayloadV1{State: pluginapi.AgentTerminalCompleted}, now),
	}
	projection := newEventProjection("request-1", "qfmodel")
	for _, event := range events {
		if errConsume := projection.consume(event); errConsume != nil {
			t.Fatal(errConsume)
		}
	}
	response, errResponse := projection.nonStreamResponse()
	if errResponse != nil {
		t.Fatal(errResponse)
	}
	if !strings.Contains(string(response.Payload), `"content":"OK"`) || !strings.Contains(string(response.Payload), `"total_tokens":5`) {
		t.Fatalf("non-stream payload = %s", response.Payload)
	}
	stream := newEventProjection("request-1", "qfmodel")
	var chunks [][]byte
	terminalCount := 0
	for _, event := range events {
		chunk, terminal, errChunk := stream.streamChunk(event)
		if errChunk != nil {
			t.Fatal(errChunk)
		}
		if terminal {
			terminalCount++
		}
		if len(chunk) == 0 {
			continue
		}
		if !json.Valid(chunk) {
			t.Fatalf("plugin stream chunk is not JSON: %q", chunk)
		}
		if strings.Contains(string(chunk), "data:") || strings.Contains(string(chunk), "[DONE]") {
			t.Fatalf("plugin stream chunk contains host-owned SSE framing: %q", chunk)
		}
		chunks = append(chunks, chunk)
	}
	if terminalCount != 1 {
		t.Fatalf("terminal chunks = %d, want 1", terminalCount)
	}
	if len(chunks) != 3 {
		t.Fatalf("stream chunks = %d, want 3", len(chunks))
	}
	if !strings.Contains(string(chunks[0]), `"content":"O"`) ||
		!strings.Contains(string(chunks[2]), `"finish_reason":"stop"`) ||
		!strings.Contains(string(chunks[2]), `"total_tokens":5`) {
		t.Fatalf("stream chunks = %q", chunks)
	}
}

func TestProjectionDoesNotExposeRunnerExecutedToolsAsClientToolCalls(t *testing.T) {
	now := time.Now().UTC()
	events := []pluginapi.AgentEventV1{
		testAgentEvent(1, pluginapi.AgentEventTurnStarted, map[string]any{}, now),
		testAgentEvent(2, pluginapi.AgentEventToolStarted, map[string]any{"tool_call_id": "native-1", "name": "Read", "input": map[string]any{"file_path": "/tmp/probe"}}, now),
		testAgentEvent(3, pluginapi.AgentEventToolCompleted, map[string]any{"tool_call_id": "native-1"}, now),
		testAgentEvent(4, pluginapi.AgentEventMessageDelta, pluginapi.AgentTextDeltaV1{Text: "done"}, now),
		testAgentEvent(5, pluginapi.AgentEventTurnCompleted, pluginapi.AgentTerminalPayloadV1{State: pluginapi.AgentTerminalCompleted}, now),
	}
	projection := newEventProjection("request-1", "qfmodel")
	var chunks [][]byte
	for _, event := range events {
		chunk, _, errChunk := projection.streamChunk(event)
		if errChunk != nil {
			t.Fatal(errChunk)
		}
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		}
	}
	if len(chunks) != 2 || bytes.Contains(bytes.Join(chunks, nil), []byte("tool_calls")) {
		t.Fatalf("runner-executed tool leaked into client projection: %q", chunks)
	}
}

func TestProjectionPreservesDirectClientToolCalls(t *testing.T) {
	now := time.Now().UTC()
	events := []pluginapi.AgentEventV1{
		testAgentEvent(1, pluginapi.AgentEventToolStarted, map[string]any{"index": 0, "tool_call_id": "call-1", "name": "probe"}, now),
		testAgentEvent(2, pluginapi.AgentEventToolUpdated, map[string]any{"index": 0, "partial_json": `{"x":`}, now),
		testAgentEvent(3, pluginapi.AgentEventToolUpdated, map[string]any{"index": 0, "partial_json": `1}`}, now),
		testAgentEvent(4, pluginapi.AgentEventTurnCompleted, pluginapi.AgentTerminalPayloadV1{State: pluginapi.AgentTerminalCompleted}, now),
	}
	projection := newEventProjection("request-1", "qfmodel")
	var chunks [][]byte
	for _, event := range events {
		chunk, _, errChunk := projection.streamChunk(event)
		if errChunk != nil {
			t.Fatal(errChunk)
		}
		if len(chunk) > 0 {
			chunks = append(chunks, chunk)
		}
	}
	if len(chunks) != 4 {
		t.Fatalf("direct tool chunks = %d, want 4", len(chunks))
	}
	if !bytes.Contains(bytes.Join(chunks, nil), []byte(`"tool_calls"`)) || !bytes.Contains(bytes.Join(chunks, nil), []byte(`"arguments":"1}"`)) {
		t.Fatalf("direct tool call was not projected: %q", chunks)
	}
	response, errResponse := projection.nonStreamResponse()
	if errResponse != nil {
		t.Fatal(errResponse)
	}
	if !bytes.Contains(response.Payload, []byte(`"finish_reason":"tool_calls"`)) || !bytes.Contains(response.Payload, []byte(`"name":"probe"`)) {
		t.Fatalf("non-stream direct tool call = %s", response.Payload)
	}
}

func TestProjectionPreservesDirectTerminalErrorClassification(t *testing.T) {
	now := time.Now().UTC()
	projection := newEventProjection("request-1", "qfmodel")
	_, _, errChunk := projection.streamChunk(testAgentEvent(1, pluginapi.AgentEventTurnFailed, pluginapi.AgentTerminalPayloadV1{
		State: pluginapi.AgentTerminalFailed, Code: "quota_or_rate_limit", Message: "rate limited", Retryable: true,
	}, now))
	if errChunk == nil {
		t.Fatal("direct terminal error was accepted as a successful stream")
	}
	callErr, ok := errChunk.(*pluginCallError)
	if !ok || callErr.code != "quota_or_rate_limit" || callErr.statusCode != http.StatusTooManyRequests || !callErr.retryable {
		t.Fatalf("terminal error = %#v, want typed 429 retryable error", errChunk)
	}
}

func TestExecutorAlwaysRunsNativeDirect(t *testing.T) {
	runtime := newPluginRuntime(nil)
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		RequestID: "request-native", ExecutionSessionID: "session-native", CallerScope: "caller", WorkspaceIdentity: "workspace",
		AuthID: "auth-1", AuthIndex: "index-1", AuthProvider: "qoder", Model: "qfmodel", Format: "chat-completions",
		Payload:     []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"reply OK"}],"stream":false}`),
		StorageJSON: []byte(`{"type":"qoder","auth_mode":"pat","pat":"pt-test-secret"}`),
	}}
	raw, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	_, errExecute := runtime.execute(raw)
	callErr, ok := errExecute.(*pluginCallError)
	if !ok || callErr.code != "invalid_request" || !strings.Contains(callErr.message, "host callback") {
		t.Fatalf("execute without a host callback = %#v, want the native direct callback requirement", errExecute)
	}
}

func testAgentEvent(sequence uint64, eventType pluginapi.AgentEventType, payload any, timestamp time.Time) pluginapi.AgentEventV1 {
	raw, _ := json.Marshal(payload)
	return pluginapi.AgentEventV1{
		SchemaVersion: pluginapi.AgentEventSchemaVersionV1, Type: eventType, RequestID: "request-1",
		ExecutionSessionID: "session-1", TurnID: "turn-1", Provider: "qoder", AuthID: "auth-1", AuthIndex: "index-1",
		Sequence: sequence, Timestamp: timestamp, Payload: raw,
	}
}
