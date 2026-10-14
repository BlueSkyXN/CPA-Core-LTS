package pluginhost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostHTTPUsageSizeFollowsRedirect(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, disableRedirects := range []bool{false, true} {
			name := "buffered"
			if stream {
				name = "stream"
			}
			if disableRedirects {
				name += "/blocked"
			} else {
				name += "/followed"
			}
			t.Run(name, func(t *testing.T) {
				const requestBody = "initial-payload"
				const redirectBody = "redirect-response"
				const finalBody = "final-response"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/start" {
						w.Header().Set("Location", "/final")
						w.WriteHeader(http.StatusSeeOther)
						_, _ = io.WriteString(w, redirectBody)
						return
					}
					if r.Method != http.MethodGet || r.ContentLength != 0 {
						t.Errorf("redirected request = %s, length %d", r.Method, r.ContentLength)
					}
					_, _ = io.WriteString(w, finalBody)
				}))
				t.Cleanup(server.Close)
				sink := newTestUsageCapturePlugin(t.Name())
				registerTestUsagePlugin(t, t.Name(), sink)
				reporter := helps.NewUsageReporter(context.Background(), t.Name(), "test-model", nil)
				ctx := helps.WithUsageReporter(context.Background(), reporter)
				client := New().newHTTPClient(nil)
				req := pluginapi.HTTPRequest{
					Method: http.MethodPost, URL: server.URL + "/start", Body: []byte(requestBody),
					DisableRedirects: disableRedirects,
				}
				if stream {
					resp, err := client.DoStream(ctx, req)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range resp.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := client.Do(ctx, req); err != nil {
					t.Fatal(err)
				}
				reporter.Publish(ctx, coreusage.Detail{})
				want := coreusage.RequestSize{
					Version: coreusage.RequestSizeVersionV1, UpstreamTransport: coreusage.UpstreamTransportHTTP,
					UpstreamResponseBytes: int64(len(finalBody)),
				}
				if disableRedirects {
					want.UpstreamRequestBytes = int64(len(requestBody))
					want.UpstreamRequestWireBytes = int64(len(requestBody))
					want.UpstreamResponseBytes = int64(len(redirectBody))
				}
				if got := sink.waitRecord(t, time.Second).Size; got != want {
					t.Fatalf("size = %+v, want %+v", got, want)
				}
			})
		}
	}
}
