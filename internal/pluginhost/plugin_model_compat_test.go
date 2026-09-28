package pluginhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPluginSelectedModelCompatibility(t *testing.T) {
	const model = "plugin-compat-fixture"
	var info pluginapi.ModelInfo
	if err := json.Unmarshal([]byte(`{"ID":"plugin-compat-fixture","IsCompat":true}`), &info); err != nil {
		t.Fatal(err)
	}
	converted := pluginModelInfoToRegistryModelInfo(info)
	if !converted.IsCompat {
		t.Error("model declaration discarded")
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("compat-auth", "plugin-provider", []*registry.ModelInfo{converted})
	reg.RegisterClient("normal-auth", "plugin-provider", []*registry.ModelInfo{{ID: model}})
	defer reg.UnregisterClient("compat-auth")
	defer reg.UnregisterClient("normal-auth")
	var captured pluginapi.ExecutorRequest
	adapter := newCurrentExecutorAdapterForTest(New(), "model-compat", &fakeExecutor{execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
		captured = req
		return pluginapi.ExecutorResponse{Payload: []byte(`{"type":"message"}`)}, nil
	}}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
	raw := []byte(`{"input":[{"role":"user","content":"hello"},{"type":"reasoning","encrypted_content":"synthetic-opaque","summary":[{"type":"summary_text","text":"reason"}]},{"role":"assistant","content":"answer"},{"role":"user","content":"continue"}]}`)
	for _, authID := range []string{"compat-auth", "normal-auth"} {
		_, err := adapter.Execute(context.Background(), &coreauth.Auth{ID: authID, Provider: "plugin-provider"}, coreexecutor.Request{Model: model, Payload: raw}, coreexecutor.Options{SourceFormat: tr.FormatOpenAIResponse, ResponseFormat: tr.FormatClaude})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, message := range gjson.GetBytes(captured.Payload, "messages").Array() {
			for _, block := range message.Get("content").Array() {
				if block.Get("type").String() == "thinking" {
					found = true
				}
			}
		}
		if found != (authID == "compat-auth") {
			t.Errorf("auth=%s compatibility=%v", authID, found)
		}
	}
	tr.SetPluginHooks(&anthropicTestHooks{})
	defer tr.SetPluginHooks(nil)
	_, err := adapter.Execute(context.Background(), &coreauth.Auth{ID: "compat-auth", Provider: "plugin-provider"}, coreexecutor.Request{Model: model, Payload: raw}, coreexecutor.Options{SourceFormat: tr.FormatOpenAIResponse, ResponseFormat: tr.FormatClaude})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(captured.Payload, "messages.1.content.0.type").String() == "thinking" {
		t.Error("compat restored content removed by final normalizer")
	}
}
