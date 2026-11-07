package executor

import (
	"context"
	"errors"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestKimiResponsesDelegationBeforeTransport(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, opaque := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/opaque=%t", stream, opaque), func(t *testing.T) {
				calls := 0
				var sent []byte
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					var err error
					sent, err = io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					response := `{"id":"resp-task","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
					contentType := "application/json"
					if stream {
						contentType = "text/event-stream"
						response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response))}, nil
				}))
				cfg := &config.Config{SDKConfig: config.SDKConfig{Client: config.ClientConfig{Codex: config.CodexClientConfig{OptimizeMultiAgentV2: true}}}}
				executor := NewKimiExecutor(cfg)
				content := `[{"type":"input_text","text":"perform the delegated task"}]`
				if opaque {
					content = `[{"type":"input_text","text":"NEW_TASK"},{"type":"encrypted_content","encrypted_content":"opaque-kimi-fixture"}]`
				}
				req := coreexecutor.Request{Model: "kimi-k3", Payload: []byte(`{"input":[{"type":"agent_message","content":` + content + `}]}`)}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream, Headers: http.Header{"User-Agent": []string{"Codex Desktop/0.163.0"}}}
				auth := &coreauth.Auth{Metadata: map[string]any{"access_token": "synthetic-kimi-token"}}
				var err error
				if stream {
					var result *coreexecutor.StreamResult
					result, err = executor.ExecuteStream(ctx, auth, req, opts)
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
				} else {
					_, err = executor.Execute(ctx, auth, req, opts)
				}
				if opaque {
					var refusal *translatorcommon.UnconvertedAgentMessageError
					if !errors.As(err, &refusal) || calls != 0 {
						t.Fatalf("unreadable task reached transport: calls=%d err=%v", calls, err)
					}
					return
				}
				if err != nil || calls != 1 || ojson.GetBytes(sent, "input.0.role").String() != "user" || ojson.GetBytes(sent, "input.0.content.0.text").String() != "perform the delegated task" {
					t.Fatalf("Kimi did not receive the task as a user turn: calls=%d err=%v body=%s", calls, err, sent)
				}
			})
		}
	}
}
