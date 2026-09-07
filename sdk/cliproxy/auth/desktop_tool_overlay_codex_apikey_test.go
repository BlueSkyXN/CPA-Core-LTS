package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestDesktopToolOverlayCodexAPIKeyTarget(t *testing.T) {
	apiKey := &Auth{Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-only-key"}}
	oauth := &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "test-only-token"}}
	for _, test := range []struct {
		name     string
		auth     *Auth
		provider string
		format   sdktranslator.Format
		want     bool
	}{
		{"selected API key", apiKey, "codex", sdktranslator.FormatCodex, true},
		{"explicit API key kind", &Auth{Provider: "codex", Attributes: map[string]string{AttributeAuthKind: "api-key"}}, "codex", sdktranslator.FormatCodex, true},
		{"Home API key metadata", &Auth{Provider: "codex", Metadata: map[string]any{AttributeAuthKind: "apikey"}}, "codex", sdktranslator.FormatCodex, true},
		{"case and whitespace", &Auth{Provider: " CODEX ", Attributes: map[string]string{AttributeAuthKind: " API_KEY "}}, " CoDeX ", sdktranslator.FormatCodex, true},
		{"OAuth", oauth, "codex", sdktranslator.FormatCodex, false},
		{"explicit OAuth overrides legacy key", &Auth{Provider: "codex", Attributes: map[string]string{AttributeAuthKind: "oauth", AttributeAPIKey: "test-only-key"}}, "codex", sdktranslator.FormatCodex, false},
		{"nil credential", nil, "codex", sdktranslator.FormatCodex, false},
		{"unknown kind", &Auth{Provider: "codex"}, "codex", sdktranslator.FormatCodex, false},
		{"empty key", &Auth{Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "  "}}, "codex", sdktranslator.FormatCodex, false},
		{"wrong credential provider", &Auth{Provider: "kimi", Attributes: map[string]string{AttributeAPIKey: "test-only-key"}}, "codex", sdktranslator.FormatCodex, false},
		{"missing credential provider", &Auth{Attributes: map[string]string{AttributeAPIKey: "test-only-key"}}, "codex", sdktranslator.FormatCodex, false},
		{"other Codex-format provider", apiKey, "other", sdktranslator.FormatCodex, false},
		{"unsupported wire format", apiKey, "codex", sdktranslator.FormatClaude, false},
		{"existing xAI", nil, "xai", sdktranslator.FormatCodex, true},
		{"existing Chat", nil, "kimi", sdktranslator.FormatOpenAI, true},
		{"existing Responses", nil, "openai-compatible", sdktranslator.FormatOpenAIResponse, true},
		// Preserve the pre-existing compact/Responses branch; this change adds
		// only the ordinary Codex wire path, and only for API key credentials.
		{"existing Codex compact format", oauth, "codex", sdktranslator.FormatOpenAIResponse, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := desktopToolOverlayTargetSupported(test.auth, test.provider, test.format); got != test.want {
				t.Fatalf("supported = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBuildCodexAPIKeyDesktopToolOverlayEligibility(t *testing.T) {
	apiKey := &Auth{Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-only-key"}}
	body := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
	for _, test := range []struct {
		name      string
		model     string
		requested string
		source    sdktranslator.Format
		headers   http.Header
		body      []byte
		want      string
	}{
		{"Kimi", "kimi-k3", "desktop-kimi", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, "applied"},
		{"gpt substring anywhere", "non-gpt-free-name", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, "gpt_model"},
		{"GLM fixture", "glm-test", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, "applied"},
		{"DeepSeek fixture", "deepseek-test", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, "applied"},
		{"unlisted model", "vendor-model-new", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, "applied"},
		{"requested GPT alias", "kimi-k3", "my-GPT-alias", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, "gpt_model"},
		{"selected GPT", "GpT-test", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, "gpt_model"},
		{"model missing", "", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, "selected_model_missing"},
		{"wrong source", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAI, desktopOverlayHeaders(), body, "unsupported_source"},
		{"UA case insensitive", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, http.Header{"User-Agent": {"client CODEX DESKTOP build"}}, body, "applied"},
		{"CLI UA", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, http.Header{"User-Agent": {"codex_cli_rs"}}, body, "desktop_user_agent_missing"},
		{"reversed UA", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, http.Header{"User-Agent": {"Desktop Codex"}}, body, "desktop_user_agent_missing"},
		{"missing UA", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, nil, body, "desktop_user_agent_missing"},
		{"subagent", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), desktopOverlayRootBody("subagent", nil, desktopOverlayTopLevelNamespace()), "not_root_user_turn"},
		{"flat subagent", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), desktopOverlayRootBody("user", map[string]any{"x-openai-subagent": "worker"}, desktopOverlayTopLevelNamespace()), "not_root_user_turn"},
		{"metadata missing", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), desktopOverlayBodyWithoutMetadata(desktopOverlayTopLevelNamespace()), "not_root_user_turn"},
		{"metadata malformed", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), []byte(`{"client_metadata":{"x-codex-turn-metadata":"{"}}`), "root_metadata_invalid"},
		{"namespace missing", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), desktopOverlayRootBody("user", nil, []any{}), "codex_app_namespace_missing"},
		{"tool search", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), desktopOverlayRootBody("user", nil, []any{desktopOverlayNamespace(), map[string]any{"type": "tool_search"}}), "forbidden_tool_surface"},
		{"custom exec", "kimi-k3", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), desktopOverlayRootBody("user", nil, []any{desktopOverlayNamespace(), map[string]any{"type": "custom", "name": "exec"}}), "forbidden_tool_surface"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := bytes.Clone(test.body)
			result := buildCodexDesktopToolOverlay(apiKey, "codex", sdktranslator.FormatCodex, test.model, test.requested, test.source, test.headers, test.body, []string{"read_thread"})
			if result.skipReason != test.want {
				t.Fatalf("reason = %q, want %q", result.skipReason, test.want)
			}
			if test.want == "applied" {
				if result.injectedCount != 1 || !reflect.DeepEqual(desktopOverlayChildNames(desktopOverlayCodexAppChildren(t, result.body)), []string{"read_thread"}) {
					t.Fatal("missing selected tool")
				}
			} else if result.injectedCount != 0 || !bytes.Equal(result.body, original) {
				t.Fatal("skipped request changed")
			}
			if !bytes.Equal(test.body, original) {
				t.Fatal("original request mutated")
			}
		})
	}
}

func TestBuildCodexAPIKeyDesktopToolOverlayOnlyMissing(t *testing.T) {
	apiKey := &Auth{Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-only-key"}}
	for _, additional := range []bool{false, true} {
		t.Run(map[bool]string{false: "top-level", true: "additional-tools"}[additional], func(t *testing.T) {
			existing := map[string]any{"type": "function", "name": "list_threads", "description": "client-owned", "strict": true, "parameters": map[string]any{"type": "object"}}
			namespace := desktopOverlayNamespace(existing)
			body := desktopOverlayRootBody("user", nil, []any{namespace})
			if additional {
				body = desktopOverlayRootBodyWithAdditional("user", nil, []any{namespace})
			}
			selection := []string{"list_threads", "read_thread", "wait_threads"}
			first := buildCodexDesktopToolOverlay(apiKey, "codex", sdktranslator.FormatCodex, "vendor-model", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), body, selection)
			if first.injectedCount != 2 {
				t.Fatalf("injected = %d, want 2", first.injectedCount)
			}
			children := desktopOverlayCodexAppChildren(t, first.body)
			if got := desktopOverlayChildNames(children); !reflect.DeepEqual(got, selection) {
				t.Fatalf("children = %v, want %v", got, selection)
			}
			if got := desktopOverlayChildByName(t, children, "list_threads"); !reflect.DeepEqual(got, existing) {
				t.Fatal("client-owned definition replaced")
			}
			second := buildCodexDesktopToolOverlay(apiKey, "codex", sdktranslator.FormatCodex, "vendor-model", "desktop-model", sdktranslator.FormatOpenAIResponse, desktopOverlayHeaders(), first.body, selection)
			if second.injectedCount != 0 || second.skipReason != "no_new_tools" || !bytes.Equal(second.body, first.body) {
				t.Fatal("second pass was not a byte-preserving no-op")
			}
		})
	}
}

func TestManagerCodexAPIKeyDesktopToolOverlayCredentialAndReload(t *testing.T) {
	manager := desktopOverlayManager(false)
	apiKey := &Auth{Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-only-key"}}
	oauth := &Auth{Provider: "codex", Metadata: map[string]any{"access_token": "test-only-token"}}
	body := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
	req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: desktopOverlayHeaders(), OriginalRequest: bytes.Clone(body)}
	for _, step := range []struct {
		name    string
		auth    *Auth
		enabled bool
		want    bool
	}{
		{"API key", apiKey, true, true},
		{"next attempt OAuth", oauth, true, false},
		{"next attempt no auth", nil, true, false},
		{"disabled", apiKey, false, false},
		{"re-enabled", apiKey, true, true},
	} {
		t.Run(step.name, func(t *testing.T) {
			cfg := desktopOverlayConfig(false)
			cfg.Codex.DesktopToolOverlay.Enabled = step.enabled
			manager.SetConfig(cfg)
			gotReq, gotOpts, err := manager.applyRequestAfterAuthInterceptor(context.Background(), nil, step.auth, "codex", req, opts, "desktop-model")
			if err != nil {
				t.Fatal(err)
			}
			if got := desktopOverlayHasChild(gotReq.Payload, "read_thread"); got != step.want {
				t.Fatalf("overlay = %v, want %v", got, step.want)
			}
			if !bytes.Equal(gotReq.Payload, gotOpts.OriginalRequest) {
				t.Fatal("Payload and OriginalRequest diverged")
			}
			if !bytes.Equal(req.Payload, body) || !bytes.Equal(opts.OriginalRequest, body) {
				t.Fatal("base request was mutated across attempts")
			}
			if step.want {
				gotReq.Payload[0] = ' '
				if gotOpts.OriginalRequest[0] != '{' || body[0] != '{' {
					t.Fatal("payload buffers alias")
				}
			}
		})
	}
	manager.SetConfig(desktopOverlayConfig(false))
	seen := false
	opts.RequestAfterAuthInterceptor = func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		seen = desktopOverlayHasChild(request.Body, "read_thread")
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{Body: []byte(`{"plugin":true}`)}
	}
	gotReq, gotOpts, err := manager.applyRequestAfterAuthInterceptor(context.Background(), nil, apiKey, "codex", req, opts, "desktop-model")
	if err != nil || !seen || string(gotReq.Payload) != `{"plugin":true}` || !bytes.Equal(gotReq.Payload, gotOpts.OriginalRequest) {
		t.Fatalf("plugin order or override changed: %v", err)
	}
}

// Capture executors exercise Manager dispatch, not a real provider or Desktop.
type codexAPIKeyOverlayCaptureExecutor struct{ desktopOverlayCaptureExecutor }

func (*codexAPIKeyOverlayCaptureExecutor) Identifier() string { return "codex" }

type codexAPIKeyOverlayHomeDispatcher struct{ auth *Auth }

func (codexAPIKeyOverlayHomeDispatcher) HeartbeatOK() bool       { return true }
func (codexAPIKeyOverlayHomeDispatcher) AbortAmbiguousDispatch() {}
func (d codexAPIKeyOverlayHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{Auth: *d.auth})
}

func TestManagerCodexAPIKeyDesktopToolOverlayExecutionPaths(t *testing.T) {
	for _, home := range []bool{false, true} {
		for _, apiKey := range []bool{false, true} {
			name := map[bool]string{false: "local", true: "home"}[home] + "/" + map[bool]string{false: "oauth", true: "apikey"}[apiKey]
			t.Run(name, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				manager.SetRetryConfig(0, 0, 0)
				cfg := desktopOverlayConfig(home)
				manager.SetConfig(cfg)
				executor := &codexAPIKeyOverlayCaptureExecutor{}
				manager.RegisterExecutor(executor)
				auth := &Auth{ID: "codex-overlay-" + name, Provider: "codex", Status: StatusActive}
				if apiKey {
					auth.Attributes = map[string]string{AttributeAPIKey: "test-only-key"}
				} else {
					auth.Metadata = map[string]any{"access_token": "test-only-token"}
				}
				if home {
					manager.PublishHomeDispatch(codexAPIKeyOverlayHomeDispatcher{auth: auth}, executionregistry.New(), 1)
				} else {
					registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "kimi-k3"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, err := manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
				}
				body := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
				req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: desktopOverlayHeaders(), OriginalRequest: bytes.Clone(body)}
				if _, err := manager.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
					t.Fatalf("Execute: %v", err)
				}
				if _, err := manager.ExecuteCount(context.Background(), []string{"codex"}, req, opts); err != nil {
					t.Fatalf("ExecuteCount: %v", err)
				}
				stream, err := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream: %v", err)
				}
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream: %v", chunk.Err)
					}
				}
				if got := executor.snapshotKinds(); !reflect.DeepEqual(got, []string{"execute", "count", "stream"}) {
					t.Fatalf("kinds = %v", got)
				}
				for i, captured := range executor.snapshotRequests() {
					if got := desktopOverlayHasChild(captured.req.Payload, "read_thread"); got != apiKey {
						t.Fatalf("request %d: overlay = %v, want %v", i, got, apiKey)
					}
					if !bytes.Equal(captured.req.Payload, captured.opts.OriginalRequest) {
						t.Fatalf("request %d: payload and original diverged", i)
					}
				}
				if desktopOverlayHasChild(body, "read_thread") {
					t.Fatal("original request mutated")
				}
			})
		}
	}
}

func TestManagerCodexAPIKeyDesktopToolOverlayEmptyConfig(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	auth := &Auth{Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-only-key"}}
	body := desktopOverlayRootBody("user", nil, desktopOverlayTopLevelNamespace())
	req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: body}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: desktopOverlayHeaders(), OriginalRequest: body}
	gotReq, gotOpts, err := manager.applyRequestAfterAuthInterceptor(context.Background(), nil, auth, "codex", req, opts, "kimi-k3")
	if err != nil || !bytes.Equal(gotReq.Payload, body) || !bytes.Equal(gotOpts.OriginalRequest, body) {
		t.Fatalf("default-off changed request: %v", err)
	}
}
