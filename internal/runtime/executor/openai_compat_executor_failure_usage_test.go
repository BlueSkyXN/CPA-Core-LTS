package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type openAICompatFailureReader struct{ err error }

func (r openAICompatFailureReader) Read([]byte) (int, error) { return 0, r.err }

func TestOpenAICompatStreamFailurePreservesObservedUsage(t *testing.T) {
	for _, scenario := range []string{"later-error-event", "usage-in-error-event", "invalid-frame", "raw-error-body", "error-event-without-data", "scanner-error", "responses-missing-done"} {
		for _, withUsage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/usage=%t", scenario, withUsage), func(t *testing.T) {
				alias := t.Name()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = coreusage.WithRequestedModelAlias(ctx, alias)
				ctx = coreusage.WithTraceID(ctx, alias)
				ctx = coreusage.WithStream(ctx, true)
				fields := ""
				if withUsage {
					fields = `,"service_tier":"priority","usage":{"prompt_tokens":3,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens":4,"completion_tokens_details":{"reasoning_tokens":1},"total_tokens":7}`
				}
				payload := `{"id":"synthetic","object":"chat.completion.chunk","model":"synthetic-model","choices":[]` + fields + `}`
				frame := "data: " + payload + "\n\n"
				makeBody := func() io.Reader {
					switch scenario {
					case "usage-in-error-event":
						return strings.NewReader("event: error\ndata: " + strings.TrimSuffix(payload, "}") + `,"error":{"message":"synthetic private upstream error"}}` + "\n\n")
					case "invalid-frame":
						return strings.NewReader(frame + "data: {\"usage\":{\"total_tokens\":999}}\ndata: {\"extra\":true}\n\n")
					case "raw-error-body":
						return strings.NewReader(frame + `{"error":"synthetic private upstream error"}`)
					case "error-event-without-data":
						return strings.NewReader(frame + "event: error\n\n")
					case "scanner-error":
						return io.MultiReader(strings.NewReader(frame), openAICompatFailureReader{statusErr{code: http.StatusServiceUnavailable, msg: "synthetic read failure"}})
					case "responses-missing-done":
						return strings.NewReader(frame)
					default:
						return strings.NewReader(frame + "event: error\ndata: {\"error\":{\"message\":\"synthetic private upstream error\"}}\n\n")
					}
				}
				capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 8)}
				coreusage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(makeBody()), Request: req}, nil
				}))
				auth := &cliproxyauth.Auth{ID: "synthetic-auth", Index: "synthetic-index", Provider: "openai-review", Attributes: map[string]string{"base_url": "https://synthetic.invalid/v1"}}
				executor := NewOpenAICompatExecutor(auth.Provider, &config.Config{})
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true}
				if scenario == "responses-missing-done" {
					opts.ResponseFormat = sdktranslator.FormatOpenAIResponse
				}
				previousID := ""
				for range 2 {
					result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "synthetic-model", Payload: []byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"synthetic"}]}`)}, opts)
					if err != nil {
						t.Fatal(err)
					}
					var output bytes.Buffer
					errorsSeen := 0
					for chunk := range result.Chunks {
						output.Write(chunk.Payload)
						if chunk.Err != nil {
							errorsSeen++
						}
					}
					if errorsSeen != 1 {
						t.Errorf("errors=%d, want 1", errorsSeen)
					}
					if bytes.Contains(output.Bytes(), []byte("[DONE]")) || bytes.Contains(output.Bytes(), []byte(`"type":"response.completed"`)) {
						t.Error("failed stream delivered success terminator")
					}
					record := capture.await(t)
					wantStatus := http.StatusBadGateway
					if scenario == "scanner-error" {
						wantStatus = http.StatusServiceUnavailable
					}
					if !record.Failed || record.Fail.StatusCode != wantStatus {
						t.Errorf("failure=%t status=%d", record.Failed, record.Fail.StatusCode)
					}
					if strings.Contains(record.Fail.Body, "synthetic private") {
						t.Error("failure exposed upstream payload")
					}
					if record.RequestID == "" || record.RequestID == previousID || record.TraceID != alias || record.AuthID != auth.ID || record.AuthIndex != auth.Index || record.ResponseModel != "synthetic-model" || !record.Stream {
						t.Error("lost attempt identity or attribution")
					}
					previousID = record.RequestID
					want := coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(0, 0, 0, 0, 0, 0)}
					if withUsage {
						want = coreusage.Detail{InputTokens: 3, OutputTokens: 4, ReasoningTokens: 1, CachedTokens: 2, CacheReadTokens: 2, TotalTokens: 7, TokenBreakdown: coreusage.NewSubsetTokenBreakdown(3, 2, 0, 4, 1, 7), ResponseServiceTier: "priority"}
					}
					if record.Detail != want || record.ResponseServiceTier != want.ResponseServiceTier || record.EffectiveServiceTier != want.ResponseServiceTier {
						t.Errorf("usage=%+v, want %+v", record.Detail, want)
					}
				}
				coreusage.PublishRecord(ctx, coreusage.Record{Alias: alias, Model: "dispatch-barrier"})
				if next := capture.await(t); next.Model != "dispatch-barrier" {
					t.Error("duplicate attempt usage")
				}
			})
		}
	}
}
