package executor

import (
	"github.com/MoeForge/moego/ojson"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestXAIResponsesDelegatedTaskKeepsUserRole(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{Client: config.ClientConfig{Codex: config.CodexClientConfig{OptimizeMultiAgentV2: true}}}}
	payload := []byte(`{"model":"grok-4.7","input":[{"type":"agent_message","author":"/root","recipient":"/root/check","content":[{"type":"input_text","text":"Reply with the requested calculation."}]}]}`)
	prepared, err := NewXAIExecutor(cfg).prepareResponsesRequest(codexSpawnAgentTestContext(), coreexecutor.Request{Model: "grok-4.7", Payload: payload}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, true)
	if err != nil {
		t.Fatal(err)
	}
	message := ojson.GetBytes(prepared.body, "input.0")
	if message.Get("type").String() != "message" || message.Get("role").String() != "user" || message.Get("content.0.type").String() != "input_text" {
		t.Fatalf("delegated task must remain a user instruction: %s", message.Raw)
	}
}

func TestXAIResponsesRejectsOpaqueDelegatedTask(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := &config.Config{SDKConfig: config.SDKConfig{Client: config.ClientConfig{Codex: config.CodexClientConfig{OptimizeMultiAgentV2: enabled}}}}
		payload := []byte(`{"model":"grok-4.7","input":[{"type":"agent_message","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"opaque-task-fixture"}]}]}`)
		_, err := NewXAIExecutor(cfg).prepareResponsesRequest(codexSpawnAgentTestContext(), coreexecutor.Request{Model: "grok-4.7", Payload: payload}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, true)
		if err == nil {
			t.Fatalf("optimization=%t: unsupported delegated task would reach xAI", enabled)
		}
		scoped, ok := err.(interface{ IsRequestScoped() bool })
		if !ok || !scoped.IsRequestScoped() {
			t.Fatalf("error must not cause credential rotation: %v", err)
		}
	}
}
