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
	for i, n := range []int{1, 2, 3, 4, 6, 7, 8, 9, 10, 11} {
		if i >= len(urls) || urls[i] != fmt.Sprintf("https://example.com/fixture-%02d.png", n) {
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
			t.Run(fmt.Sprintf("carbon/%s/stream=%t", p.format, stream), func(t *testing.T) {
				f := &carbonBudgetWire{t: t}
				slot.Store(f)
				defer slot.Store(nil)
				var body matrixObject
				for _, over := range []bool{false, true} {
					batches := []int{5, 2, 4}
					if over {
						batches = []int{11}
					}
					req := budgetFixture(t, p.format, batches, "")
					if err := json.Unmarshal(req.OriginalRequest, &body); err != nil {
						t.Fatal(err)
					}
					body["stream"] = stream
					rec := matrixPost(t, router, p.path, fmt.Sprintf("carbon-%s-%t-%t", p.format, stream, over), body)
					if !over {
						if rec.Code != 200 || !strings.Contains(rec.Body.String(), "matrix-ok") {
							t.Fatalf("valid projection status=%d: %.300s", rec.Code, rec.Body.String())
						}
					} else if rec.Code != 400 || !strings.Contains(rec.Body.String(), "limit 10") {
						t.Fatalf("all-current refusal status=%d: %.300s", rec.Code, rec.Body.String())
					}
					select {
					case record := <-sink.records:
						if record.AuthIndex != authIndex || record.Failed != over || (!over && record.Detail.TotalTokens != 12) || (over && record.Detail.TotalTokens != 0) {
							t.Fatalf("wrong attribution: fail=%v tokens=%d", record.Failed, record.Detail.TotalTokens)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("no formal usage")
					}
				}
				if f.calls.Load() != 1 {
					t.Fatal("refusal dispatched a model request", f.calls.Load())
				}
			})
		}
	}
}
