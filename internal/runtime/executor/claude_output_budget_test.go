package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutorOutputBudget(t *testing.T) {
	const reply = `{"id":"msg_budget","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	const events = `data: {"type":"message_start","message":{"id":"msg_budget","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

data: {"type":"message_stop"}

`
	for _, tc := range []struct {
		name, model, fields string
		format              tr.Format
		want                int64
	}{
		{"responses default", "claude-opus-4-6", `"input":"hello"`, tr.FormatOpenAIResponse, 64000},
		{"responses smaller model", "claude-3-5-haiku-20241022", `"input":"hello"`, tr.FormatOpenAIResponse, 8192},
		{"responses explicit smaller", "claude-opus-4-6", `"input":"hello","max_output_tokens":4096`, tr.FormatOpenAIResponse, 4096},
		{"responses explicit larger", "claude-opus-4-6", `"input":"hello","max_output_tokens":128000`, tr.FormatOpenAIResponse, 128000},
		{"chat default", "claude-opus-4-6", `"messages":[{"role":"user","content":"hello"}]`, tr.FormatOpenAI, 64000},
		{"chat smaller model", "claude-3-5-haiku-20241022", `"messages":[{"role":"user","content":"hello"}]`, tr.FormatOpenAI, 8192},
		{"chat explicit smaller", "claude-opus-4-6", `"messages":[{"role":"user","content":"hello"}],"max_tokens":4096`, tr.FormatOpenAI, 4096},
		{"chat explicit larger", "claude-opus-4-6", `"messages":[{"role":"user","content":"hello"}],"max_completion_tokens":128000`, tr.FormatOpenAI, 128000},
		{"native messages", "claude-opus-4-6", `"messages":[{"role":"user","content":"hello"}],"max_tokens":4096`, tr.FormatClaude, 4096},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				seen := make(chan []byte, 1)
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					seen <- body
					response, contentType := reply, "application/json"
					if gjson.GetBytes(body, "stream").Bool() {
						response, contentType = events, "text/event-stream"
					}
					response = strings.ReplaceAll(response, "claude-opus-4-6", tc.model)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
				})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport))
				auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "synthetic-output-budget", "base_url": "https://api.anthropic.com", "cloak_mode": "never"}}
				body := []byte(fmt.Sprintf(`{"model":%q,"stream":%t,%s}`, tc.model, stream, tc.fields))
				req := coreexecutor.Request{Model: tc.model, Payload: body}
				opts := coreexecutor.Options{SourceFormat: tc.format, OriginalRequest: body, Stream: stream}
				exec := NewClaudeExecutor(&config.Config{})
				if stream {
					result, err := exec.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := exec.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
				select {
				case wire := <-seen:
					if got := gjson.GetBytes(wire, "max_tokens").Int(); got != tc.want {
						t.Fatalf("upstream max_tokens = %d, want %d", got, tc.want)
					}
				default:
					t.Fatal("upstream request missing")
				}
			})
		}
	}
}
