package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestHTTPAuthFailureDoesNotWaitForBody(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/stream=%t", status, stream), func(t *testing.T) {
				host := newFakeHost()
				host.openResponse.StatusCode = status
				host.openResponse.Headers.Set("Content-Type", "application/json")
				// 模拟已返回鉴权状态、正文保持打开且不再发送数据的上游。
				host.blockReads = true
				runtime := newPluginRuntime(host)
				requestID := "auth-failure"
				raw := nonStreamRequest(t, requestID)
				if stream {
					raw = executorRequestJSON(t, requestID)
				}
				result := make(chan error, 1)
				go func() {
					if stream {
						_, err := runtime.executeStream(raw)
						result <- err
					} else {
						_, err := runtime.execute(raw)
						result <- err
					}
				}()
				select {
				case err := <-result:
					typed, ok := err.(*pluginCallError)
					if !ok || typed.code != "upstream_auth_rejected" || typed.statusCode != status || typed.retryable {
						t.Fatalf("expected non-retryable HTTP %d auth failure, got %v", status, err)
					}
					if host.openCount() != 1 || host.httpCloseCount() != 1 {
						t.Fatal("auth failure replayed the request or did not close the upstream")
					}
				case <-time.After(time.Second):
					runtime.cancel(requestID)
					select {
					case <-result:
					case <-time.After(time.Second):
						t.Fatal("cancellation failed to release the blocked error-body read")
					}
					t.Fatal("known auth failure waited for the upstream body")
				}
			})
		}
	}
}
