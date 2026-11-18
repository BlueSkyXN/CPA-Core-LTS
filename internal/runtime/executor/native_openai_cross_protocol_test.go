package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type nativeProtocolRequest struct {
	name string
	from translator.Format
	body string
}

func nativeCrossProtocolRequests() []nativeProtocolRequest {
	return []nativeProtocolRequest{
		{"chat", translator.FormatOpenAI, `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hello"}]}`},
		{"claude", translator.FormatClaude, `{"model":"gpt-6-luna","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`},
		{"gemini", translator.FormatGemini, `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`},
	}
}

func assertNativeCrossProtocolInput(t *testing.T, body []byte) {
	t.Helper()
	input := ojson.GetBytes(body, "input")
	if !input.IsArray() || !strings.Contains(input.Raw, `"hello"`) {
		t.Fatalf("Responses input lost the user message: %s", body)
	}
	for _, key := range []string{"messages", "contents", "max_tokens", "prompt_cache_key"} {
		if ojson.GetBytes(body, key).Exists() {
			t.Errorf("native request unexpectedly contains %s: %s", key, body)
		}
	}
}

func TestNativeResponsesCrossProtocolHTTP(t *testing.T) {
	for _, tc := range nativeCrossProtocolRequests() {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+"/"+map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
				var captured []byte
				rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					captured, _ = io.ReadAll(r.Body)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + nativeCacheCompleted + "\n\n")), Request: r}, nil
				})
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", rt)
				e := NewCodexExecutor(&config.Config{})
				req := exec.Request{Model: "gpt-6-luna", Payload: []byte(tc.body)}
				opts := exec.Options{SourceFormat: tc.from, Stream: stream}
				if stream {
					response, err := e.ExecuteStream(ctx, nativeAPIAuth(), req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range response.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := e.Execute(ctx, nativeAPIAuth(), req, opts); err != nil {
					t.Fatal(err)
				}
				assertNativeCrossProtocolInput(t, captured)
			})
		}
	}
}
