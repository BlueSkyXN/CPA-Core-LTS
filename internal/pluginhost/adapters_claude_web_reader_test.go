package pluginhost

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAnthropicPluginWebReaderStreamAndUsage(t *testing.T) {
	lines := []string{
		`{"type":"message_start","message":{"type":"message","id":"reader","role":"assistant","content":[],"usage":{"input_tokens":11,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"call_reader","name":"webReader","input":{"url":"https://example.com/doc"}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_result","tool_use_id":"call_reader","content":"[{\"url\":\"https://example.com/doc\",\"content\":\"网页正文\"}]"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Answer"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	}
	for _, tc := range []struct {
		name      string
		split     int
		truncated bool
	}{{name: "whole"}, {name: "bytes", split: 1}, {name: "truncated", split: 7, truncated: true}} {
		t.Run(tc.name, func(t *testing.T) {
			selected := lines
			if tc.truncated {
				selected = lines[:len(lines)-1]
			}
			body := "data: " + strings.Join(selected, "\n\ndata: ") + "\n\n"
			input := make(chan pluginapi.ExecutorStreamChunk, len(body)+1)
			for len(body) > 0 {
				n := len(body)
				if tc.split > 0 {
					n = min(n, tc.split)
				}
				input <- pluginapi.ExecutorStreamChunk{Payload: []byte(body[:n])}
				body = body[n:]
			}
			close(input)
			var calls atomic.Int32
			adapter := newCurrentExecutorAdapterForTest(New(), "reader-fixture", &fakeExecutor{executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				calls.Add(1)
				return pluginapi.ExecutorStreamResponse{Chunks: input}, nil
			}}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
			authID := t.Name()
			records := make(chan coreusage.Record, 8)
			coreusage.RegisterNamedPlugin("test:webreader-usage", coreUsagePluginFunc(func(_ context.Context, r coreusage.Record) {
				if r.AuthID == authID && r.Provider == "plugin-provider" {
					records <- r
				}
			}))
			t.Cleanup(func() { coreusage.RegisterNamedPlugin("test:webreader-usage", noopFormalPluginUsageSink{}) })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			response, err := adapter.ExecuteStream(ctx, &coreauth.Auth{ID: authID, Provider: "plugin-provider"}, coreexecutor.Request{Model: "fixture", Payload: []byte(`{"input":"read"}`)}, coreexecutor.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			var streamErr error
			for chunk := range response.Chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if (streamErr != nil) != tc.truncated || calls.Load() != 1 {
				t.Fatalf("truncated=%t err=%v dispatches=%d", tc.truncated, streamErr, calls.Load())
			}
			var completed gjson.Result
			terminals := 0
			for _, line := range strings.Split(output.String(), "\n") {
				if strings.HasPrefix(line, "data:") {
					event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
					if event.Get("type").String() == "response.completed" {
						terminals++
						completed = event.Get("response")
					}
				}
			}
			if tc.truncated {
				if terminals != 0 {
					t.Fatal("truncated reader reported success")
				}
			} else {
				if terminals != 1 || completed.Get("output.0.action.type").String() != "open_page" || !strings.Contains(completed.Get("output.0.results.content").String(), "网页正文") || completed.Get("output.1.content.0.text").String() != "Answer" {
					t.Fatalf("reader response lost: %s", completed.Raw)
				}
			}
			select {
			case record := <-records:
				if record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 7 || record.Failed != tc.truncated {
					t.Fatalf("usage changed: %+v", record)
				}
			case <-ctx.Done():
				t.Fatal("no usage record")
			}
			select {
			case <-records:
				t.Fatal("duplicate usage record")
			default:
			}
		})
	}
}
