package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityCompactionRequiresCompleteSummary(t *testing.T) {
	for _, model := range []string{"gemini-3.7-flash", "claude-sonnet-4-6"} {
		for _, mode := range []string{"stream-trigger", "nonstream-trigger", "compact-endpoint"} {
			for _, tc := range []struct {
				name, reason, summary string
				wantError             bool
			}{
				{"complete", "STOP", "Summary of synthetic history", false},
				{"truncated", "MAX_TOKENS", "Partial summary", true},
				{"blocked", "SAFETY", "Partial summary", true},
				{"missing-terminal", "", "Partial summary", true},
				{"empty", "STOP", "", true},
				{"whitespace", "STOP", "  \n", true},
			} {
				t.Run(model+"/"+mode+"/"+tc.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						content := fmt.Sprintf(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"not the summary"},{"text":%q}]}}]}}`, tc.summary)
						terminal := fmt.Sprintf(`{"response":{"candidates":[{"finishReason":%q}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}}}`, tc.reason)
						if strings.Contains(model, "claude") {
							w.Header().Set("Content-Type", "text/event-stream")
							// 摘要文本和结束原因分片到达，验证聚合后仍能识别截断。
							_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", content, terminal)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprintf(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"not the summary"},{"text":%q}]},"finishReason":%q}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}}}`, tc.summary, tc.reason)
					}))
					defer server.Close()
					executor := NewAntigravityExecutor(&config.Config{})
					auth := &cliproxyauth.Auth{ID: "compaction-completion-test", Provider: "antigravity",
						Metadata:   map[string]any{"access_token": "test-token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "test-project"},
						Attributes: map[string]string{"base_url": server.URL}}
					payload := []byte(`{"input":[{"role":"user","content":"synthetic history"},{"type":"compaction_trigger"}]}`)
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse}
					if mode == "compact-endpoint" {
						opts.Alt = "responses/compact"
						payload = []byte(`{"input":[{"role":"user","content":"synthetic history"}]}`)
					}
					original := bytes.Clone(payload)
					req := cliproxyexecutor.Request{Model: model, Payload: payload}
					var result []byte
					var err error
					if mode == "stream-trigger" {
						opts.Stream = true
						var stream *cliproxyexecutor.StreamResult
						stream, err = executor.ExecuteStream(context.Background(), auth, req, opts)
						if err == nil {
							for chunk := range stream.Chunks {
								if chunk.Err != nil {
									t.Fatalf("unexpected chunk error: %v", chunk.Err)
								}
								for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
									data := bytes.TrimPrefix(line, []byte("data: "))
									if gjson.GetBytes(data, "type").String() == "response.completed" {
										result = []byte(gjson.GetBytes(data, "response").Raw)
									}
								}
							}
						} else if stream != nil {
							t.Fatal("failed compaction must not return a stream")
						}
					} else {
						var resp cliproxyexecutor.Response
						resp, err = executor.Execute(context.Background(), auth, req, opts)
						result = resp.Payload
					}
					if !bytes.Equal(payload, original) {
						t.Fatal("compaction mutated caller history")
					}
					if tc.wantError {
						var status interface{ StatusCode() int }
						if !errors.As(err, &status) || status.StatusCode() != http.StatusBadGateway {
							t.Fatalf("error = %v, want failed compaction with status 502", err)
						}
						if len(result) != 0 {
							t.Fatal("failed compaction returned a result")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if gjson.GetBytes(result, "status").String() != "completed" {
						t.Fatalf("missing completed response: %s", result)
					}
					summary, err := helps.UnsealAntigravityCompaction(gjson.GetBytes(result, "output.0.encrypted_content").String())
					if err != nil || summary != tc.summary {
						t.Fatalf("summary = %q, error = %v", summary, err)
					}
					for key, want := range map[string]int64{"input_tokens": 10, "output_tokens": 5, "total_tokens": 17} {
						if got := gjson.GetBytes(result, "usage."+key).Int(); got != want {
							t.Errorf("usage.%s = %d, want %d", key, got, want)
						}
					}
				})
			}
		}
	}
}
