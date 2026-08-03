package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestBuildCodexDesktopToolOverlayInjectsStableSchemas(t *testing.T) {
	body := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
	configured := []string{"read_thread", "automation_update", "create_thread", "handoff_thread", "wait_threads"}
	result := buildCodexDesktopToolOverlay(
		"kimi",
		sdktranslator.FormatOpenAI,
		"kimi-k3",
		"kimi-k3",
		sdktranslator.FormatOpenAIResponse,
		http.Header{"User-Agent": {"Codex Desktop/26.727.51351"}},
		body,
		configured,
	)
	if result.injectedCount != len(configured) || result.skipReason != "applied" {
		t.Fatalf("result = %+v", result)
	}
	children := desktopOverlayCodexAppChildren(t, result.body)
	wantOrder := []string{"automation_update", "handoff_thread", "create_thread", "read_thread", "wait_threads"}
	if got := desktopOverlayChildNames(children); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("child order = %v, want %v", got, wantOrder)
	}
	for _, child := range children {
		if child["type"] != "function" {
			t.Fatalf("child type = %v, want function", child["type"])
		}
		strict, exists := child["strict"]
		if !exists || strict != false {
			t.Fatalf("child strict = %#v, want explicit false", strict)
		}
		if _, exists := child["deferLoading"]; exists {
			t.Fatal("child contains deferLoading")
		}
		if _, exists := child["defer_loading"]; exists {
			t.Fatal("child contains defer_loading")
		}
		if _, ok := child["parameters"].(map[string]any); !ok {
			t.Fatalf("%s parameters = %T, want object", child["name"], child["parameters"])
		}
	}

	readThread := desktopOverlayChildByName(t, children, "read_thread")
	readProperties := readThread["parameters"].(map[string]any)["properties"].(map[string]any)
	if got := readProperties["turnLimit"].(map[string]any)["minimum"]; got != float64(1) {
		t.Fatalf("read_thread.turnLimit.minimum = %v", got)
	}
	if got := readProperties["turnLimit"].(map[string]any)["maximum"]; got != float64(10) {
		t.Fatalf("read_thread.turnLimit.maximum = %v", got)
	}
	if got := readProperties["maxOutputCharsPerItem"].(map[string]any)["maximum"]; got != float64(20000) {
		t.Fatalf("read_thread.maxOutputCharsPerItem.maximum = %v", got)
	}
	if got := readThread["parameters"].(map[string]any)["required"]; !reflect.DeepEqual(got, []any{"threadId"}) {
		t.Fatalf("read_thread.required = %#v", got)
	}

	waitThreads := desktopOverlayChildByName(t, children, "wait_threads")
	waitProperties := waitThreads["parameters"].(map[string]any)["properties"].(map[string]any)
	targets := waitProperties["targets"].(map[string]any)
	if targets["minItems"] != float64(1) || targets["maxItems"] != float64(8) {
		t.Fatalf("wait_threads.targets bounds = %#v", targets)
	}
	if waitProperties["timeoutMs"].(map[string]any)["maximum"] != float64(120000) {
		t.Fatalf("wait_threads.timeoutMs = %#v", waitProperties["timeoutMs"])
	}

	automation := desktopOverlayChildByName(t, children, "automation_update")
	if _, ok := automation["parameters"].(map[string]any)["oneOf"].([]any); !ok {
		t.Fatal("automation_update oneOf schema missing")
	}
	createThread := desktopOverlayChildByName(t, children, "create_thread")
	if _, ok := createThread["parameters"].(map[string]any)["properties"].(map[string]any)["target"].(map[string]any)["anyOf"].([]any); !ok {
		t.Fatal("create_thread target union missing")
	}
	handoff := desktopOverlayChildByName(t, children, "handoff_thread")
	if got := handoff["parameters"].(map[string]any)["required"]; !reflect.DeepEqual(got, []any{"threadId"}) {
		t.Fatalf("handoff_thread.required = %#v", got)
	}
}

func TestBuildCodexDesktopToolOverlayEligibility(t *testing.T) {
	validBody := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
	flatSubagentBody := desktopOverlayRootBody("user", map[string]any{"x-openai-subagent": "worker"}, desktopOverlayTopLevelNamespace())
	toolSearchBody := desktopOverlayRootBody("user", nil, []any{
		map[string]any{"type": "tool_search"},
		desktopOverlayNamespace(),
	})
	customExecBody := desktopOverlayRootBody("user", nil, []any{
		map[string]any{"type": "custom", "name": "exec"},
		desktopOverlayNamespace(),
	})
	noNamespaceBody := desktopOverlayRootBody("user", nil, []any{map[string]any{"type": "function", "name": "lookup"}})

	tests := []struct {
		name           string
		provider       string
		to             sdktranslator.Format
		selectedModel  string
		requestedModel string
		source         sdktranslator.Format
		headers        http.Header
		body           []byte
		wantCount      int
		wantReason     string
	}{
		{name: "Kimi Chat", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: validBody, wantCount: 1, wantReason: "applied"},
		{name: "xAI Codex wire", provider: "XAI", to: sdktranslator.FormatCodex, selectedModel: "grok-4.5", requestedModel: "grok-4.5", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: validBody, wantCount: 1, wantReason: "applied"},
		{name: "non xAI Codex wire", provider: "codex", to: sdktranslator.FormatCodex, selectedModel: "third-party", requestedModel: "third-party", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: validBody, wantReason: "unsupported_target"},
		{name: "unsupported source", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAI, headers: desktopOverlayHeaders(), body: validBody, wantReason: "unsupported_source"},
		{name: "unsupported target", provider: "claude", to: sdktranslator.FormatClaude, selectedModel: "claude-sonnet", requestedModel: "claude-sonnet", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: validBody, wantReason: "unsupported_target"},
		{name: "UA case insensitive", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: http.Header{"User-Agent": {"client CODEX DESKTOP build"}}, body: validBody, wantCount: 1, wantReason: "applied"},
		{name: "UA missing", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: http.Header{"User-Agent": {"codex-tui"}}, body: validBody, wantReason: "desktop_user_agent_missing"},
		{name: "requested GPT", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "Alias-GpT-Review", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: validBody, wantReason: "gpt_model"},
		{name: "selected GPT", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "Gpt-5.6", requestedModel: "codex-auto-review", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: validBody, wantReason: "gpt_model"},
		{name: "negative alias accepted", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "grok-4.5", requestedModel: "codex-auto-review", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: validBody, wantCount: 1, wantReason: "applied"},
		{name: "selected model missing", provider: "kimi", to: sdktranslator.FormatOpenAI, requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: validBody, wantReason: "selected_model_missing"},
		{name: "canonical subagent without flat marker", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: desktopOverlayRootBody("subagent", nil, desktopOverlayTopLevelNamespace()), wantReason: "not_root_user_turn"},
		{name: "flat subagent marker", provider: "xai", to: sdktranslator.FormatCodex, selectedModel: "grok-4.5", requestedModel: "grok-4.5", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: flatSubagentBody, wantReason: "not_root_user_turn"},
		{name: "metadata missing", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: desktopOverlayBodyWithoutMetadata(desktopOverlayTopLevelNamespace()), wantReason: "not_root_user_turn"},
		{name: "metadata malformed", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: []byte(`{"client_metadata":{"x-codex-turn-metadata":"{"}}`), wantReason: "root_metadata_invalid"},
		{name: "body malformed", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: []byte(`{`), wantReason: "root_metadata_invalid"},
		{name: "tool search present", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: toolSearchBody, wantReason: "forbidden_tool_surface"},
		{name: "custom exec present", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: customExecBody, wantReason: "forbidden_tool_surface"},
		{name: "namespace missing", provider: "kimi", to: sdktranslator.FormatOpenAI, selectedModel: "kimi-k3", requestedModel: "kimi-k3", source: sdktranslator.FormatOpenAIResponse, headers: desktopOverlayHeaders(), body: noNamespaceBody, wantReason: "codex_app_namespace_missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := buildCodexDesktopToolOverlay(test.provider, test.to, test.selectedModel, test.requestedModel, test.source, test.headers, test.body, []string{"read_thread"})
			if result.injectedCount != test.wantCount || result.skipReason != test.wantReason {
				t.Fatalf("result count=%d reason=%q, want count=%d reason=%q", result.injectedCount, result.skipReason, test.wantCount, test.wantReason)
			}
			if test.wantCount == 0 && !bytes.Equal(result.body, test.body) {
				t.Fatal("no-op changed request bytes")
			}
		})
	}
}

func TestBuildCodexDesktopToolOverlayHandlesPartialDuplicateAndAdditionalTools(t *testing.T) {
	topNamespace := desktopOverlayNamespace(map[string]any{"type": "function", "name": "list_threads"})
	additionalNamespace := desktopOverlayNamespace(map[string]any{"type": "custom", "name": "read_thread"})
	body := desktopOverlayRootBodyWithAdditional("user", []any{topNamespace}, []any{additionalNamespace})

	result := buildCodexDesktopToolOverlay("kimi", sdktranslator.FormatOpenAI, "kimi-k3", "kimi-k3", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, []string{"read_thread", "wait_threads"})
	if result.injectedCount != 1 || result.skipReason != "applied" {
		t.Fatalf("result = %+v", result)
	}
	allNamespaces := desktopOverlayAllCodexAppChildren(t, result.body)
	if len(allNamespaces) != 2 {
		t.Fatalf("namespace count = %d, want 2", len(allNamespaces))
	}
	if got := desktopOverlayChildNames(allNamespaces[0]); !reflect.DeepEqual(got, []string{"list_threads", "wait_threads"}) {
		t.Fatalf("top-level children = %v", got)
	}
	if got := desktopOverlayChildNames(allNamespaces[1]); !reflect.DeepEqual(got, []string{"read_thread"}) {
		t.Fatalf("additional children = %v", got)
	}

	second := buildCodexDesktopToolOverlay("kimi", sdktranslator.FormatOpenAI, "kimi-k3", "kimi-k3", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), result.body, []string{"read_thread", "wait_threads"})
	if second.injectedCount != 0 || second.skipReason != "no_new_tools" {
		t.Fatalf("second result = %+v", second)
	}
	if !bytes.Equal(second.body, result.body) {
		t.Fatal("idempotent execution changed bytes")
	}
}

func TestBuildCodexDesktopToolOverlayInjectsAdditionalToolsOnly(t *testing.T) {
	body := desktopOverlayRootBodyWithAdditional("user", nil, []any{desktopOverlayNamespace()})
	result := buildCodexDesktopToolOverlay("kimi", sdktranslator.FormatOpenAI, "kimi-k3", "kimi-k3", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, []string{"read_thread"})
	if result.injectedCount != 1 || result.skipReason != "applied" {
		t.Fatalf("result = %+v", result)
	}
	all := desktopOverlayAllCodexAppChildren(t, result.body)
	if len(all) != 1 || !reflect.DeepEqual(desktopOverlayChildNames(all[0]), []string{"read_thread"}) {
		t.Fatalf("additional_tools children = %#v", all)
	}
}

func TestManagerApplyRequestAfterAuthInterceptorOverlayPluginOrder(t *testing.T) {
	manager := desktopOverlayManager(false)
	body := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
	req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		Headers:         desktopOverlayHeaders(),
		OriginalRequest: []byte(`{"stale":true}`),
	}

	gotReq, gotOpts, err := manager.applyRequestAfterAuthInterceptor(context.Background(), nil, "kimi", req, opts, "kimi-k3")
	if err != nil {
		t.Fatalf("nil plugin error = %v", err)
	}
	if !desktopOverlayHasChild(gotReq.Payload, "read_thread") {
		t.Fatal("nil plugin blocked built-in overlay")
	}
	if !bytes.Equal(gotReq.Payload, gotOpts.OriginalRequest) {
		t.Fatal("overlay did not synchronize Payload and OriginalRequest")
	}

	pluginSawOverlay := false
	opts.RequestAfterAuthInterceptor = func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		pluginSawOverlay = desktopOverlayHasChild(request.Body, "read_thread")
		if request.RequestedModel != "kimi-k3" || request.Model != "kimi-k3" {
			t.Fatalf("plugin models = selected %q requested %q", request.Model, request.RequestedModel)
		}
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{Body: []byte(`{"plugin":true}`)}
	}
	gotReq, gotOpts, err = manager.applyRequestAfterAuthInterceptor(context.Background(), nil, "kimi", req, opts, "kimi-k3")
	if err != nil {
		t.Fatalf("plugin override error = %v", err)
	}
	if !pluginSawOverlay {
		t.Fatal("plugin did not observe built-in overlay first")
	}
	if string(gotReq.Payload) != `{"plugin":true}` || string(gotOpts.OriginalRequest) != `{"plugin":true}` {
		t.Fatalf("plugin override not final: payload=%s original=%s", gotReq.Payload, gotOpts.OriginalRequest)
	}

	opts.RequestAfterAuthInterceptor = func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		if !desktopOverlayHasChild(request.Body, "read_thread") {
			t.Fatal("terminating plugin did not observe overlay")
		}
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{
			Terminate:    true,
			StatusCode:   http.StatusTeapot,
			ResponseBody: []byte("stopped"),
		}
	}
	_, _, err = manager.applyRequestAfterAuthInterceptor(context.Background(), nil, "kimi", req, opts, "kimi-k3")
	terminated, ok := err.(*cliproxyexecutor.RequestTerminatedError)
	if !ok || terminated.HTTPStatus != http.StatusTeapot || string(terminated.Body) != "stopped" {
		t.Fatalf("terminate error = %#v", err)
	}
}

func TestManagerDesktopToolOverlayHotReload(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	body := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
	req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: desktopOverlayHeaders()}

	manager.SetConfig(&internalconfig.Config{})
	disabledReq, _, err := manager.applyRequestAfterAuthInterceptor(context.Background(), nil, "kimi", req, opts, "kimi-k3")
	if err != nil || !bytes.Equal(disabledReq.Payload, body) {
		t.Fatalf("disabled overlay changed body or errored: %v", err)
	}
	manager.SetConfig(desktopOverlayConfig(false))
	enabledReq, _, err := manager.applyRequestAfterAuthInterceptor(context.Background(), nil, "kimi", req, opts, "kimi-k3")
	if err != nil || !desktopOverlayHasChild(enabledReq.Payload, "read_thread") {
		t.Fatalf("hot-enabled overlay not applied: %v", err)
	}
	manager.SetConfig(&internalconfig.Config{})
	disabledAgainReq, _, err := manager.applyRequestAfterAuthInterceptor(context.Background(), nil, "kimi", req, opts, "kimi-k3")
	if err != nil || !bytes.Equal(disabledAgainReq.Payload, body) {
		t.Fatalf("hot-disabled overlay changed body or errored: %v", err)
	}
}

func TestManagerDesktopToolOverlayExecutionPaths(t *testing.T) {
	body := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: desktopOverlayHeaders(), OriginalRequest: body}

	manager, executor := newDesktopOverlayExecutionManager(t, false)
	if _, err := manager.Execute(context.Background(), []string{"kimi"}, cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := manager.ExecuteCount(context.Background(), []string{"kimi"}, cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}, opts); err != nil {
		t.Fatalf("ExecuteCount() error = %v", err)
	}
	stream, err := manager.ExecuteStream(context.Background(), []string{"kimi"}, cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range stream.Chunks {
	}
	if got := executor.snapshotKinds(); !reflect.DeepEqual(got, []string{"execute", "count", "stream"}) {
		t.Fatalf("execution kinds = %v", got)
	}
	for index, captured := range executor.snapshotRequests() {
		if !desktopOverlayHasChild(captured.req.Payload, "read_thread") {
			t.Fatalf("request %d missing overlay", index)
		}
		if !bytes.Equal(captured.req.Payload, captured.opts.OriginalRequest) {
			t.Fatalf("request %d Payload/OriginalRequest diverged", index)
		}
	}

	homeManager, homeExecutor := newDesktopOverlayExecutionManager(t, true)
	if _, err := homeManager.Execute(context.Background(), []string{"kimi"}, cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}, opts); err != nil {
		t.Fatalf("Home Execute() error = %v", err)
	}
	homeRequests := homeExecutor.snapshotRequests()
	if len(homeRequests) != 1 || !desktopOverlayHasChild(homeRequests[0].req.Payload, "read_thread") {
		t.Fatalf("Home request missing overlay: %#v", homeRequests)
	}
}

type desktopOverlayCapturedRequest struct {
	req  cliproxyexecutor.Request
	opts cliproxyexecutor.Options
}

type desktopOverlayCaptureExecutor struct {
	mu       sync.Mutex
	kinds    []string
	requests []desktopOverlayCapturedRequest
}

func (*desktopOverlayCaptureExecutor) Identifier() string { return "kimi" }

func (e *desktopOverlayCaptureExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record("execute", req, opts)
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *desktopOverlayCaptureExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record("stream", req, opts)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"done":true}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*desktopOverlayCaptureExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *desktopOverlayCaptureExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record("count", req, opts)
	return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}

func (*desktopOverlayCaptureExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *desktopOverlayCaptureExecutor) record(kind string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	req.Payload = bytes.Clone(req.Payload)
	opts.OriginalRequest = bytes.Clone(opts.OriginalRequest)
	e.kinds = append(e.kinds, kind)
	e.requests = append(e.requests, desktopOverlayCapturedRequest{req: req, opts: opts})
}

func (e *desktopOverlayCaptureExecutor) snapshotKinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.kinds...)
}

func (e *desktopOverlayCaptureExecutor) snapshotRequests() []desktopOverlayCapturedRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]desktopOverlayCapturedRequest(nil), e.requests...)
}

type desktopOverlayHomeDispatcher struct{}

func (desktopOverlayHomeDispatcher) HeartbeatOK() bool       { return true }
func (desktopOverlayHomeDispatcher) AbortAmbiguousDispatch() {}
func (desktopOverlayHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{ID: "desktop-overlay-home", Provider: "kimi", Status: StatusActive}})
}

func newDesktopOverlayExecutionManager(t *testing.T, homeEnabled bool) (*Manager, *desktopOverlayCaptureExecutor) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetConfig(desktopOverlayConfig(homeEnabled))
	executor := &desktopOverlayCaptureExecutor{}
	manager.RegisterExecutor(executor)
	if homeEnabled {
		manager.PublishHomeDispatch(desktopOverlayHomeDispatcher{}, executionregistry.New(), 1)
	} else {
		const authID = "desktop-overlay-local"
		registry.GetGlobalRegistry().RegisterClient(authID, "kimi", []*registry.ModelInfo{{ID: "kimi-k3"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "kimi", Status: StatusActive}); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}
	return manager, executor
}

func desktopOverlayManager(homeEnabled bool) *Manager {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(desktopOverlayConfig(homeEnabled))
	return manager
}

func desktopOverlayConfig(homeEnabled bool) *internalconfig.Config {
	return &internalconfig.Config{
		Home: internalconfig.HomeConfig{Enabled: homeEnabled},
		Codex: internalconfig.CodexConfig{DesktopToolOverlay: internalconfig.CodexDesktopToolOverlayConfig{
			Enabled: true,
			Tools:   []string{"read_thread"},
		}},
	}
}

func desktopOverlayHeaders() http.Header {
	return http.Header{"User-Agent": {"Codex Desktop/26.727.51351"}}
}

func desktopOverlayTopLevelNamespace() []any {
	return []any{desktopOverlayNamespace()}
}

func desktopOverlayNamespace(children ...any) map[string]any {
	if children == nil {
		children = []any{}
	}
	return map[string]any{
		"type":        "namespace",
		"name":        "codex_app",
		"description": "Tools in the codex_app namespace.",
		"tools":       children,
	}
}

func desktopOverlayRootBody(threadSource string, flatMetadata map[string]any, tools []any) []byte {
	canonical, err := json.Marshal(map[string]any{"request_kind": "turn", "thread_source": threadSource})
	if err != nil {
		panic(err)
	}
	clientMetadata := map[string]any{"x-codex-turn-metadata": string(canonical)}
	for key, value := range flatMetadata {
		clientMetadata[key] = value
	}
	root := map[string]any{
		"model":           "client-model",
		"input":           []any{map[string]any{"type": "message", "role": "user", "content": "hello"}},
		"tools":           tools,
		"client_metadata": clientMetadata,
	}
	body, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return body
}

func desktopOverlayBodyWithoutMetadata(tools []any) []byte {
	body, err := json.Marshal(map[string]any{
		"model": "client-model",
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "hello"}},
		"tools": tools,
	})
	if err != nil {
		panic(err)
	}
	return body
}

func desktopOverlayRootBodyWithAdditional(threadSource string, topTools, additionalTools []any) []byte {
	canonical, err := json.Marshal(map[string]any{"request_kind": "turn", "thread_source": threadSource})
	if err != nil {
		panic(err)
	}
	root := map[string]any{
		"model": "client-model",
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": additionalTools},
			map[string]any{"type": "message", "role": "user", "content": "hello"},
		},
		"client_metadata": map[string]any{"x-codex-turn-metadata": string(canonical)},
	}
	if topTools != nil {
		root["tools"] = topTools
	}
	body, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return body
}

func desktopOverlayCodexAppChildren(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	all := desktopOverlayAllCodexAppChildren(t, body)
	if len(all) == 0 {
		t.Fatal("codex_app namespace missing")
	}
	return all[0]
}

func desktopOverlayAllCodexAppChildren(t *testing.T, body []byte) [][]map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	collections, ok := desktopOverlayToolCollections(root)
	if !ok {
		t.Fatal("invalid tool collections")
	}
	all := make([][]map[string]any, 0, 2)
	for _, tools := range collections {
		for _, rawTool := range tools {
			tool, ok := rawTool.(map[string]any)
			if !ok || tool["type"] != "namespace" || tool["name"] != "codex_app" {
				continue
			}
			rawChildren, _ := tool["tools"].([]any)
			children := make([]map[string]any, 0, len(rawChildren))
			for _, rawChild := range rawChildren {
				if child, ok := rawChild.(map[string]any); ok {
					children = append(children, child)
				}
			}
			all = append(all, children)
		}
	}
	return all
}

func desktopOverlayChildNames(children []map[string]any) []string {
	names := make([]string, 0, len(children))
	for _, child := range children {
		if name, ok := child["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func desktopOverlayChildByName(t *testing.T, children []map[string]any, name string) map[string]any {
	t.Helper()
	for _, child := range children {
		if child["name"] == name {
			return child
		}
	}
	t.Fatalf("child %s missing", name)
	return nil
}

func desktopOverlayHasChild(body []byte, name string) bool {
	return len(gjson.GetBytes(body, `tools.#(name=="codex_app")#.tools.#(name=="`+name+`")#`).Array()) > 0 ||
		len(gjson.GetBytes(body, `input.#(type=="additional_tools")#.tools.#(name=="codex_app")#.tools.#(name=="`+name+`")#`).Array()) > 0
}
