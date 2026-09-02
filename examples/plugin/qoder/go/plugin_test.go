package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if errDirect != nil || direct.Transport != "direct_openai" || direct.AuthMode != "pat" {
		t.Fatalf("parse direct = %#v, %v", direct, errDirect)
	}
	legacy, errLegacy := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","access_token":"pt-legacy","account_id":"legacy-account"}`))
	if errLegacy != nil || legacy.tokenSource() != "pt-legacy" || legacy.AccountID != "legacy-account" {
		t.Fatalf("legacy access_token = %#v, err=%v", legacy, errLegacy)
	}
	if legacy.runnerAuth()["mode"] != "pat" {
		t.Fatalf("legacy pt- access_token runner auth = %#v", legacy.runnerAuth())
	}
	legacyBearer, errLegacyBearer := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","access_token":"legacy-bearer","account_id":"legacy-account"}`))
	if errLegacyBearer != nil || legacyBearer.runnerAuth()["mode"] != "access_token" {
		t.Fatalf("legacy bearer access_token runner auth = %#v, err=%v", legacyBearer.runnerAuth(), errLegacyBearer)
	}
	local, errLocal := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"local_cli","access_token":"legacy-ignored","account_id":"legacy-account","profile_id":"cn-main","config_dir":"/tmp/qoder-cn","label":"Qoder CN"}`))
	if errLocal != nil || local.AuthMode != "local_cli" || local.ProfileID != "cn-main" || local.ConfigDir != "/tmp/qoder-cn" || local.AccessToken != "" || local.AccountID != "" {
		t.Fatalf("local_cli auth = %#v, err=%v", local, errLocal)
	}
	localRunnerAuth := local.runnerAuth()
	if localRunnerAuth["mode"] != "local_cli" || localRunnerAuth["profile_id"] != "cn-main" {
		t.Fatalf("local_cli runner auth = %#v", localRunnerAuth)
	}
	if _, errLocalDirect := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"local_cli","transport":"direct_openai","profile_id":"cn-main","config_dir":"/tmp/qoder-cn"}`)); errLocalDirect == nil {
		t.Fatal("local_cli direct transport was accepted")
	}
	if _, errWhitespace := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","pat":" pt-valid "}`)); errWhitespace == nil {
		t.Fatal("PAT with surrounding whitespace was accepted")
	}
	if _, errLegacyWhitespace := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","access_token":" pt-legacy "}`)); errLegacyWhitespace == nil {
		t.Fatal("legacy access_token with surrounding whitespace was accepted")
	}
	if _, errLocalPAT := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"local_cli","pat":"pt-invalid-mix","profile_id":"cn-main","config_dir":"/tmp/qoder-cn"}`)); errLocalPAT == nil {
		t.Fatal("local_cli with the new pat field was accepted")
	}
	safe := string(errorEnvelope(newPluginCallError("invalid_auth", "Qoder authentication failed", 401, false)))
	if strings.Contains(safe, secret) {
		t.Fatalf("error envelope leaked secret: %s", safe)
	}
	redacted := redactRunnerText("authorization=Bearer "+secret, secret)
	if strings.Contains(redacted, secret) {
		t.Fatalf("stderr redaction leaked secret: %s", redacted)
	}
}

func TestCanonicalModelsPreserveExactIDs(t *testing.T) {
	models := canonicalQoderModels()
	if len(models) != len(canonicalQoderModelIDs) {
		t.Fatalf("models len = %d", len(models))
	}
	found := false
	for _, model := range models {
		if model.ID == "qfmodel" && model.DisplayName == "Qwen3.8-Flash" {
			found = true
		}
	}
	if !found {
		t.Fatal("qfmodel / Qwen3.8-Flash canonical model missing")
	}
	if errAlias := validateCanonicalModel("qwen3.8-flash"); errAlias == nil {
		t.Fatal("normalized guessed alias was accepted")
	}
	if errDisplay := validateCanonicalModel("Qwen3.8-Flash"); errDisplay == nil {
		t.Fatal("display name was accepted as an executable model ID")
	}
	if errExact := validateCanonicalModel("qfmodel"); errExact != nil {
		t.Fatalf("exact canonical model rejected: %v", errExact)
	}
}

func TestChatInputPreservesSystemContextAndImageBlocks(t *testing.T) {
	input, errInput := inputFromChat([]byte(`{
		"messages":[
			{"role":"system","content":"Use marker SYSTEM-42."},
			{"role":"user","content":"Remember ALPHA."},
			{"role":"assistant","content":"Stored ALPHA."},
			{"role":"tool","name":"lookup","tool_call_id":"call-1","content":"TOOL-RESULT"},
			{"role":"user","content":[
				{"type":"text","text":"What color is this?"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AA==","detail":"high"}}
			]}
		]
	}`))
	if errInput != nil {
		t.Fatal(errInput)
	}
	if input.SystemPrompt != "Use marker SYSTEM-42." {
		t.Fatalf("system prompt = %q", input.SystemPrompt)
	}
	for _, want := range []string{"User: Remember ALPHA.", "Assistant: Stored ALPHA.", "Tool lookup: TOOL-RESULT", "User: What color is this?"} {
		if !strings.Contains(input.Prompt, want) {
			t.Fatalf("prompt %q does not contain %q", input.Prompt, want)
		}
	}
	var image *qoderImageSource
	for _, block := range input.Content {
		if block.Type == "image" {
			image = block.Source
		}
	}
	if image == nil || image.Type != "base64" || image.MediaType != "image/png" || image.Data != "AA==" {
		t.Fatalf("structured image = %#v", image)
	}
	if _, errInvalid := inputFromChat([]byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:text/plain;base64,AA=="}}]}]}`)); errInvalid == nil {
		t.Fatal("non-image data URL was accepted")
	}
}

func TestLiveModelMetadataPreservesVisionReasoningAndContext(t *testing.T) {
	model := qoderModelInfo(runnerModel{
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

func TestFixedSkillToolAndMCPConfigValidation(t *testing.T) {
	cfg, errConfig := decodePluginConfig([]byte(`
runner_command: /usr/local/bin/cpa-qoder-runner
qoder_cli_path: /usr/local/bin/qoderclicn
working_directory: /tmp
skills: [cpa-probe]
setting_sources: [project]
allowed_tools: [Read, mcp__cpa_probe__echo]
disallowed_tools: [Bash]
mcp_servers:
  cpa_probe:
    type: stdio
    command: /usr/bin/node
    args: [/opt/cpa/probe.mjs]
`))
	if errConfig != nil {
		t.Fatal(errConfig)
	}
	if len(cfg.Skills) != 1 || len(cfg.MCPServers) != 1 || cfg.MCPServers["cpa_probe"].Command != "/usr/bin/node" {
		t.Fatalf("fixed capability config = %#v", cfg)
	}
	_, errRemoteHTTP := decodePluginConfig([]byte(`
runner_command: /usr/local/bin/cpa-qoder-runner
qoder_cli_path: /usr/local/bin/qoderclicn
working_directory: /tmp
mcp_servers:
  unsafe:
    type: http
    url: http://example.com/mcp
`))
	if errRemoteHTTP == nil {
		t.Fatal("remote plain-HTTP MCP server was accepted")
	}
	_, errOverlap := decodePluginConfig([]byte(`
runner_command: /usr/local/bin/cpa-qoder-runner
qoder_cli_path: /usr/local/bin/qoderclicn
working_directory: /tmp
allowed_tools: [Bash]
disallowed_tools: [Bash]
`))
	if errOverlap == nil {
		t.Fatal("overlapping tool policy was accepted")
	}
	direct, errDirect := decodePluginConfig([]byte(`
transport: direct_openai
runner_command: /usr/local/bin/cpa-qoder-runner
working_directory: /tmp
direct_endpoint: https://api2-v2.example.test/model/v1/chat/completions
openapi_endpoint: https://openapi.example.test
direct_models:
  - id: qfmodel
    display_name: Qwen3.8-Flash
`))
	if errDirect != nil || direct.Transport != "direct_openai" || len(direct.DirectModels) != 1 || direct.DirectAuthEndpoint != "https://openapi.example.test" {
		t.Fatalf("direct config = %#v, %v", direct, errDirect)
	}
	if _, errNoModels := decodePluginConfig([]byte(`
transport: direct_openai
runner_command: /usr/local/bin/cpa-qoder-runner
working_directory: /tmp
direct_endpoint: https://api2-v2.example.test/model/v1/chat/completions
`)); errNoModels == nil {
		t.Fatal("direct config without model source was accepted")
	}
	if _, errNoOpenAPI := decodePluginConfig([]byte(`
transport: direct_openai
runner_command: /usr/local/bin/cpa-qoder-runner
working_directory: /tmp
direct_endpoint: https://api2-v2.example.test/model/v1/chat/completions
direct_models:
  - id: qfmodel
`)); errNoOpenAPI != nil {
		t.Fatalf("direct config without an auth endpoint was rejected: %v", errNoOpenAPI)
	}
	bearer, errBearer := decodePluginConfig([]byte(`
transport: direct_openai
runner_command: /usr/local/bin/cpa-qoder-runner
working_directory: /tmp
direct_endpoint: https://api2-v2.example.test/model/v1/chat/completions
direct_token_mode: bearer
direct_auth_endpoint: https://openapi.example.test
direct_models:
  - id: qfmodel
`))
	if errBearer != nil || bearer.DirectTokenMode != "bearer" || bearer.OpenAPIEndpoint != "https://openapi.example.test" || bearer.DirectAuthEndpoint != bearer.OpenAPIEndpoint {
		t.Fatalf("legacy bearer config = %#v, %v", bearer, errBearer)
	}
	if _, errMissingAuth := decodePluginConfig([]byte(`
transport: direct_openai
runner_command: /usr/local/bin/cpa-qoder-runner
working_directory: /tmp
direct_endpoint: https://api2-v2.example.test/model/v1/chat/completions
direct_token_mode: pat_exchange
direct_models:
  - id: qfmodel
`)); errMissingAuth == nil {
		t.Fatal("pat_exchange config without an auth endpoint was accepted")
	}
	if _, errOwnedArg := decodePluginConfig([]byte(`
runner_command: /usr/local/bin/cpa-qoder-runner
qoder_cli_path: /usr/local/bin/qoderclicn
working_directory: /tmp
runner_args: [--transport=direct_openai]
`)); errOwnedArg == nil {
		t.Fatal("runner_args overrode transport")
	}
}

func TestRunnerCapabilityValidationErrorsRemainClientErrors(t *testing.T) {
	for code, status := range map[string]int{
		"invalid_content":               http.StatusBadRequest,
		"invalid_configuration":         http.StatusBadRequest,
		"content_required":              http.StatusBadRequest,
		"session_configuration_changed": http.StatusConflict,
		"sdk_auth_config":               http.StatusServiceUnavailable,
		"sdk_auth_payload_incompatible": http.StatusServiceUnavailable,
	} {
		errMapped, ok := runnerCallError(&runnerError{Code: code, Message: "bounded test error"}).(*pluginCallError)
		if !ok || errMapped.statusCode != status {
			t.Fatalf("runner code %s mapped to %#v, want HTTP %d", code, errMapped, status)
		}
	}
}

func TestRegistrationDeclaresSchema5LifecycleCapabilities(t *testing.T) {
	registration := pluginRegistration()
	if registration.SchemaVersion != 5 {
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
	if executionSessionKey(base, auth, "direct_openai") == want {
		t.Fatal("transport mutation did not change session key")
	}
}

func TestCancelMatchesAllSuppliedOwnershipDimensions(t *testing.T) {
	session := &runnerSession{
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
	session := &runnerSession{authID: "auth-1", authIndex: "index-1"}
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

func TestRunnerEnvironmentIsolatesPATHomeAndTemporaryFiles(t *testing.T) {
	runtimeRoot := t.TempDir()
	env := runnerEnvironment(qoderAuth{AuthMode: "pat", PAT: "pt-test-secret"}, nil, runtimeRoot)
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if values[key] != runtimeRoot {
			t.Fatalf("%s = %q, want isolated root %q", key, values[key], runtimeRoot)
		}
	}
	if values["HOME"] != filepath.Join(runtimeRoot, "home") {
		t.Fatalf("HOME = %q, want isolated PAT home", values["HOME"])
	}
	if values[runnerPATEnv] != "pt-test-secret" {
		t.Fatal("runner PAT was not placed in the dedicated process environment")
	}
	localEnv := runnerEnvironment(qoderAuth{AuthMode: "local_cli", ProfileID: "cn-main", ConfigDir: "/tmp/qoder-cn"}, nil, runtimeRoot)
	localValues := make(map[string]string, len(localEnv))
	for _, item := range localEnv {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			localValues[key] = value
		}
	}
	if localValues["QODER_CONFIG_DIR"] != "/tmp/qoder-cn" || localValues[runnerPATEnv] != "" {
		t.Fatalf("local_cli environment = %#v", localValues)
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

func TestFakeRunnerProtocolModelsCancelCloseAndSecretIsolation(t *testing.T) {
	cfg := fakeRunnerConfig(t)
	auth := qoderAuth{Type: "qoder", AuthMode: "pat", PAT: "pt-test-secret"}
	client := startFakeRunner(t, cfg, auth, "cancel")
	defer client.shutdown()
	runtimeRoot := client.runtimeRoot

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var models runnerModelsResponse
	if errModels := client.call(ctx, "models", map[string]any{"auth": auth.runnerAuth()}, &models); errModels != nil {
		t.Fatal(errModels)
	}
	if len(models.Models) != 1 || models.Models[0].ID != "qfmodel" {
		t.Fatalf("models = %#v", models.Models)
	}
	start := fakeStartParams()
	if errStart := client.call(ctx, "start", start, nil); errStart != nil {
		t.Fatal(errStart)
	}
	if errCancel := client.call(ctx, "cancel", map[string]any{"request_id": "request-1", "execution_session_id": "session-1"}, nil); errCancel != nil {
		t.Fatal(errCancel)
	}
	var eventTypes []pluginapi.AgentEventType
	for len(eventTypes) < 3 {
		event, errEvent := client.readEvent(ctx)
		if errEvent != nil {
			t.Fatal(errEvent)
		}
		eventTypes = append(eventTypes, event.Type)
	}
	if got := fmt.Sprint(eventTypes); got != "[session.created turn.started turn.cancelled]" {
		t.Fatalf("event types = %s", got)
	}
	if errClose := client.call(ctx, "close", map[string]any{"execution_session_id": "session-1"}, nil); errClose != nil {
		t.Fatal(errClose)
	}
	client.shutdown()
	if _, errStat := os.Stat(runtimeRoot); !os.IsNotExist(errStat) {
		t.Fatalf("runner private runtime root still exists after shutdown: %v", errStat)
	}
}

func TestRunnerVersionMismatchAndBoundedEventQueue(t *testing.T) {
	cfg := fakeRunnerConfig(t)
	auth := qoderAuth{Type: "qoder", AuthMode: "pat", PAT: "pt-test-secret"}
	client, errStart := newRunnerClient(cfg, auth, map[string]string{
		"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": "version-mismatch",
	})
	if errStart != nil {
		t.Fatal(errStart)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, errHandshake := client.handshake(ctx); errHandshake == nil {
		client.shutdown()
		t.Fatal("version mismatch handshake succeeded")
	}
	client.shutdown()

	cfg.MaxQueueFrames = 2
	flood := startFakeRunner(t, cfg, auth, "flood")
	if errCall := flood.call(ctx, "start", fakeStartParams(), nil); errCall != nil {
		flood.shutdown()
		t.Fatal(errCall)
	}
	select {
	case <-flood.done:
	case <-time.After(5 * time.Second):
		flood.shutdown()
		t.Fatal("runner queue overflow did not terminate client")
	}
	flood.shutdown()
}

func TestDirectTransportNegotiatesWithRunner(t *testing.T) {
	cfg := fakeRunnerConfig(t)
	cfg.DirectEndpoint = "https://api2-v2.example.test/model/v1/chat/completions"
	cfg.DirectModels = []directModelConfig{{ID: "qfmodel", DisplayName: "Qwen3.8-Flash"}}
	auth := qoderAuth{Type: "qoder", AuthMode: "pat", Transport: "direct_openai", PAT: "pt-fixture"}
	client, errStart := newRunnerClient(cfg, auth, map[string]string{
		"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": "success", "QODER_FAKE_TRANSPORT": "direct_openai",
	}, "direct_openai")
	if errStart != nil {
		t.Fatal(errStart)
	}
	defer client.shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, errHandshake := client.handshake(ctx, "direct_openai"); errHandshake != nil {
		t.Fatal(errHandshake)
	}
	start := fakeStartParams()
	start["auth"] = auth.runnerAuth("direct_openai")
	start["transport"] = "direct_openai"
	start["chat_request"] = map[string]any{"model": "qfmodel", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
	if errCall := client.call(ctx, "start", start, nil); errCall != nil {
		t.Fatal(errCall)
	}
}

func TestRunnerExitTakesPriorityOverBufferedEvents(t *testing.T) {
	client := &runnerClient{
		events: make(chan pluginapi.AgentEventV1, 1),
		done:   make(chan struct{}),
	}
	client.events <- testAgentEvent(1, pluginapi.AgentEventTurnStarted, map[string]any{}, time.Now().UTC())
	close(client.done)
	if _, errEvent := client.readEvent(context.Background()); errEvent == nil {
		t.Fatal("buffered event was returned after runner exit")
	}
}

func TestRunnerCrashIsRestartableWithFreshSession(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.config = fakeRunnerConfig(t)
	runtime.runnerExtraEnv = map[string]string{"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": "crash"}
	authJSON := []byte(`{"type":"qoder","auth_mode":"pat","pat":"pt-test-secret"}`)
	req := pluginapi.ExecutorRequest{
		RequestID: "request-restart", ExecutionSessionID: "session-restart", CallerScope: "caller", WorkspaceIdentity: "workspace",
		AuthID: "auth-1", AuthIndex: "index-1", AuthProvider: "qoder", Model: "qfmodel", Format: "chat-completions",
		Payload: []byte(`{"messages":[{"role":"user","content":"reply OK"}]}`), StorageJSON: authJSON,
	}
	if _, errCrash := runtime.startTurn(req); errCrash == nil {
		t.Fatal("crashing runner start succeeded")
	}
	runtime.mu.Lock()
	runtime.runnerExtraEnv["QODER_FAKE_MODE"] = "success"
	runtime.mu.Unlock()
	session, errRestart := runtime.startTurn(req)
	if errRestart != nil {
		t.Fatalf("fresh runner restart failed: %v", errRestart)
	}
	runtime.completeTurn(session, req.RequestID)
	runtime.dropSession(session)
}

func TestNonStreamRunnerCrashReturnsConnectionLifecycleAndRecovers(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.config = fakeRunnerConfig(t)
	runtime.runnerExtraEnv = map[string]string{"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": "crash"}
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		RequestID: "request-nonstream-crash", ExecutionSessionID: "session-nonstream-crash", CallerScope: "caller", WorkspaceIdentity: "workspace",
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
	if !ok || callErr.code != "connection_lifecycle" || callErr.statusCode != 0 || !callErr.retryable {
		t.Fatalf("non-stream runner crash error = %#v, want retryable connection_lifecycle without HTTP status", errExecute)
	}

	runtime.mu.Lock()
	runtime.runnerExtraEnv["QODER_FAKE_MODE"] = "success"
	runtime.mu.Unlock()
	req.RequestID = "request-nonstream-recovery"
	raw, errMarshal = json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response, errRecovery := runtime.execute(raw)
	if errRecovery != nil || !strings.Contains(string(response.Payload), `"content":"OK"`) {
		t.Fatalf("immediate non-stream recovery response=%s error=%v", response.Payload, errRecovery)
	}
	runtime.shutdown()
}

func TestIdleRunnerExitIsReplacedBeforeNextTurn(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.config = fakeRunnerConfig(t)
	runtime.runnerExtraEnv = map[string]string{"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": "exit-after-success"}
	authJSON := []byte(`{"type":"qoder","auth_mode":"pat","pat":"pt-test-secret"}`)
	request := func(requestID string) []byte {
		req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
			RequestID: requestID, ExecutionSessionID: "session-idle-restart", CallerScope: "caller", WorkspaceIdentity: "workspace",
			AuthID: "auth-1", AuthIndex: "index-1", AuthProvider: "qoder", Model: "qfmodel", Format: "chat-completions",
			Payload: []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"reply OK"}],"stream":false}`), StorageJSON: authJSON,
		}}
		raw, _ := json.Marshal(req)
		return raw
	}
	if _, errFirst := runtime.execute(request("request-idle-1")); errFirst != nil {
		t.Fatalf("first turn failed: %v", errFirst)
	}
	key := executionSessionKey(pluginapi.ExecutorRequest{
		ExecutionSessionID: "session-idle-restart", CallerScope: "caller", WorkspaceIdentity: "workspace",
		AuthID: "auth-1", AuthIndex: "index-1",
	}, qoderAuth{Type: "qoder", AuthMode: "pat", PAT: "pt-test-secret"})
	runtime.mu.Lock()
	firstSession := runtime.sessions[key]
	runtime.mu.Unlock()
	if firstSession == nil {
		t.Fatal("persistent session missing after first turn")
	}
	select {
	case <-firstSession.client.done:
	case <-time.After(5 * time.Second):
		t.Fatal("fake runner did not exit while idle")
	}
	runtime.mu.Lock()
	runtime.runnerExtraEnv["QODER_FAKE_MODE"] = "success"
	runtime.mu.Unlock()
	if _, errSecond := runtime.execute(request("request-idle-2")); errSecond != nil {
		t.Fatalf("next turn did not replace stale runner: %v", errSecond)
	}
	runtime.mu.Lock()
	secondSession := runtime.sessions[key]
	runtime.mu.Unlock()
	if secondSession == nil || secondSession == firstSession {
		t.Fatal("stale runner session was reused")
	}
	runtime.dropSession(secondSession)
}

func TestExecutorUsesRunnerAndProjectsChat(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.config = fakeRunnerConfig(t)
	runtime.runnerExtraEnv = map[string]string{"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": "success"}
	authJSON := []byte(`{"type":"qoder","auth_mode":"pat","pat":"pt-test-secret"}`)
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		RequestID: "request-1", ExecutionSessionID: "session-1", CallerScope: "caller", WorkspaceIdentity: "workspace",
		AuthID: "auth-1", AuthIndex: "index-1", AuthProvider: "qoder", Model: "qfmodel", Format: "chat-completions",
		Payload:     []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"reply OK"}],"stream":false}`),
		StorageJSON: authJSON,
	}}
	raw, _ := json.Marshal(req)
	response, errExecute := runtime.execute(raw)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	if !strings.Contains(string(response.Payload), `"content":"OK"`) {
		t.Fatalf("response = %s", response.Payload)
	}
	if errClose := runtime.closeExecutionSessions(pluginapi.CloseExecutionSessionRequest{
		Scope: pluginapi.ExecutionSessionCloseScopeSession, ExecutionSessionID: "session-1", CallerScope: "caller", WorkspaceIdentity: "workspace",
	}); errClose != nil {
		t.Fatal(errClose)
	}
}

func TestExecutorRetainsLegacyLocalCLIAuthPath(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.config = fakeRunnerConfig(t)
	runtime.runnerExtraEnv = map[string]string{"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": "success"}
	req := rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		RequestID: "request-local", ExecutionSessionID: "session-local", CallerScope: "caller", WorkspaceIdentity: "workspace",
		AuthID: "auth-local", AuthIndex: "index-local", AuthProvider: "qoder", Model: "qfmodel", Format: "chat-completions",
		Payload:     []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"reply OK"}],"stream":false}`),
		StorageJSON: []byte(`{"type":"qoder","auth_mode":"local_cli","profile_id":"cn-main","config_dir":"/tmp/qoder-cn"}`),
	}}
	raw, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response, errExecute := runtime.execute(raw)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	if !strings.Contains(string(response.Payload), `"content":"OK"`) {
		t.Fatalf("local_cli response = %s", response.Payload)
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

func fakeRunnerConfig(t *testing.T) pluginConfig {
	t.Helper()
	return pluginConfig{
		RunnerCommand: os.Args[0], RunnerArgs: []string{"-test.run=TestQoderFakeRunnerProcess", "--"},
		QoderCLIPath: "/fake/qoderclicn", WorkingDirectory: t.TempDir(), MaxQueueFrames: 32,
		RequestTimeout: 5 * time.Second, ModelCacheTTL: time.Minute, PermissionDefault: "deny",
	}
}

func startFakeRunner(t *testing.T, cfg pluginConfig, auth qoderAuth, mode string) *runnerClient {
	t.Helper()
	client, errStart := newRunnerClient(cfg, auth, map[string]string{
		"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": mode, "QODER_FAKE_SECRET": auth.tokenSource(),
	})
	if errStart != nil {
		t.Fatal(errStart)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, errHandshake := client.handshake(ctx); errHandshake != nil {
		client.shutdown()
		t.Fatal(errHandshake)
	}
	return client
}

func fakeStartParams() map[string]any {
	return map[string]any{
		"request_id": "request-1", "execution_session_id": "session-1", "turn_id": "turn-1", "provider": "qoder",
		"auth_id": "auth-1", "auth_index": "index-1", "prompt": "reply OK", "model": "qfmodel",
		"auth":              map[string]any{"mode": "pat", "env_var": runnerPATEnv},
		"permission_policy": map[string]any{"default": "deny", "rules": []any{}},
	}
}

func TestQoderFakeRunnerProcess(t *testing.T) {
	if os.Getenv("GO_WANT_QODER_FAKE_RUNNER") != "1" {
		return
	}
	mode := os.Getenv("QODER_FAKE_MODE")
	secret := os.Getenv("QODER_FAKE_SECRET")
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if secret != "" && strings.Contains(string(line), secret) {
			os.Exit(91)
		}
		var request runnerRequest
		if json.Unmarshal(line, &request) != nil {
			os.Exit(92)
		}
		if mode == "crash" && request.Method == "start" {
			os.Exit(93)
		}
		result := any(map[string]any{})
		switch request.Method {
		case "handshake":
			version := runnerProtocol
			if mode == "version-mismatch" {
				version = 2
			}
			transport := os.Getenv("QODER_FAKE_TRANSPORT")
			result = map[string]any{"runner": "cpa-qoder-runner", "runner_version": "0.1.0-test", "protocol_version": version, "sdk_version": "1.0.10", "transport": transport}
		case "models":
			result = map[string]any{"models": []any{map[string]any{"id": "qfmodel", "display_name": "Qwen3.8-Flash", "is_enabled": true}}}
		case "readiness":
			result = map[string]any{"ready": true, "checks": []any{map[string]any{"Level": "auth_ready", "State": "ready"}}}
		case "start":
			result = map[string]any{"accepted": true}
		case "cancel":
			result = map[string]any{"cancelled": true}
		case "close":
			result = map[string]any{"closed": true}
		case "shutdown":
			result = map[string]any{"shutdown": true}
		}
		rawResult, _ := json.Marshal(result)
		_ = encoder.Encode(runnerResponse{ProtocolVersion: runnerProtocol, Type: "response", ID: request.ID, OK: true, Result: rawResult})
		if request.Method == "start" {
			var params map[string]any
			rawParams, _ := json.Marshal(request.Params)
			_ = json.Unmarshal(rawParams, &params)
			emitFakeEvent(encoder, params, 1, pluginapi.AgentEventSessionCreated, map[string]any{"native_session_id": "native-1"})
			emitFakeEvent(encoder, params, 2, pluginapi.AgentEventTurnStarted, map[string]any{})
			if mode == "success" || mode == "exit-after-success" {
				emitFakeEvent(encoder, params, 3, pluginapi.AgentEventMessageDelta, pluginapi.AgentTextDeltaV1{Text: "OK"})
				emitFakeEvent(encoder, params, 4, pluginapi.AgentEventTurnCompleted, pluginapi.AgentTerminalPayloadV1{State: pluginapi.AgentTerminalCompleted})
				if mode == "exit-after-success" {
					return
				}
			}
			if mode == "flood" {
				for sequence := uint64(3); sequence < 100; sequence++ {
					emitFakeEvent(encoder, params, sequence, pluginapi.AgentEventWarning, map[string]any{"code": "test"})
				}
			}
		}
		if request.Method == "cancel" && mode == "cancel" {
			params := fakeStartParams()
			emitFakeEvent(encoder, params, 3, pluginapi.AgentEventTurnCancelled, pluginapi.AgentTerminalPayloadV1{State: pluginapi.AgentTerminalCancelled})
		}
		if request.Method == "shutdown" {
			return
		}
	}
}

func emitFakeEvent(encoder *json.Encoder, params map[string]any, sequence uint64, eventType pluginapi.AgentEventType, payload any) {
	rawPayload, _ := json.Marshal(payload)
	event := pluginapi.AgentEventV1{
		SchemaVersion: 1, Type: eventType, RequestID: fmt.Sprint(params["request_id"]),
		ExecutionSessionID: fmt.Sprint(params["execution_session_id"]), TurnID: fmt.Sprint(params["turn_id"]), Provider: "qoder",
		AuthID: fmt.Sprint(params["auth_id"]), AuthIndex: fmt.Sprint(params["auth_index"]),
		Sequence: sequence, Timestamp: time.Now().UTC(), Payload: rawPayload,
	}
	_ = encoder.Encode(map[string]any{"protocol_version": runnerProtocol, "type": "event", "request_id": event.RequestID, "event": event})
}
