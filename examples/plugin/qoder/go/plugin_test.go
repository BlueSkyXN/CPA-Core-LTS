package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAuthModesAndSecretSafeErrors(t *testing.T) {
	secret := "pt-test-secret-never-log"
	patJSON := []byte(`{"type":"qoder","auth_mode":"pat","account_id":"acct","access_token":"` + secret + `"}`)
	pat, errPAT := parseStoredAuth(patJSON)
	if errPAT != nil || pat.AuthMode != "pat" || pat.AccessToken != secret {
		t.Fatalf("parse PAT = %#v, %v", pat, errPAT)
	}
	local, errLocal := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"local_cli","profile_id":"cn-main","config_dir":"/tmp/qoder-cn"}`))
	if errLocal != nil || local.ProfileID != "cn-main" || local.ConfigDir != "/tmp/qoder-cn" {
		t.Fatalf("parse local = %#v, %v", local, errLocal)
	}
	if _, errWhitespace := parseStoredAuth([]byte(`{"type":"qoder","auth_mode":"pat","access_token":" bad "}`)); errWhitespace == nil {
		t.Fatal("PAT with surrounding whitespace was accepted")
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

func TestRegistrationDeclaresSchema5LifecycleCapabilities(t *testing.T) {
	registration := pluginRegistration()
	if registration.SchemaVersion != 5 {
		t.Fatalf("schema version = %d", registration.SchemaVersion)
	}
	capabilities := registration.Capabilities
	if !capabilities.AuthProvider || !capabilities.ModelProvider || !capabilities.Executor ||
		!capabilities.ExecutionCanceller || !capabilities.ExecutionSessionCloser || !capabilities.ProviderReadiness {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if registration.Metadata.GitHubRepository != "https://github.com/BlueSkyXN/CPA-Core-LTS" {
		t.Fatalf("metadata = %#v", registration.Metadata)
	}
}

func TestSessionKeyIncludesAllOwnershipDimensions(t *testing.T) {
	auth := qoderAuth{Type: "qoder", AuthMode: "local_cli", ProfileID: "profile-1", ConfigDir: "/tmp/qoder-1"}
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
	env := runnerEnvironment(qoderAuth{AuthMode: "pat", AccessToken: "pt-test-secret"}, nil, runtimeRoot)
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

func TestFakeRunnerProtocolModelsCancelCloseAndSecretIsolation(t *testing.T) {
	cfg := fakeRunnerConfig(t)
	auth := qoderAuth{Type: "qoder", AuthMode: "pat", AccountID: "acct", AccessToken: "pt-test-secret"}
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
	auth := qoderAuth{Type: "qoder", AuthMode: "local_cli", ProfileID: "test"}
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
	authJSON := []byte(`{"type":"qoder","auth_mode":"local_cli","profile_id":"test","config_dir":"/tmp/qoder-test"}`)
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

func TestIdleRunnerExitIsReplacedBeforeNextTurn(t *testing.T) {
	runtime := newPluginRuntime(nil)
	runtime.config = fakeRunnerConfig(t)
	runtime.runnerExtraEnv = map[string]string{"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": "exit-after-success"}
	authJSON := []byte(`{"type":"qoder","auth_mode":"local_cli","profile_id":"test","config_dir":"/tmp/qoder-test"}`)
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
	}, qoderAuth{Type: "qoder", AuthMode: "local_cli", ProfileID: "test", ConfigDir: "/tmp/qoder-test"})
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
	authJSON := []byte(`{"type":"qoder","auth_mode":"pat","account_id":"acct","access_token":"pt-test-secret"}`)
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
		"GO_WANT_QODER_FAKE_RUNNER": "1", "QODER_FAKE_MODE": mode, "QODER_FAKE_SECRET": auth.AccessToken,
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
			result = map[string]any{"runner": "cpa-qoder-runner", "runner_version": "0.1.0-test", "protocol_version": version, "sdk_version": "1.0.10"}
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
