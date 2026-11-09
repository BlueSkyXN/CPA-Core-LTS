package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	responses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const emptyCopilotUsage = `{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7}}`

func TestNativeEmptyStreamRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, ending := range []string{"done", "eof", "stop", "json"} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, ending), func(t *testing.T) {
				var calls atomic.Int32
				var host *copilotFixtureHost
				runtime, fixture := copilotFixture(t, func(w http.ResponseWriter, req *http.Request) {
					switch req.URL.Path {
					case "/copilot_internal/v2/token":
						copilotTokenFixture(w)
					case "/models":
						serveCopilotModels(w)
					case "/chat/completions":
						attempt := calls.Add(1)
						if attempt == 1 && ending == "json" {
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprint(w, emptyCopilotUsage)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if attempt == 1 {
							fmt.Fprintf(w, "data: %s\n\n", emptyCopilotUsage)
							if ending == "stop" {
								fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
							}
							if ending != "eof" {
								fmt.Fprint(w, "data: [DONE]\n\n")
							}
							return
						}
						host.mu.Lock()
						emitted := len(host.emitted)
						host.mu.Unlock()
						if emitted != 0 {
							t.Error("empty attempt committed downstream payload before retry")
						}
						fmt.Fprint(w, "data: {\"choices\":[{\"message\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"reasoning_tokens\":2,\"total_tokens\":16}}\n\ndata: [DONE]\n\n")
					default:
						t.Errorf("unexpected path %s", req.URL.Path)
					}
				})
				host = fixture
				req := copilotReq("recover", stream)
				var output []byte
				if stream {
					if _, err := runtime.executeNativeStream(req); err != nil {
						t.Fatal(err)
					}
					if closed := waitCopilotClose(t, host); closed.Error != "" {
						t.Fatalf("close = %+v", closed)
					}
					var param any
					var events [][]byte
					host.mu.Lock()
					frames := append([][]byte(nil), host.emitted...)
					host.mu.Unlock()
					frames = append(frames, []byte("data: [DONE]"))
					for _, frame := range frames {
						events = append(events, responses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), req.Model, req.Payload, req.Payload, frame, &param)...)
					}
					if terminalCount := bytes.Count(bytes.Join(events, nil), []byte("event: response.completed\n")); terminalCount != 1 {
						t.Fatalf("completed count = %d", terminalCount)
					}
					output = bytes.Join(frames, nil)
				} else {
					response, err := runtime.executeNative(req)
					if err != nil {
						t.Fatal(err)
					}
					output = response.Payload
				}
				if calls.Load() != 2 {
					t.Fatalf("upstream calls = %d, want 2", calls.Load())
				}
				for _, want := range []string{"recovered", `"prompt_tokens":18`, `"completion_tokens":5`, `"reasoning_tokens":2`, `"total_tokens":23`} {
					if !bytes.Contains(output, []byte(want)) {
						t.Errorf("output missing %s", want)
					}
				}
				assertNativeReleased(t, runtime, host)
			})
		}
	}
}

func TestNativeTerminalBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		calls            int32
	}{
		{"persistent_empty", "data: " + emptyCopilotUsage + "\n\ndata: [DONE]\n\n", "empty_upstream_response", 2},
		{"persistent_eof", "data: " + emptyCopilotUsage + "\n\n", "empty_upstream_response", 2},
		{"bare_done", "data: [DONE]\n\n", "empty_upstream_response", 2},
		{"text_then_eof", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "stream_truncated", 1},
		{"reasoning_then_eof", "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"partial\"}}]}\n\n", "stream_truncated", 1},
		{"tool_then_eof", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]}}]}\n\n", "stream_truncated", 1},
		{"missing_tool_calls", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", "invalid_upstream_response", 1},
		{"empty_length", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", "", 1},
		{"content_filter", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			runtime, host := copilotFixture(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/copilot_internal/v2/token":
					copilotTokenFixture(w)
				case "/models":
					serveCopilotModels(w)
				case "/chat/completions":
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, tc.body)
				}
			})
			if _, err := runtime.executeNativeStream(copilotReq(tc.name, true)); err != nil {
				t.Fatal(err)
			}
			closed := waitCopilotClose(t, host)
			if closed.ErrorCode != tc.code || calls.Load() != tc.calls {
				t.Fatalf("close = %+v, calls = %d", closed, calls.Load())
			}
			if tc.code != "" {
				for _, frame := range host.emitted {
					if ojson.GetBytes(frame, "choices.0.finish_reason").String() != "" {
						t.Fatal("failure emitted successful terminal")
					}
				}
			}
			assertNativeReleased(t, runtime, host)
		})
	}
}

func assertNativeReleased(t *testing.T, runtime *pluginRuntime, host *copilotFixtureHost) {
	t.Helper()
	runtime.mu.Lock()
	active, sessions := len(runtime.nativeActive), len(runtime.nativeSessions)
	runtime.mu.Unlock()
	host.mu.Lock()
	defer host.mu.Unlock()
	if active != 0 || sessions != 0 || len(host.streams) != 0 {
		t.Fatal("native execution leaked ownership or HTTP stream")
	}
	for _, count := range host.closeCounts {
		if count != 1 {
			t.Fatal("HTTP stream closed more than once")
		}
	}
}

func TestNativeRequestPreservesReasoningEffortAndToolChoice(t *testing.T) {
	for _, choice := range []string{`"none"`, `"auto"`, `"required"`, `{"type":"function","function":{"name":"lookup"}}`} {
		req := copilotReq("fields", true).ExecutorRequest
		req.Payload = []byte(`{"messages":[{"role":"user","content":"fixture"}],"reasoning_effort":"high","tool_choice":` + choice + `}`)
		payload, err := nativeRequestPayload(req)
		if err != nil {
			t.Fatal(err)
		}
		if ojson.GetBytes(payload, "reasoning_effort").String() != "high" || !reflect.DeepEqual(ojson.GetBytes(payload, "tool_choice").Value(), ojson.Parse(choice).Value()) {
			t.Fatal("request controls changed")
		}
	}
}

func TestNativeReasoningUsageCumulative(t *testing.T) {
	decoder := newNativeDecoder("usage", "gemini-4-argon", nil)
	frame := []byte(`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3,"reasoning_tokens":2,"total_tokens":16}}`)
	for i := 0; i < 2; i++ {
		if _, err := decoder.data(frame); err != nil {
			t.Fatal(err)
		}
	}
	usage := chatUsage(decoder.projection.usage)
	if usage["completion_tokens"] != int64(5) || usage["total_tokens"] != int64(16) {
		t.Fatal("cumulative reasoning counted twice or lost")
	}
	if _, err := decoder.data([]byte(`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16,"completion_tokens_details":{"reasoning_tokens":2}}}`)); err != nil {
		t.Fatal(err)
	}
	if *decoder.projection.usage.OutputTokens != 5 {
		t.Fatal("subset reasoning was added twice")
	}
}

func TestNativeCancelDuringEmptyRetry(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	runtime, host := copilotFixture(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/copilot_internal/v2/token":
			copilotTokenFixture(w)
		case "/models":
			serveCopilotModels(w)
		case "/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			if calls.Add(1) == 1 {
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			w.(http.Flusher).Flush()
			close(started)
			<-req.Context().Done()
		}
	})
	req := copilotReq("cancel-retry", true)
	if _, err := runtime.executeNativeStream(req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("empty retry did not start")
	}
	if _, err := runtime.executeNativeStream(copilotReq("conflict-during-retry", true)); err == nil {
		t.Fatal("retry released session ownership")
	}
	runtime.cancelNative(pluginapi.CancelExecutionRequest{RequestID: req.RequestID, CallerScope: req.CallerScope})
	if closed := waitCopilotClose(t, host); closed.ErrorCode != "request_cancelled" {
		t.Fatalf("close = %+v", closed)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
	assertNativeReleased(t, runtime, host)
}
