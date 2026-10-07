package pluginhost

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const claudePluginFixture = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"id\":\"fixture\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"你好\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\ndata: \"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestAnthropicPluginResponsesStream(t *testing.T) {
	var baseline []string
	for _, tc := range []struct {
		name      string
		split     int
		crlf      bool
		truncated bool
	}{
		{name: "whole"}, {name: "bytes", split: 1}, {name: "random", split: 17}, {name: "crlf", split: 1, crlf: true}, {name: "cr", split: 1}, {name: "data-only", split: 17}, {name: "duplicate"}, {name: "ping", split: 1}, {name: "truncated", split: 17, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := claudePluginFixture
			if tc.name == "cr" {
				body = strings.ReplaceAll(body, "\n", "\r")
			}
			if tc.name == "duplicate" {
				body += "data: {\"type\":\"message_stop\"}\n\ndata: broken-json\n\n"
			}
			if tc.name == "ping" {
				body = ":heartbeat\n\ndata: {\"type\":\"ping\"}\n\ndata: {\"type\":\"future_event\"}\n\n" + body
			}
			if tc.name == "data-only" {
				var lines []string
				for _, line := range strings.Split(body, "\n") {
					if !strings.HasPrefix(line, "event:") {
						lines = append(lines, line)
					}
				}
				body = strings.Join(lines, "\n")
			}
			if tc.truncated {
				body = strings.Split(body, "event: message_stop")[0]
			}
			if tc.crlf {
				body = strings.ReplaceAll(body, "\n", "\r\n")
			}
			input := make(chan pluginapi.ExecutorStreamChunk, len(body)+1)
			rng := rand.New(rand.NewSource(42))
			for len(body) > 0 {
				n := len(body)
				if tc.split > 0 {
					n = min(n, 1+rng.Intn(tc.split))
				}
				input <- pluginapi.ExecutorStreamChunk{Payload: []byte(body[:n])}
				body = body[n:]
			}
			close(input)
			adapter := newCurrentExecutorAdapterForTest(New(), "claude-fixture", &fakeExecutor{executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				return pluginapi.ExecutorStreamResponse{Chunks: input}, nil
			}}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
			records := make(chan coreusage.Record, 8)
			coreusage.RegisterNamedPlugin("test:claude-stream", coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
				if record.Provider == "plugin-provider" {
					records <- record
				}
			}))
			t.Cleanup(func() { coreusage.RegisterNamedPlugin("test:claude-stream", noopFormalPluginUsageSink{}) })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := adapter.ExecuteStream(ctx, &coreauth.Auth{ID: "fixture-auth", Provider: "plugin-provider"}, coreexecutor.Request{Model: "fixture", Payload: []byte(`{"input":"hello"}`)}, coreexecutor.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			var streamErr error
			for chunk := range result.Chunks {
				out.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if !strings.Contains(out.String(), "你好") {
				t.Error("answer lost")
			}
			if tc.truncated {
				if streamErr == nil {
					t.Error("truncated stream accepted")
				}
			} else if streamErr != nil || strings.Count(out.String(), "event: response.completed\n") != 1 {
				t.Errorf("terminal mismatch: %v", streamErr)
			}
			if !tc.truncated {
				var normalized []string
				for _, line := range strings.Split(out.String(), "\n") {
					if !strings.HasPrefix(line, "data:") {
						continue
					}
					data := []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
					data, _ = sjson.DeleteBytes(data, "response.created_at")
					normalized = append(normalized, string(data))
				}
				if baseline == nil {
					baseline = normalized
				} else if !reflect.DeepEqual(baseline, normalized) {
					t.Error("chunking changed response semantics")
				}
				last := gjson.Parse(normalized[len(normalized)-1])
				if last.Get("response.usage.output_tokens").Int() != 7 {
					t.Error("client usage lost")
				}
			}
			select {
			case record := <-records:
				if record.Detail.InputTokens != 3 || record.Detail.OutputTokens != 7 || record.Failed != tc.truncated {
					t.Errorf("usage = %+v failed=%v", record.Detail, record.Failed)
				}
			case <-ctx.Done():
				t.Fatal("no usage record")
			}
		})
	}
}

func TestAnthropicPluginResponsesUpstreamFailureKeepsUsage(t *testing.T) {
	upstreamErr := &coreauth.Error{Code: "upstream_error", Message: "synthetic overload", HTTPStatus: 503}
	in := make(chan pluginapi.ExecutorStreamChunk, 2)
	in <- pluginapi.ExecutorStreamChunk{Payload: []byte(strings.Split(claudePluginFixture, "event: message_stop")[0])}
	in <- pluginapi.ExecutorStreamChunk{Err: upstreamErr}
	close(in)
	adapter := newCurrentExecutorAdapterForTest(New(), "upstream-fixture", &fakeExecutor{executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
		return pluginapi.ExecutorStreamResponse{Chunks: in}, nil
	}}, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude})
	records := make(chan coreusage.Record, 4)
	coreusage.RegisterNamedPlugin("test:upstream-usage", coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
		if record.Provider == "plugin-provider" {
			records <- record
		}
	}))
	t.Cleanup(func() { coreusage.RegisterNamedPlugin("test:upstream-usage", noopFormalPluginUsageSink{}) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := adapter.ExecuteStream(ctx, &coreauth.Auth{ID: "upstream-auth", Provider: "plugin-provider"}, coreexecutor.Request{Model: "fixture", Payload: []byte(`{"input":"hello"}`)}, coreexecutor.Options{Stream: true, SourceFormat: tr.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	var got error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			got = chunk.Err
		}
	}
	if !errors.Is(got, upstreamErr) {
		t.Fatalf("upstream classification replaced: %v", got)
	}
	select {
	case record := <-records:
		if !record.Failed || record.Detail.OutputTokens != 7 {
			t.Error("failure lost observed usage")
		}
	case <-ctx.Done():
		t.Fatal("no usage")
	}
}
