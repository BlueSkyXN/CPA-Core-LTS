package auth

import (
	"context"
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// F12: an after-auth interceptor cannot silently switch the protocol
// operation by rewriting only the request_path metadata.
func TestAfterAuthInterceptorRejectsIncompatiblePathOverride(t *testing.T) {
	for _, tc := range []struct {
		current, override string
		wantErr           bool
	}{
		{"/v1/responses", "/v1/images/generations", true},
		{"/v1/responses", "/v1/responses/compact", true},
		{"/v1/images/edits", "/v1/images/generations", false},
	} {
		opts := cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.RequestPathMetadataKey: tc.current},
			RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{Path: tc.override}
			},
		}
		_, gotOpts, err := applyRequestAfterAuthInterceptorWithManager(nil, context.Background(), nil, &Auth{ID: "a"}, "openai", cliproxyexecutor.Request{Model: "m"}, opts, "m")
		if tc.wantErr {
			var pathErr *cliproxyexecutor.RequestPathOverrideError
			if !errors.As(err, &pathErr) || !isRequestScopedError(err) {
				t.Fatalf("%s -> %s: err=%v, want request-scoped path override error", tc.current, tc.override, err)
			}
			continue
		}
		if err != nil || cliproxyexecutor.RequestPathFromMetadata(gotOpts.Metadata) != tc.override {
			t.Fatalf("%s -> %s: err=%v path=%v", tc.current, tc.override, err, gotOpts.Metadata)
		}
	}
}
