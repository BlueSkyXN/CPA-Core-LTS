package handlers

import (
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataIncludesInternalCodexContextResetReplayMarker(t *testing.T) {
	meta := requestExecutionMetadata(WithCodexModelFallbackContextResetReplay(context.Background()))
	if got, ok := meta[coreexecutor.CodexModelFallbackContextResetReplayMetadataKey].(bool); !ok || !got {
		t.Fatalf("context reset replay marker = %#v, want true", meta[coreexecutor.CodexModelFallbackContextResetReplayMetadataKey])
	}
}
