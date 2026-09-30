package pluginhost

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type anthropicTestHooks struct {
	mu             sync.Mutex
	before, after  int
	chat           bool
	badShape       bool
	removeThinking bool
}

func (h *anthropicTestHooks) NormalizeRequest(_ context.Context, _, _ tr.Format, _ string, body []byte, _ bool) []byte {
	out, _ := sjson.DeleteBytes(body, "messages.1.content.0")
	if h.removeThinking {
		out, _ = sjson.DeleteBytes(out, "thinking")
	}
	return out
}
func (*anthropicTestHooks) TranslateRequest(_ context.Context, _, _ tr.Format, _ string, body []byte, _ bool) ([]byte, bool) {
	return body, false
}
func (h *anthropicTestHooks) NormalizeResponseBefore(_ context.Context, _, _ tr.Format, _ string, _, _, body []byte, stream bool) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.before++
	if stream {
		if !bytes.HasPrefix(body, []byte("data: ")) {
			h.badShape = true
		}
		return bytes.ReplaceAll(body, []byte("你好"), []byte("normalized"))
	}
	if string(body) == "normalizer-input" {
		return []byte(`{"type":"message","id":"hook","role":"assistant","content":[{"type":"text","text":"normalized"}],"stop_reason":"end_turn"}`)
	}
	return body
}
func (*anthropicTestHooks) TranslateResponse(_ context.Context, _, _ tr.Format, _ string, _, _, body []byte, _ bool) ([]byte, bool) {
	return body, false
}
func (h *anthropicTestHooks) NormalizeResponseAfter(_ context.Context, _, _ tr.Format, _ string, _, _, body []byte, stream bool) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.after++
	wantObject := "response"
	if h.chat {
		wantObject = "chat.completion"
	}
	if !stream && gjson.GetBytes(body, "object").String() != wantObject {
		h.badShape = true
	}
	return body
}

func TestAnthropicPluginOpenAIHooks(t *testing.T) {
	for _, source := range []tr.Format{tr.FormatOpenAIResponse, tr.FormatOpenAI} {
		for _, stream := range []bool{false, true} {
			hooks := &anthropicTestHooks{chat: source == tr.FormatOpenAI}
			tr.SetPluginHooks(hooks)
			t.Cleanup(func() { tr.SetPluginHooks(nil) })
			adapter := newCurrentExecutorAdapterForTest(New(), "hook-fixture", &fakeExecutor{
				execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					return pluginapi.ExecutorResponse{Payload: []byte("normalizer-input")}, nil
				},
				executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					in := make(chan pluginapi.ExecutorStreamChunk, 1)
					in <- pluginapi.ExecutorStreamChunk{Payload: []byte(claudePluginFixture)}
					close(in)
					return pluginapi.ExecutorStreamResponse{Chunks: in}, nil
				},
			}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
			req := coreexecutor.Request{Model: "fixture", Payload: []byte(`{"input":"hello"}`)}
			opts := coreexecutor.Options{SourceFormat: source, Stream: stream}
			var output strings.Builder
			if stream {
				result, err := adapter.ExecuteStream(context.Background(), nil, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for c := range result.Chunks {
					if c.Err != nil {
						t.Fatal(c.Err)
					}
					output.Write(c.Payload)
				}
			} else {
				result, err := adapter.Execute(context.Background(), nil, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				output.Write(result.Payload)
			}
			if !strings.Contains(output.String(), "normalized") {
				t.Error("before hook did not affect translation")
			}
			hooks.mu.Lock()
			if hooks.badShape || (!stream && (hooks.before != 1 || hooks.after != 1)) || (stream && hooks.before != 6) {
				t.Errorf("hook counts before=%d after=%d shape=%v", hooks.before, hooks.after, hooks.badShape)
			}
			hooks.mu.Unlock()
			tr.SetPluginHooks(nil)
		}
	}
}
