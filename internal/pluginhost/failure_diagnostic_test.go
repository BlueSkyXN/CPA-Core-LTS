package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRPCFailureReasonDoesNotChangeErrorClassification(t *testing.T) {
	_, err := decodeRPCEnvelope[rpcEmptyResponse]([]byte(`{"ok":false,"error":{"code":"upstream_error","message":"[1301] Content safety check blocked the request","http_status":400,"retryable":false,"failure_reason":"bigmodel_1301"}}`))
	var diagnostic interface{ FailureReason() string }
	if !errors.As(err, &diagnostic) || diagnostic.FailureReason() != "bigmodel_1301" {
		t.Fatalf("missing business reason: %v", err)
	}
	var rpc rpcError
	if !errors.As(err, &rpc) || rpc.PluginCode() != "upstream_error" || rpc.StatusCode() != 400 {
		t.Fatalf("classification changed: %v", err)
	}
}

func TestStreamFailureReasonDoesNotChangeErrorClassification(t *testing.T) {
	host := New()
	defer host.ShutdownAll()
	id, chunks, cleanup := host.streams.open(context.Background())
	defer cleanup()
	raw, err := json.Marshal(rpcStreamCloseRequest{StreamID: id, Error: "[1301] Content safety check blocked the request", ErrorCode: "upstream_error", HTTPStatus: 502, FailureReason: "bigmodel_1301"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.callHostStreamClose(raw); err != nil {
		t.Fatal(err)
	}
	chunk, ok := <-chunks
	if !ok || chunk.Err == nil {
		t.Fatal("missing terminal error")
	}
	var diagnostic interface{ FailureReason() string }
	var authErr *coreauth.Error
	if !errors.As(chunk.Err, &diagnostic) || diagnostic.FailureReason() != "bigmodel_1301" {
		t.Fatalf("missing stream business reason: %v", chunk.Err)
	}
	if !errors.As(chunk.Err, &authErr) || authErr.Code != "upstream_error" || authErr.HTTPStatus != 502 || authErr.Retryable {
		t.Fatalf("classification changed: %v", chunk.Err)
	}
	if _, ok := <-chunks; ok {
		t.Fatal("duplicate terminal event")
	}
}
