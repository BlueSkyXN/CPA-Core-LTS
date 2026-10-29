package handlers

import (
	"context"
	"fmt"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRequestScopedBootstrapErrorAfterDroppedPayloadDoesNotRetry(t *testing.T) {
	executor := &bootstrapStreamExecutor{stream: func(context.Context, int) (*coreexecutor.StreamResult, error) {
		chunks := make(chan coreexecutor.StreamChunk, 2)
		chunks <- coreexecutor.StreamChunk{Payload: []byte("drop")}
		chunks <- coreexecutor.StreamChunk{Err: fmt.Errorf("conversion: %w", coreauth.NewRequestScopedError("synthetic conversion failure", 502))}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}}
	handler, _ := registerBootstrapExecutor(t, executor)
	handler.SetPluginHost(&handlerInterceptorTestHost{interceptStreamChunk: func(_ context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
		return pluginapi.StreamChunkInterceptResponse{Body: cloneBytes(req.Body), DropChunk: string(req.Body) == "drop"}
	}})
	data, _, errs := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "bootstrap-model", []byte(`{"model":"bootstrap-model"}`), "")
	for range data {
	}
	for range errs {
	}
	if executor.Calls() != 1 {
		t.Fatalf("local request error caused %d executions", executor.Calls())
	}
}
