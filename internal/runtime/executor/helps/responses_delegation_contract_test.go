package helps

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestResponsesDelegationTranslationMatrix(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{Client: config.ClientConfig{Codex: config.CodexClientConfig{OptimizeMultiAgentV2: true}}}}
	for _, target := range []struct {
		format sdktranslator.Format
		role   string
	}{
		{sdktranslator.FormatClaude, "messages.0.role"},
		{sdktranslator.FormatOpenAI, "messages.0.role"},
		{sdktranslator.FormatGemini, "contents.0.role"},
		{sdktranslator.FormatAntigravity, "request.contents.0.role"},
	} {
		t.Run(target.format.String(), func(t *testing.T) {
			for _, userAgent := range []string{"Codex Desktop/0.163.0", "zcode/1.0"} {
				headers := http.Header{"User-Agent": []string{userAgent}}
				opaque := []byte(`{"input":[{"type":"message","role":"user","content":"Earlier context"},{"type":"agent_message","content":[{"type":"input_text","text":"NEW_TASK"},{"type":"encrypted_content","encrypted_content":"opaque-delegation-fixture"}]}]}`)
				for _, stream := range []bool{false, true} {
					_, err := TranslateRequestReturningError(context.Background(), headers, cfg, sdktranslator.FormatOpenAIResponse, target.format, "test-model", opaque, stream, false)
					var refusal *translatorcommon.UnconvertedAgentMessageError
					if !errors.As(err, &refusal) || refusal.StatusCode() != 400 || !refusal.IsRequestScoped() {
						t.Fatalf("client=%s stream=%t: unreadable task was not rejected: %v", userAgent, stream, err)
					}
				}
				standard := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"standard task"}]}]}`)
				out, err := TranslateRequestReturningError(context.Background(), headers, cfg, sdktranslator.FormatOpenAIResponse, target.format, "test-model", standard, false, false)
				if err != nil || !strings.Contains(string(out), "standard task") {
					t.Fatalf("ordinary client request changed: %v %s", err, out)
				}
			}
			plain := []byte(`{"input":[{"type":"agent_message","content":[{"type":"input_text","text":"delegated task"}]}]}`)
			out, err := TranslateRequestReturningError(context.Background(), http.Header{"User-Agent": []string{"codex_exec/0.163.0"}}, cfg, sdktranslator.FormatOpenAIResponse, target.format, "test-model", plain, true, false)
			if err != nil || gjson.GetBytes(out, target.role).String() != "user" || !strings.Contains(string(out), "delegated task") {
				t.Fatalf("plaintext task lost its user turn: %v %s", err, out)
			}
		})
	}
}

func TestCodexNativeAndCompatDelegationBoundaries(t *testing.T) {
	payload := []byte(`{"input":[{"type":"agent_message","author":"/root","content":[{"type":"encrypted_content","encrypted_content":"opaque-native-fixture"}]}]}`)
	headers := http.Header{"User-Agent": []string{"Codex Desktop/0.163.0"}}
	for _, enabled := range []bool{false, true} {
		cfg := &config.Config{SDKConfig: config.SDKConfig{Client: config.ClientConfig{Codex: config.CodexClientConfig{OptimizeMultiAgentV2: enabled}}}}
		native, _, err := OptimizeCodexMultiAgentV2RequestForAuth(context.Background(), headers, payload, cfg, nil, false)
		if err != nil || string(native) != string(payload) {
			t.Fatalf("native encrypted history changed: %v", err)
		}
		_, _, err = OptimizeCodexMultiAgentV2RequestForAuth(context.Background(), headers, payload, cfg, nil, true)
		var refusal *translatorcommon.UnconvertedAgentMessageError
		if !errors.As(err, &refusal) {
			t.Fatalf("compatibility route accepted opaque history: %v", err)
		}
	}
}
