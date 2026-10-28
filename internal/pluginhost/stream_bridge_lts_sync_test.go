package pluginhost

import (
	"context"
	"errors"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStreamCallbacksStructuredLifecyclePreservesLTSClassification(t *testing.T) {
	for _, method := range []string{"emit", "close"} {
		t.Run(method, func(t *testing.T) {
			host := New()
			id, chunks, cleanup := host.streams.open(context.Background())
			t.Cleanup(cleanup)
			body := []byte(`{"stream_id":"` + id + `","error_detail":{"code":"` + coreauth.ErrorCodeConnectionLifecycle + `","message":"stream detached","retryable":true,"failure_reason":"plugin_transport"}}`)
			var err error
			if method == "emit" {
				_, err = host.callHostStreamEmit(context.Background(), body)
			} else {
				_, err = host.callHostStreamClose(body)
			}
			if err != nil {
				t.Fatal(err)
			}
			chunk := <-chunks
			var authErr *coreauth.Error
			if !errors.As(chunk.Err, &authErr) || authErr.Code != coreauth.ErrorCodeConnectionLifecycle || !authErr.Retryable {
				t.Fatalf("lifecycle classification lost: %v", chunk.Err)
			}
			var diagnostic interface{ FailureReason() string }
			if !errors.As(chunk.Err, &diagnostic) || diagnostic.FailureReason() != "plugin_transport" {
				t.Fatalf("usage diagnostic lost: %v", chunk.Err)
			}
		})
	}
}
