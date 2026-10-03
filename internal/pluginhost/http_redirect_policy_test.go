package pluginhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPluginHTTPDisableRedirects(t *testing.T) {
	for _, status := range []int{307, 308} {
		for _, wire := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/wire=%v/stream=%v", status, wire, stream), func(t *testing.T) {
					var received atomic.Int32
					target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						received.Add(1)
						_, _ = io.Copy(io.Discard, r.Body)
						_, _ = io.WriteString(w, "ok")
					}))
					defer target.Close()
					origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Location", target.URL)
						w.WriteHeader(status)
					}))
					defer origin.Close()
					profile := ""
					if wire {
						profile = `,"wire_profile":{"http1_only":true}`
					}
					request, err := decodeHostHTTPRequest([]byte(fmt.Sprintf(`{"method":"POST","url":%q,"body":"c3ludGhldGlj","disable_redirects":true%s}`, origin.URL, profile)))
					if err != nil {
						t.Fatal(err)
					}
					client := New().newHTTPClient(nil)
					if stream {
						response, err := client.DoStream(context.Background(), request)
						if err != nil {
							t.Fatal(err)
						}
						for range response.Chunks {
						}
						if response.StatusCode != status {
							t.Errorf("status=%d", response.StatusCode)
						}
					} else {
						response, err := client.Do(context.Background(), request)
						if err != nil {
							t.Fatal(err)
						}
						if response.StatusCode != status {
							t.Errorf("status=%d", response.StatusCode)
						}
					}
					if received.Load() != 0 {
						t.Error("redirect target received credential-bearing request")
					}
					response, err := client.Do(context.Background(), pluginapi.HTTPRequest{Method: "POST", URL: origin.URL, Body: []byte("synthetic")})
					if err != nil || response.StatusCode != 200 || !strings.Contains(string(response.Body), "ok") {
						t.Fatalf("default redirect behavior changed: %v", err)
					}
				})
			}
		}
	}
}
