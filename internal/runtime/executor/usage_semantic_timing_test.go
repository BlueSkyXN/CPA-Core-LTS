package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type timingUsageSink struct{ ch chan usage.Record }

func (s *timingUsageSink) HandleUsage(_ context.Context, r usage.Record) {
	select {
	case s.ch <- r:
	default:
	}
}
func newTimingUsageSink() *timingUsageSink {
	s := &timingUsageSink{make(chan usage.Record, 32)}
	usage.RegisterPlugin(s)
	return s
}
func waitTimingUsageRecord(t *testing.T, s *timingUsageSink, id string) usage.Record {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case r := <-s.ch:
			if r.AuthID == id {
				return r
			}
		case <-timer.C:
			t.Fatal("missing usage record")
			return usage.Record{}
		}
	}
}
func drainTimingStream(t *testing.T, r *core.StreamResult) {
	t.Helper()
	for c := range r.Chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
	}
}
func TestClaudeStreamSemanticTimingAfterDecompression(t *testing.T) {
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"audit\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"think\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "gzip"}[compressed], func(t *testing.T) {
			wire := []byte(body)
			if compressed {
				var b bytes.Buffer
				w := gzip.NewWriter(&b)
				_, _ = w.Write(wire)
				_ = w.Close()
				wire = b.Bytes()
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if compressed {
					w.Header().Set("Content-Encoding", "gzip")
				}
				_, _ = w.Write(wire)
			}))
			defer server.Close()
			sink := newTimingUsageSink()
			id := t.Name()
			e := NewClaudeExecutor(&config.Config{})
			result, err := e.ExecuteStream(context.Background(), &auth.Auth{ID: id, Attributes: map[string]string{"api_key": "audit-dummy", "base_url": server.URL}}, core.Request{Model: "claude-3-5-sonnet-20241022", Payload: []byte(`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)}, core.Options{SourceFormat: tr.FormatClaude, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			drainTimingStream(t, result)
			r := waitTimingUsageRecord(t, sink, id)
			t.Logf("compressed=%v failed=%v ttfb=%v ttft=%v ttfa=%v", compressed, r.Failed, r.TTFB, r.TTFT, r.TTFA)
			if r.Failed || r.TTFB <= 0 || r.TTFT < r.TTFB || r.TTFA < r.TTFB {
				t.Fatalf("invalid semantic timing: version=%d first=%v reasoning=%v answer=%v", r.TimingVersion, r.TTFB, r.TTFT, r.TTFA)
			}
			if r.Size.UpstreamResponseBytes != int64(len(wire)) {
				t.Fatalf("response bytes=%d want wire bytes=%d", r.Size.UpstreamResponseBytes, len(wire))
			}
		})
	}
}
func TestKimiChatStreamSemanticTiming(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\",\"content\":\"answer\"}}]}\n\n" + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n" + "data: [DONE]\n\n"
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}))
	sink := newTimingUsageSink()
	id := t.Name()
	e := NewKimiExecutor(&config.Config{})
	result, err := e.ExecuteStream(ctx, &auth.Auth{ID: id, Metadata: map[string]any{"access_token": "audit-dummy"}}, core.Request{Model: "kimi-k3", Payload: []byte(`{"model":"kimi-k3","stream":true,"messages":[{"role":"user","content":"hi"}]}`)}, core.Options{SourceFormat: tr.FormatOpenAI, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	drainTimingStream(t, result)
	r := waitTimingUsageRecord(t, sink, id)
	t.Logf("failed=%v ttfb=%v ttft=%v ttfa=%v", r.Failed, r.TTFB, r.TTFT, r.TTFA)
	if r.Failed || r.TTFB <= 0 || r.TTFT < r.TTFB || r.TTFA < r.TTFB {
		t.Fatalf("missing semantic timing: first=%v reasoning=%v answer=%v", r.TTFB, r.TTFT, r.TTFA)
	}
}

func TestKimiResponsesStreamSemanticTiming(t *testing.T) {
	body := "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"think\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"timing\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}))
	sink := newTimingUsageSink()
	id := t.Name()
	result, err := NewKimiExecutor(&config.Config{}).ExecuteStream(ctx, &auth.Auth{ID: id, Metadata: map[string]any{"access_token": "test-token"}}, core.Request{Model: "kimi-k3", Payload: []byte(`{"model":"kimi-k3","stream":true,"input":[]}`)}, core.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	drainTimingStream(t, result)
	r := waitTimingUsageRecord(t, sink, id)
	if r.Failed || r.TTFB <= 0 || r.TTFT < r.TTFB || r.TTFA < r.TTFB {
		t.Fatalf("missing semantic timing: first=%v reasoning=%v answer=%v", r.TTFB, r.TTFT, r.TTFA)
	}
}

func TestCodexDuplexSemanticTimingPerExplicitRequest(t *testing.T) {
	const delay = 40 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for i := 1; i <= 3; i++ {
			if i < 3 {
				if _, _, err = conn.ReadMessage(); err != nil {
					return
				}
				time.Sleep(delay)
			}
			for _, p := range []string{
				fmt.Sprintf(`{"type":"response.created","response":{"id":"timing_%d","output":[]}}`, i),
				`{"type":"response.reasoning_summary_text.delta","delta":"think"}`,
				`{"type":"response.output_text.delta","delta":"answer"}`,
				fmt.Sprintf(`{"type":"response.completed","response":{"id":"timing_%d","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`, i),
			} {
				if err = conn.WriteMessage(websocket.TextMessage, []byte(p)); err != nil {
					return
				}
			}
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := make(chan core.WebsocketInput, 1)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
	e := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
	e.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	sink := newTimingUsageSink()
	id := t.Name()
	result, err := e.ExecuteStream(ctx, &auth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "websockets": "true"}}, core.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)}, core.Options{SourceFormat: tr.FormatCodex, Stream: true, Metadata: map[string]any{core.ExecutionSessionMetadataKey: id}})
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for c := range result.Chunks {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		if bytes.Contains(c.Payload, []byte("response.completed")) {
			completed++
			if completed == 1 {
				input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"gpt-6-astra","input":[]}`)}
			}
			if completed == 3 {
				cancel()
				break
			}
		}
	}
	if completed != 3 {
		t.Fatalf("completed=%d want 3", completed)
	}
	for i := 1; i <= 3; i++ {
		r := waitTimingUsageRecord(t, sink, id)
		if r.Failed {
			t.Fatalf("response %d failed", i)
		}
		if i < 3 {
			if r.TTFB < delay || r.TTFT < r.TTFB || r.TTFA < r.TTFB {
				t.Fatalf("response %d timing first=%v reasoning=%v answer=%v", i, r.TTFB, r.TTFT, r.TTFA)
			}
		} else if r.TTFB != 0 || r.TTFT != 0 || r.TTFA != 0 {
			t.Fatal("automatic successor invented a dispatch timestamp")
		}
	}
}
