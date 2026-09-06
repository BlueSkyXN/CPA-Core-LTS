package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestChatExecutorRejectsNativeOnlyControlsBeforeNetwork(t *testing.T) {
	executor := &OpenAICompatExecutor{cfg: &config.Config{}}
	request := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`)}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	_, syncErr := executor.Execute(context.Background(), nil, request, options)
	_, streamErr := executor.ExecuteStream(context.Background(), nil, request, options)
	for _, err := range []error{syncErr, streamErr} {
		status, ok := err.(interface{ StatusCode() int })
		if !ok || status.StatusCode() != http.StatusBadRequest {
			t.Fatalf("error = %v, want HTTP 400 without network", err)
		}
	}
}
