package executor

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestConfiguredCodexCompressionPreservesLTSExplicitNone(t *testing.T) {
	cfg := codexCompressionConfig()
	cfg.Codex.Outbound.RequestCompression = config.CodexRequestCompressionNone
	executor := NewCodexExecutor(cfg)
	payload := codexCompressionPayload()
	req, plain, _, err := executor.cacheHelper(t.Context(), sdktranslator.FormatOpenAIResponse, "https://chatgpt.com/backend-api/codex/responses", codexCompressionAuth(), coreexecutor.Request{Payload: payload}, payload, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := readCodexCompressionBody(t, req.Body); !bytes.Equal(got, plain) || req.Header.Get("Content-Encoding") != "" {
		t.Fatal("explicit LTS compression disable was overridden")
	}
}
