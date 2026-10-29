package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestExecutorModelInfoResolvesSelectedRoute(t *testing.T) {
	model := func(id, canonical string, output int) *registry.ModelInfo {
		return &registry.ModelInfo{ID: id, Name: id, MetadataModelID: canonical, MaxCompletionTokens: output}
	}
	for _, tc := range []struct {
		name      string
		models    []*registry.ModelInfo
		upstream  string
		requested string
		prefix    string
		wantLimit int
		wantError bool
	}{
		{"direct", []*registry.ModelInfo{model("upstream", "", 100)}, "upstream", "", "", 100, false},
		{"name", []*registry.ModelInfo{{ID: "public", Name: "upstream", MaxCompletionTokens: 100}}, "upstream", "", "", 100, false},
		{"no-fork-alias", []*registry.ModelInfo{model("public", "upstream", 100)}, "upstream", "public", "", 100, false},
		{"canonical-only", []*registry.ModelInfo{model("public", "upstream", 100)}, "upstream", "", "", 100, false},
		{"prefix", []*registry.ModelInfo{model("tenant/upstream", "upstream", 100)}, "upstream", "tenant/upstream", "tenant", 100, false},
		{"suffix", []*registry.ModelInfo{model("upstream", "", 100)}, "upstream(max)", "upstream(max)", "", 100, false},
		{"exact-suffix", []*registry.ModelInfo{model("upstream", "", 100), model("upstream(max)", "", 200)}, "upstream(max)", "upstream(max)", "", 200, false},
		{"alias-selects-second", []*registry.ModelInfo{model("first", "upstream", 100), model("second", "upstream", 200)}, "upstream", "second", "", 200, false},
		{"alias-before-direct", []*registry.ModelInfo{model("upstream", "", 100), model("public", "upstream", 200)}, "upstream", "public", "", 200, false},
		{"rewritten-model", []*registry.ModelInfo{model("public", "old-upstream", 100), model("new-upstream", "", 200)}, "new-upstream", "public", "", 200, false},
		{"ambiguous-canonical", []*registry.ModelInfo{model("first", "upstream", 100), model("second", "upstream", 200)}, "upstream", "", "", 0, true},
		{"catalog-miss", []*registry.ModelInfo{model("other", "", 100)}, "upstream", "", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := New()
			adapter := newCurrentExecutorAdapterForTest(host, "selected-model", &fakeExecutor{}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
			auth := &coreauth.Auth{ID: "selected-model-" + tc.name, Provider: adapter.provider, Prefix: tc.prefix}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, auth.Provider, tc.models)
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			opts := coreexecutor.Options{Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: tc.requested}}
			selected, err := adapter.executionModelInfo(auth, coreexecutor.Request{Model: tc.upstream}, opts)
			if tc.wantError {
				var scoped *coreauth.Error
				if !errors.As(err, &scoped) || !scoped.IsRequestScoped() || scoped.Retryable {
					t.Fatalf("ambiguous capability must return a non-retryable request-scoped error: %v", err)
				}
				return
			}
			if err != nil || selected == nil || selected.MaxCompletionTokens != tc.wantLimit {
				t.Fatalf("selected=%+v err=%v, want limit %d", selected, err, tc.wantLimit)
			}
			if tc.name == "catalog-miss" && (!selected.UserDefined || selected.ID != tc.upstream) {
				t.Fatal("catalog miss did not retain authoritative unknown capabilities")
			}
		})
	}
}

func TestExecutorModelInfoResolvedAndStaticPrecedence(t *testing.T) {
	host := New()
	adapter := newCurrentExecutorAdapterForTest(host, "selected-source", &fakeExecutor{}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
	static := &registry.ModelInfo{ID: "model", MaxCompletionTokens: 100}
	host.modelRegistrations[adapter.pluginID] = pluginModelRegistration{models: []*registry.ModelInfo{static}}
	selected, err := adapter.executionModelInfo(nil, coreexecutor.Request{Model: "model"}, coreexecutor.Options{})
	if err != nil || selected == nil || selected.MaxCompletionTokens != 100 {
		t.Fatal("static plugin capability not selected", err)
	}
	auth := &coreauth.Auth{ID: "selected-source-auth", Provider: adapter.provider}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "model", MaxCompletionTokens: 200}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	selected, err = adapter.executionModelInfo(auth, coreexecutor.Request{Model: "model"}, coreexecutor.Options{})
	if err != nil || selected == nil || selected.MaxCompletionTokens != 200 {
		t.Fatal("auth catalog did not take precedence over static models", err)
	}
	apiKeyInfo := &registry.ModelInfo{ID: "model", MaxCompletionTokens: 300}
	homeInfo := &registry.ModelInfo{ID: "model"}
	req := coreexecutor.Request{Model: "model", Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": apiKeyInfo}}
	selected, err = adapter.executionModelInfo(auth, req, coreexecutor.Options{})
	if err != nil || selected != apiKeyInfo {
		t.Fatal("resolved API-key capability was replaced", err)
	}
	req.Metadata["cliproxy.resolved_home_model_info"] = homeInfo
	selected, err = adapter.executionModelInfo(auth, req, coreexecutor.Options{})
	if err != nil || selected != homeInfo {
		t.Fatal("authoritative empty Home capability was replaced", err)
	}
	reg.UnregisterClient(auth.ID)
	selected, err = adapter.executionModelInfo(auth, coreexecutor.Request{Model: "model"}, coreexecutor.Options{})
	if err != nil || selected != nil {
		t.Fatal("legacy auth without model metadata unexpectedly inherited static models", err)
	}
}

func TestExecutorModelInfoPreventsGlobalFallbackOnCatalogMiss(t *testing.T) {
	host := New()
	adapter := newCurrentExecutorAdapterForTest(host, "model-catalog-miss", &fakeExecutor{}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
	reg := registry.GetGlobalRegistry()
	auth := &coreauth.Auth{ID: "catalog-miss-selected", Provider: adapter.provider}
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "another-model"}})
	reg.RegisterClient("catalog-miss-peer", "claude", []*registry.ModelInfo{{ID: "catalog-miss-model", MaxCompletionTokens: 4096, Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
		reg.UnregisterClient("catalog-miss-peer")
	})
	for _, source := range []struct {
		format tr.Format
		body   string
	}{
		{tr.FormatOpenAIResponse, `{"input":"hello","max_output_tokens":64000,"reasoning":{"summary":"auto"}}`},
		{tr.FormatOpenAI, `{"messages":[{"role":"user","content":"hello"}],"max_completion_tokens":64000,"reasoning_effort":"max"}`},
	} {
		prepared, err := adapter.prepareExecutorCallForAuth(context.Background(), auth, coreexecutor.Request{Model: "catalog-miss-model", Payload: []byte(source.body)}, coreexecutor.Options{SourceFormat: source.format})
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(prepared.req.Payload, "max_tokens").Int() != 64000 || gjson.GetBytes(prepared.req.Payload, "thinking").Exists() {
			t.Fatal("selected catalog miss inherited global capability")
		}
	}
}

func TestExecutorSelectedCapabilitiesAcrossExecutionCalls(t *testing.T) {
	const model = "plugin-selected-execution"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("selected-execution-auth", "plugin-provider", []*registry.ModelInfo{{ID: model, MaxCompletionTokens: 128000, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}}}})
	reg.RegisterClient("selected-execution-second", "plugin-provider", []*registry.ModelInfo{{ID: model, MaxCompletionTokens: 4096, Thinking: &registry.ThinkingSupport{Levels: []string{"high"}}}})
	reg.RegisterClient("selected-execution-peer", "claude", []*registry.ModelInfo{{ID: model, MaxCompletionTokens: 1024, Thinking: &registry.ThinkingSupport{Min: 1024, Max: 32768}}})
	t.Cleanup(func() {
		for _, id := range []string{"selected-execution-auth", "selected-execution-second", "selected-execution-peer"} {
			reg.UnregisterClient(id)
		}
	})
	var captured pluginapi.ExecutorRequest
	executor := &fakeExecutor{
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			captured = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"type":"message","content":[]}`)}, nil
		},
		countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			captured = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":1}`)}, nil
		},
		executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			captured = req
			chunks := make(chan pluginapi.ExecutorStreamChunk)
			close(chunks)
			return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(New(), "selected-execution", executor, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
	for _, source := range []tr.Format{tr.FormatOpenAIResponse, tr.FormatOpenAI} {
		for _, authID := range []string{"selected-execution-auth", "selected-execution-second", "selected-execution-auth"} {
			auth := &coreauth.Auth{ID: authID, Provider: adapter.provider}
			for _, operation := range []string{"execute", "stream", "count"} {
				req := coreexecutor.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"input":"hello","reasoning":{"effort":"max"},"max_output_tokens":64000}`, model))}
				if source == tr.FormatOpenAI {
					req.Payload = []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"reasoning_effort":"max","max_completion_tokens":64000}`, model))
				}
				opts := coreexecutor.Options{SourceFormat: source, ResponseFormat: tr.FormatClaude, Stream: operation == "stream"}
				var err error
				switch operation {
				case "execute":
					_, err = adapter.Execute(context.Background(), auth, req, opts)
				case "count":
					_, err = adapter.CountTokens(context.Background(), auth, req, opts)
				case "stream":
					var result *coreexecutor.StreamResult
					result, err = adapter.ExecuteStream(context.Background(), auth, req, opts)
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				wantEffort, wantMax := "max", int64(64000)
				if authID == "selected-execution-second" {
					wantEffort, wantMax = "high", 4096
				}
				if captured.AuthID != authID || gjson.GetBytes(captured.Payload, "output_config.effort").String() != wantEffort || gjson.GetBytes(captured.Payload, "max_tokens").Int() != wantMax {
					t.Fatalf("%s reused another account's capabilities for %s", operation, authID)
				}
			}
		}
	}
}

func TestExecutorSummaryIntentAndNormalizerAuthority(t *testing.T) {
	defer tr.SetPluginHooks(nil)
	host := New()
	adapter := newCurrentExecutorAdapterForTest(host, "summary-controls", &fakeExecutor{}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
	host.modelRegistrations[adapter.pluginID] = pluginModelRegistration{models: []*registry.ModelInfo{{ID: "summary-controls", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "max"}}}}}
	for _, source := range []tr.Format{tr.FormatOpenAIResponse, tr.FormatOpenAI} {
		for _, remove := range []bool{false, true} {
			tr.SetPluginHooks(&anthropicTestHooks{removeThinking: remove})
			for _, tc := range []struct{ controls, display, effort string }{
				{``, "", ""},
				{`"reasoning":{"summary":"auto"}`, "summarized", ""},
				{`"reasoning":{"summary":"concise"}`, "summarized", ""},
				{`"reasoning":{"summary":"detailed"}`, "summarized", ""},
				{`"reasoning":{"summary":"none"}`, "", ""},
				{`"reasoning":{"summary":null}`, "", ""},
				{`"reasoning":{"summary":"none","effort":"high"},"reasoning_effort":"high"`, "omitted", "high"},
				{`"reasoning":{"summary":null,"effort":"high"},"reasoning_effort":"high"`, "omitted", "high"},
			} {
				body := `{"messages":[{"role":"user","content":"hello"}],"input":"hello"`
				if tc.controls != "" {
					body += "," + tc.controls
				}
				body += "}"
				prepared, err := adapter.prepareExecutorCallForAuth(context.Background(), nil, coreexecutor.Request{Model: "summary-controls", Payload: []byte(body)}, coreexecutor.Options{SourceFormat: source})
				if err != nil {
					t.Fatal(err)
				}
				display := tc.display
				if remove {
					display = ""
				}
				if gjson.GetBytes(prepared.req.Payload, "thinking.display").String() != display || gjson.GetBytes(prepared.req.Payload, "output_config.effort").String() != tc.effort {
					t.Errorf("summary intent mismatch source=%s remove=%v controls=%s", source, remove, tc.controls)
				}
			}
			tr.SetPluginHooks(nil)
		}
	}
}
