package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestEnsureExecutionRequestIDPreservesOrGeneratesStableID(t *testing.T) {
	req := cliproxyexecutor.Request{}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RequestIDMetadataKey: "request-existing",
	}}
	_, opts = ensureExecutionRequestID(req, opts)
	if got := stringMetadataValue(opts.Metadata, cliproxyexecutor.RequestIDMetadataKey); got != "request-existing" {
		t.Fatalf("existing request ID = %q", got)
	}

	generatedReq, generatedOpts := ensureExecutionRequestID(cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
	generatedID := stringMetadataValue(generatedOpts.Metadata, cliproxyexecutor.RequestIDMetadataKey)
	if generatedID == "" {
		t.Fatal("generated request ID is empty")
	}
	_, generatedOpts = ensureExecutionRequestID(generatedReq, generatedOpts)
	if got := stringMetadataValue(generatedOpts.Metadata, cliproxyexecutor.RequestIDMetadataKey); got != generatedID {
		t.Fatalf("generated request ID changed from %q to %q", generatedID, got)
	}
}
