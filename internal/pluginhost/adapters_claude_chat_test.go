package pluginhost

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAnthropicPluginChatStreamFrames(t *testing.T) {
	body := strings.ReplaceAll(claudePluginFixture, `"type":"text","text":""`, `"type":"thinking","thinking":"初始"`)
	body = strings.ReplaceAll(body, `"type":"text_delta","text":"你好"`, `"type":"thinking_delta","thinking":"思考"`)
	for _, split := range []int{1, 17, len(body)} {
		for _, truncated := range []bool{false, true} {
			t.Run(fmt.Sprintf("split=%d/truncated=%v", split, truncated), func(t *testing.T) {
				wire := strings.ReplaceAll(body, "\n", "\r\n")
				if truncated {
					wire = strings.Split(wire, "event: message_stop")[0]
				}
				in := make(chan pluginapi.ExecutorStreamChunk, len(wire))
				for len(wire) > 0 {
					n := min(split, len(wire))
					in <- pluginapi.ExecutorStreamChunk{Payload: []byte(wire[:n])}
					wire = wire[n:]
				}
				close(in)
				adapter := newCurrentExecutorAdapterForTest(New(), "chat-stream-fixture", &fakeExecutor{executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					return pluginapi.ExecutorStreamResponse{Chunks: in}, nil
				}}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				result, err := adapter.ExecuteStream(ctx, nil, coreexecutor.Request{Model: "fixture", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}, coreexecutor.Options{SourceFormat: tr.FormatOpenAI, Stream: true})
				if err != nil {
					t.Fatal(err)
				}
				var thought, text string
				var gotError bool
				var trailing int
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						gotError = true
						continue
					}
					thought += gjson.GetBytes(chunk.Payload, "choices.0.delta.reasoning_content").String()
					text += gjson.GetBytes(chunk.Payload, "choices.0.delta.content").String()
					if gjson.GetBytes(chunk.Payload, "choices.#").Int() == 0 && gjson.GetBytes(chunk.Payload, "usage.total_tokens").Int() == 10 {
						trailing++
					}
				}
				if gotError != truncated || thought != "初始思考" || text != "" || (!truncated && trailing != 1) {
					t.Fatalf("stream result thought=%q text=%q error=%v trailing=%d", thought, text, gotError, trailing)
				}
			})
		}
	}
}
