package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	claudeapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	openaiapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
)

type carbonBudgetWire struct {
	t     *testing.T
	omit  int
	calls atomic.Int32
}

func (f *carbonBudgetWire) serve(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	var body struct {
		Model    string
		Messages []struct {
			Role    string
			Content any
			Calls   []any  `json:"tool_calls"`
			ID      string `json:"tool_call_id"`
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Error(err)
		return
	}
	var urls []string
	ids := map[string]bool{}
	omitted := 0
	for _, m := range body.Messages {
		if m.Role == "tool" {
			if m.ID == "" || ids[m.ID] {
				f.t.Error("missing/duplicated tool result ID")
			}
			ids[m.ID] = true
		}
		parts, _ := m.Content.([]any)
		for _, v := range parts {
			p, _ := v.(map[string]any)
			if p["text"] == carbonImageOmitted {
				omitted++
			}
			if p["type"] == "image_url" {
				if m.Role != "user" {
					f.t.Error("image in non-visual role")
				}
				im, _ := p["image_url"].(map[string]any)
				u, _ := im["url"].(string)
				urls = append(urls, u)
			}
		}
	}
	if body.Model != carbonImageModel || len(urls) != 10 || omitted != 1 || len(ids) != 14 {
		f.t.Errorf("unexpected Chat shape: images=%d omissions=%d paired IDs=%d", len(urls), omitted, len(ids))
	}
	if f.omit == 0 {
		f.t.Error("rejected history reached inference")
	}
	for i := range urls {
		n := i + 1
		if n >= f.omit {
			n++
		}
		if urls[i] != fmt.Sprintf("https://example.com/fixture-%02d.png", n) {
			f.t.Error("wrong source/order for image", n)
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", matrixFinalAnswer)
}

func testCarbonBudgetHandlers(t *testing.T, base *handlers.BaseAPIHandler, slot *atomic.Pointer[carbonBudgetWire], sink *copilotUsageSink, authIndex string) {
	t.Helper()
	router := gin.New()
	router.POST("/v1/chat/completions", openaiapi.NewOpenAIAPIHandler(base).ChatCompletions)
	router.POST("/v1/responses", openaiapi.NewOpenAIResponsesAPIHandler(base).Responses)
	router.POST("/v1/messages", claudeapi.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	for _, p := range []struct{ format, path string }{{"openai", "/v1/chat/completions"}, {"openai-response", "/v1/responses"}, {"claude", "/v1/messages"}} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name, instruction string
				batches           []int
				omit              int
			}{
				{name: "answered history", batches: []int{5, 2, 4}, omit: 5},
				{name: "all current", batches: []int{11}},
				{name: "unresolved old reference", batches: []int{5, 2, 4}, instruction: "Read the new files and compare with the fifth image from the first batch."},
				{name: "exact Markdown reference", batches: []int{5, 2, 4}, instruction: "Read the new files and compare them with [my reference](https://example.com/fixture-05.png).", omit: 4},
				{name: "additional unresolved reference", batches: []int{5, 2, 4}, instruction: "Compare with [my reference](https://example.com/fixture-05.png) and the one before it."},
			} {
				t.Run(fmt.Sprintf("carbon/%s/stream=%t/%s", p.format, stream, tc.name), func(t *testing.T) {
					f := &carbonBudgetWire{t: t, omit: tc.omit}
					slot.Store(f)
					defer slot.Store(nil)
					req := budgetFixture(t, p.format, tc.batches, "")
					var body matrixObject
					if err := json.Unmarshal(req.OriginalRequest, &body); err != nil {
						t.Fatal(err)
					}
					if tc.instruction != "" {
						setCarbonWireInstruction(t, p.format, body, tc.instruction)
					}
					body["stream"] = stream
					rec := matrixPost(t, router, p.path, fmt.Sprintf("carbon-%s-%t-%s", p.format, stream, tc.name), body)
					refused := tc.omit == 0
					wantCalls, wantTokens := int32(0), int64(0)
					if refused {
						if rec.Code != 400 || !strings.Contains(rec.Body.String(), "limit 10") {
							t.Fatalf("refusal status=%d: %.300s", rec.Code, rec.Body.String())
						}
					} else {
						wantCalls, wantTokens = 1, 12
						if rec.Code != 200 || !strings.Contains(rec.Body.String(), "matrix-ok") {
							t.Fatalf("valid projection status=%d: %.300s", rec.Code, rec.Body.String())
						}
					}
					assertCarbonWireUsage(t, sink, authIndex, refused, wantTokens)
					if f.calls.Load() != wantCalls {
						t.Fatalf("inference calls=%d, want %d", f.calls.Load(), wantCalls)
					}
				})
			}
		}
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("carbon/openai/stream=%t/assistant progress", stream), func(t *testing.T) {
			f := &carbonBudgetWire{t: t}
			slot.Store(f)
			defer slot.Store(nil)
			req := budgetFixture(t, "openai", []int{10}, "")
			_, body := carbonProgressFixture(t, req)
			body["stream"] = stream
			rec := matrixPost(t, router, "/v1/chat/completions", fmt.Sprintf("carbon-progress-%t", stream), body)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), "11 images") || f.calls.Load() != 0 {
				t.Fatalf("current attachment budget status=%d inference=%d", rec.Code, f.calls.Load())
			}
			assertCarbonWireUsage(t, sink, authIndex, true, 0)
		})
	}
}

func setCarbonWireInstruction(t *testing.T, format string, body matrixObject, text string) {
	t.Helper()
	key := "messages"
	if format == "openai-response" {
		key = "input"
	}
	items, _ := body[key].([]any)
	for i := len(items) - 1; i >= 0; i-- {
		msg, _ := items[i].(matrixObject)
		if msg["role"] == "user" && msg["content"] == "run the tools" {
			msg["content"] = text
			return
		}
	}
	t.Fatal("fixture has no instruction for current tools")
}

func assertCarbonWireUsage(t *testing.T, sink *copilotUsageSink, authIndex string, failed bool, tokens int64) {
	t.Helper()
	select {
	case record := <-sink.records:
		if record.AuthIndex != authIndex || record.Failed != failed || record.Detail.TotalTokens != tokens {
			t.Fatalf("wrong attribution: failed=%v tokens=%d", record.Failed, record.Detail.TotalTokens)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no formal usage")
	}
}
