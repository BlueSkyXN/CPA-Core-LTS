package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestSubscriptionAdapterDelegationRequestContract(t *testing.T) {
	for _, target := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI} {
		t.Run(target.String(), func(t *testing.T) {
			host := New()
			host.runtimeConfig = &config.Config{SDKConfig: config.SDKConfig{Client: config.ClientConfig{Codex: config.CodexClientConfig{OptimizeMultiAgentV2: true}}}}
			adapter := newCurrentExecutorAdapterForTest(host, "subscription-delegation-test", &fakeExecutor{}, []sdktranslator.Format{target}, []sdktranslator.Format{target})
			for _, userAgent := range []string{"Codex Desktop/0.163.0", "zcode/1.0"} {
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{"User-Agent": []string{userAgent}}}
				request := coreexecutor.Request{Model: "test-model", Payload: []byte(`{"input":[{"type":"message","role":"user","content":"Earlier context"},{"type":"agent_message","content":[{"type":"input_text","text":"NEW_TASK"},{"type":"encrypted_content","encrypted_content":"opaque-subscription-fixture"}]}]}`)}
				_, err := adapter.prepareExecutorCallForAuth(context.Background(), nil, request, opts)
				var refusal *translatorcommon.UnconvertedAgentMessageError
				if !errors.As(err, &refusal) {
					t.Fatalf("client=%s: plugin preparation lost the delegation error: %v", userAgent, err)
				}
				request.Payload = []byte(`{"input":[{"type":"message","role":"user","content":"ordinary subscription task"}]}`)
				prepared, err := adapter.prepareExecutorCallForAuth(context.Background(), nil, request, opts)
				if err != nil || !strings.Contains(string(prepared.req.Payload), "ordinary subscription task") {
					t.Fatalf("ordinary Responses request failed: %v", err)
				}
			}
			request := coreexecutor.Request{Model: "test-model", Payload: []byte(`{"input":[{"type":"agent_message","content":[{"type":"input_text","text":"plaintext subscription task"}]}]}`)}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{"User-Agent": []string{"codex_exec/0.163.0"}}}
			prepared, err := adapter.prepareExecutorCallForAuth(context.Background(), nil, request, opts)
			if err != nil {
				t.Fatal(err)
			}
			rolePath := "messages.0.role"
			if gjson.GetBytes(prepared.req.Payload, rolePath).String() != "user" || !strings.Contains(string(prepared.req.Payload), "plaintext subscription task") {
				t.Fatalf("subscription task changed: %s", prepared.req.Payload)
			}
		})
	}
}

func TestClaudeCodeSubscriptionToolTurnRemainsNative(t *testing.T) {
	host := New()
	adapter := newCurrentExecutorAdapterForTest(host, "native-messages-test", &fakeExecutor{}, []sdktranslator.Format{sdktranslator.FormatClaude}, []sdktranslator.Format{sdktranslator.FormatClaude})
	body := []byte(`{"messages":[{"role":"user","content":"inspect a file"},{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{"path":"example.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"the file mentions agent_message"}]}]}`)
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"User-Agent": []string{"claude-cli/2.1"}}}
	prepared, err := adapter.prepareExecutorCallForAuth(context.Background(), nil, coreexecutor.Request{Model: "test-model", Payload: body}, opts)
	if err != nil || string(prepared.req.Payload) != string(body) {
		t.Fatalf("native Messages tool turn changed: %v %s", err, prepared.req.Payload)
	}
}

// A plugin that accepts Responses directly owns its extension contract. The
// host must not infer an incompatible target merely from the format name.
func TestResponsesPluginPassthroughKeepsNativeDelegation(t *testing.T) {
	host := New()
	adapter := newCurrentExecutorAdapterForTest(host, "native-responses-test", &fakeExecutor{}, []sdktranslator.Format{sdktranslator.FormatOpenAIResponse}, []sdktranslator.Format{sdktranslator.FormatOpenAIResponse})
	body := []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"opaque-native-plugin-fixture"}]}]}`)
	prepared, err := adapter.prepareExecutorCallForAuth(context.Background(), nil, coreexecutor.Request{Model: "test-model", Payload: body}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil || string(prepared.req.Payload) != string(body) {
		t.Fatalf("plugin-owned Responses payload changed: %v %s", err, prepared.req.Payload)
	}
}
