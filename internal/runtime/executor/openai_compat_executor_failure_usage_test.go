package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type openAICompatFailureReader struct{ err error }

func (r openAICompatFailureReader) Read([]byte) (int, error) { return 0, r.err }

type openAICompatFailureState struct {
	err    error
	atEOF  bool
	cancel context.CancelFunc
}

func (s *openAICompatFailureState) ToolInputError() error { return s.err }
func (s *openAICompatFailureState) FinalizeToolInput() [][]byte {
	if !s.atEOF {
		return nil
	}
	s.err = errors.New("synthetic EOF conversion error")
	if s.cancel != nil {
		s.cancel()
	}
	return [][]byte{[]byte(`data: {"type":"response.failed"}`)}
}

func TestOpenAICompatStreamFailureKeepsSeparateAttempts(t *testing.T) {
	alias := uuid.NewString()
	capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 8)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = coreusage.WithRequestedModelAlias(ctx, alias)
	ctx = coreusage.WithTraceID(ctx, alias)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body := "data: {\"model\":\"synthetic-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\nevent: error\ndata: {\"error\":{\"message\":\"synthetic failure\"}}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}))
	auth := &cliproxyauth.Auth{ID: "synthetic-auth", Provider: "openai-review", Attributes: map[string]string{"base_url": "https://synthetic.invalid/v1"}}
	executor := NewOpenAICompatExecutor(auth.Provider, &config.Config{})
	for range 2 {
		result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "synthetic-model", Payload: []byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"synthetic"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
		if err != nil {
			t.Fatal(err)
		}
		for range result.Chunks {
		}
	}
	coreusage.PublishRecord(ctx, coreusage.Record{Alias: alias, Model: "dispatch-barrier"})
	first, second := capture.await(t), capture.await(t)
	for _, record := range []coreusage.Record{first, second} {
		if !record.Failed || record.TraceID != alias || record.Detail.TotalTokens != 7 {
			t.Error("an actual upstream attempt lost its separate failed consumption record")
		}
	}
	if first.RequestID == "" || first.RequestID == second.RequestID {
		t.Error("separate executions reused the same attempt ID")
	}
	if next := capture.await(t); next.Model != "dispatch-barrier" {
		t.Error("an attempt published duplicate usage")
	}
}

func TestOpenAICompatStreamFailurePreservesObservedUsage(t *testing.T) {
	for _, scenario := range []string{
		"later-error-event", "usage-in-error-event", "invalid-frame", "raw-error-body",
		"error-event-without-data", "scanner-error", "responses-missing-done",
		"apply-patch-error", "apply-patch-eof", "synthetic-done-error",
		"canceled-translation-error", "canceled-eof-error",
	} {
		for _, withUsage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/usage=%t", scenario, withUsage), func(t *testing.T) {
				alias := uuid.NewString()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = coreusage.WithRequestedModelAlias(ctx, alias)
				ctx = coreusage.WithTraceID(ctx, alias)
				ctx = coreusage.WithStream(ctx, true)
				usageFields := ""
				if withUsage {
					usageFields = `,"service_tier":"priority","usage":{"prompt_tokens":3,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens":4,"completion_tokens_details":{"reasoning_tokens":1},"total_tokens":7}`
				}
				usagePayload := `{"id":"synthetic","object":"chat.completion.chunk","model":"synthetic-model","choices":[]` + usageFields + `}`
				usageFrame := "data: " + usagePayload + "\n\n"
				var body io.Reader = strings.NewReader(usageFrame + "event: error\ndata: {\"error\":{\"message\":\"synthetic private upstream error\"}}\n\n")
				request := []byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"synthetic"}]}`)
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true}
				wantFailure := "upstream stream returned an error payload"
				wantStatus := http.StatusBadGateway
				wantClientError := "synthetic private upstream error"
				canceled := strings.HasPrefix(scenario, "canceled-")
				switch scenario {
				case "usage-in-error-event":
					body = strings.NewReader("event: error\n" + "data: " + strings.TrimSuffix(usagePayload, "}") + `,"error":{"message":"synthetic private upstream error"}}` + "\n\n")
				case "invalid-frame":
					body = strings.NewReader(usageFrame + "data: {\"model\":\"untrusted\",\"usage\":{\"total_tokens\":999}}\ndata: {\"extra\":true}\n\n")
					wantFailure = "upstream stream ended with incomplete SSE data frame"
					wantClientError = wantFailure
				case "raw-error-body":
					body = strings.NewReader(usageFrame + `{"error":"synthetic private upstream error"}`)
				case "error-event-without-data":
					body = strings.NewReader(usageFrame + "event: error\n\n")
					wantFailure = "upstream error event ended without data"
					wantClientError = wantFailure
				case "scanner-error":
					wantFailure = "synthetic upstream read failure"
					wantStatus = http.StatusServiceUnavailable
					wantClientError = wantFailure
					body = io.MultiReader(strings.NewReader(usageFrame), openAICompatFailureReader{statusErr{code: wantStatus, msg: wantFailure}})
				case "responses-missing-done":
					opts.ResponseFormat = sdktranslator.FormatOpenAIResponse
					body = strings.NewReader(usageFrame)
					wantFailure = "upstream stream closed before [DONE]"
					wantClientError = wantFailure
				case "apply-patch-error", "apply-patch-eof":
					opts.SourceFormat = sdktranslator.FormatOpenAIResponse
					request = []byte(task6PatchRequest)
					mode := "stream"
					if scenario == "apply-patch-eof" {
						mode = "eof"
					}
					body = strings.NewReader(usageFrame + task6ProviderFixture("custom-compat", mode, "apply_patch"))
					wantFailure = helps.ApplyPatchUpstreamErrorMessage
					wantClientError = wantFailure
				case "synthetic-done-error", "canceled-translation-error", "canceled-eof-error":
					format := sdktranslator.Format("failure-usage-" + alias)
					opts.ResponseFormat = format
					sdktranslator.Register(format, sdktranslator.FormatOpenAI, nil, sdktranslator.ResponseTransform{
						Stream: func(_ context.Context, _ string, _, _, raw []byte, param *any) [][]byte {
							state, _ := (*param).(*openAICompatFailureState)
							if state == nil {
								state = &openAICompatFailureState{atEOF: scenario == "canceled-eof-error", cancel: cancel}
								*param = state
							}
							if scenario == "canceled-translation-error" || (scenario == "synthetic-done-error" && bytes.Contains(raw, []byte("[DONE]"))) {
								state.err = errors.New("synthetic private conversion error")
								if canceled {
									cancel()
								}
								return [][]byte{[]byte(`data: {"type":"response.failed"}`)}
							}
							return nil
						},
					})
					t.Cleanup(func() { sdktranslator.Unregister(format, sdktranslator.FormatOpenAI) })
					body = strings.NewReader(usageFrame)
					wantFailure = helps.ApplyPatchUpstreamErrorMessage
					wantClientError = wantFailure
				}

				capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 8)}
				coreusage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body), Request: req}, nil
				}))
				auth := &cliproxyauth.Auth{ID: "synthetic-auth", Index: "synthetic-index", Provider: "openai-review", Attributes: map[string]string{"base_url": "https://synthetic.invalid/v1"}}
				result, err := NewOpenAICompatExecutor(auth.Provider, &config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "synthetic-model", Payload: request}, opts)
				if err != nil {
					t.Fatal(err)
				}
				errorsSeen := 0
				var output bytes.Buffer
				for chunk := range result.Chunks {
					output.Write(chunk.Payload)
					if chunk.Err != nil {
						errorsSeen++
						if !strings.Contains(chunk.Err.Error(), wantClientError) {
							t.Errorf("client error=%v, want %q", chunk.Err, wantClientError)
						}
						if status, ok := chunk.Err.(interface{ StatusCode() int }); !ok || status.StatusCode() != wantStatus {
							t.Errorf("client status=%v, want %d", chunk.Err, wantStatus)
						}
					}
				}
				if !canceled && errorsSeen != 1 {
					t.Errorf("delivered errors=%d, want 1", errorsSeen)
				}
				if bytes.Contains(output.Bytes(), []byte("[DONE]")) || bytes.Contains(output.Bytes(), []byte(`"type":"response.completed"`)) {
					t.Error("failed stream delivered a success terminator")
				}
				if strings.HasPrefix(scenario, "apply-patch-") && bytes.Count(output.Bytes(), []byte(`"type":"response.failed"`)) != 1 {
					t.Error("apply_patch failure did not retain its single translated failure event")
				}
				coreusage.PublishRecord(ctx, coreusage.Record{Alias: alias, Model: "dispatch-barrier"})
				record := capture.await(t)
				if !record.Failed || record.Fail.StatusCode != wantStatus || record.Fail.Body != wantFailure {
					t.Errorf("failure=%t %+v, want sanitized failure %d %q", record.Failed, record.Fail, wantStatus, wantFailure)
				}
				if record.RequestID == "" || record.TraceID != alias || record.AuthID != auth.ID || record.AuthIndex != auth.Index || record.Model != "synthetic-model" || record.ResponseModel != "synthetic-model" || !record.Stream {
					t.Error("failure lost request, model, stream or credential attribution")
				}
				want := coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(0, 0, 0, 0, 0, 0)}
				if withUsage {
					want = coreusage.Detail{InputTokens: 3, OutputTokens: 4, ReasoningTokens: 1, CachedTokens: 2, CacheReadTokens: 2, TotalTokens: 7, TokenBreakdown: coreusage.NewSubsetTokenBreakdown(3, 2, 0, 4, 1, 7), ResponseServiceTier: "priority"}
				}
				if record.Detail != want || record.ResponseServiceTier != want.ResponseServiceTier || record.EffectiveServiceTier != want.ResponseServiceTier {
					t.Errorf("failure usage=%+v tier=%q, want %+v", record.Detail, record.ResponseServiceTier, want)
				}
				if next := capture.await(t); next.Model != "dispatch-barrier" {
					t.Errorf("duplicate attempt record: %q", next.Model)
				}
			})
		}
	}
}
