package executor

import (
	"bytes"
	"context"
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
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorChatCompletionUsageObservesCompleteFrames(t *testing.T) {
	const model = "Doubao-Seed-2.1-Pro"
	const contentChunk = `{"id":"chatcmpl-test","object":"chat.completion.chunk","model":"Doubao-Seed-2.1-Pro","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`
	const usageChunk = `{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`
	const done = "data: [DONE]\n\n"
	frame := func(lines ...string) string {
		return "data: " + strings.Join(lines, "\ndata: ") + "\n\n"
	}
	multilineContent := frame(
		`{"id":"chatcmpl-test","object":"chat.completion.chunk",`,
		`"model":"Doubao-Seed-2.1-Pro",`,
		`"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
	)
	multilineUsage := frame(
		`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[],`,
		`"service_tier":"priority",`,
		`"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
	)

	tests := []struct {
		name       string
		body       string
		nonStream  bool
		wantModel  string
		wantTokens int64
		wantTier   string
		wantText   string
		wantErr    string
	}{
		{
			name:       "nonstream matching model",
			body:       `{"id":"chatcmpl-test","object":"chat.completion","model":"Doubao-Seed-2.1-Pro","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
			nonStream:  true,
			wantModel:  model,
			wantTokens: 7,
			wantText:   "ok",
		},
		{
			name:       "nonstream missing model stays unknown",
			body:       `{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
			nonStream:  true,
			wantTokens: 7,
			wantText:   "ok",
		},
		{
			name:       "single line events",
			body:       frame(contentChunk) + frame(usageChunk) + done,
			wantModel:  model,
			wantTokens: 7,
			wantText:   "ok",
		},
		{
			name:       "multiline model with separate usage",
			body:       multilineContent + frame(usageChunk) + done,
			wantModel:  model,
			wantTokens: 7,
			wantText:   "ok",
		},
		{
			name:       "multiline usage and tier",
			body:       frame(contentChunk) + multilineUsage + done,
			wantModel:  model,
			wantTokens: 7,
			wantTier:   "priority",
			wantText:   "ok",
		},
		{
			name: "model and usage in one multiline event",
			body: frame(
				`{"id":"chatcmpl-test","object":"chat.completion.chunk",`,
				`"model":"Doubao-Seed-2.1-Pro","service_tier":"priority",`,
				`"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],`,
				`"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
			) + done,
			wantModel:  model,
			wantTokens: 7,
			wantTier:   "priority",
			wantText:   "ok",
		},
		{
			name:      "multiline model without usage",
			body:      multilineContent + done,
			wantModel: model,
			wantText:  "ok",
		},
		{
			name: "model only in final usage event",
			body: frame(`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`) + frame(
				`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[],`,
				`"model":"Doubao-Seed-2.1-Pro",`,
				`"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
			) + done,
			wantModel:  model,
			wantTokens: 7,
			wantText:   "ok",
		},
		{
			name:       "CRLF and keepalive metadata",
			body:       strings.ReplaceAll(": keepalive\n\nevent: message\nid: event-1\nretry: 1000\n"+multilineContent+multilineUsage+done, "\n", "\r\n"),
			wantModel:  model,
			wantTokens: 7,
			wantTier:   "priority",
			wantText:   "ok",
		},
		{
			name:       "complete pending event at EOF",
			body:       multilineContent + strings.TrimRight(multilineUsage, "\n"),
			wantModel:  model,
			wantTokens: 7,
			wantTier:   "priority",
			wantText:   "ok",
		},
		{
			name: "stream missing model stays unknown",
			body: frame(
				`{"id":"chatcmpl-test","object":"chat.completion.chunk",`,
				`"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			) + frame(usageChunk) + done,
			wantTokens: 7,
			wantText:   "ok",
		},
		{
			name:       "post DONE metadata is ignored",
			body:       multilineContent + frame(usageChunk) + done + frame(`{"model":"ignored-model","usage":{"prompt_tokens":99,"completion_tokens":99,"total_tokens":198}}`),
			wantModel:  model,
			wantTokens: 7,
			wantText:   "ok",
		},
		{
			name: "invalid frame cannot supply model or usage",
			body: frame(
				`{"model":"untrusted-model","usage":{"prompt_tokens":99,"completion_tokens":99,"total_tokens":198}}`,
				`{"choices":[]}`,
			) + done,
			wantErr: "incomplete SSE data frame",
		},
		{
			name:    "model cannot cross event boundaries",
			body:    frame(`{"id":"chatcmpl-test",`) + frame(`"model":"Doubao-Seed-2.1-Pro","choices":[]}`) + done,
			wantErr: "incomplete SSE data frame",
		},
		{
			name:      "multiline model survives later error",
			body:      multilineContent + "event: error\n" + frame(`{"code":"upstream_failed",`, `"message":"upstream failed"}`) + done,
			wantModel: model,
			wantText:  "ok",
			wantErr:   "upstream failed",
		},
		{
			name:      "error frame preserves reported model",
			body:      "event: error\n" + frame(`{"model":"Doubao-Seed-2.1-Pro","error":{"message":"upstream failed"}}`),
			wantModel: model,
			wantErr:   "upstream failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = coreusage.WithRequestedModelAlias(ctx, t.Name())
			ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "chat-completions.test" || req.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected upstream URL: %s", req.URL)
				}
				contentType := "text/event-stream"
				if tc.nonStream {
					contentType = "application/json"
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{contentType}},
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Request:    req,
				}, nil
			}))
			auth := &cliproxyauth.Auth{
				ID:       "chat-model-test",
				Index:    "chat-model-auth-index",
				Provider: "openai-compatible-doubao",
				Attributes: map[string]string{
					"base_url": "https://chat-completions.test/v1",
				},
			}
			executor := NewOpenAICompatExecutor(auth.Provider, &config.Config{})
			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"Doubao-Seed-2.1-Pro","messages":[{"role":"user","content":"hi"}]}`),
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: !tc.nonStream}
			var payloads [][]byte
			var streamErr error
			if tc.nonStream {
				response, err := executor.Execute(ctx, auth, req, opts)
				if err != nil {
					t.Fatalf("Execute error: %v", err)
				}
				payloads = append(payloads, response.Payload)
			} else {
				response, err := executor.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				for chunk := range response.Chunks {
					if chunk.Err != nil {
						streamErr = chunk.Err
					}
					if len(chunk.Payload) > 0 {
						payloads = append(payloads, chunk.Payload)
					}
				}
			}
			if tc.wantErr == "" {
				if streamErr != nil {
					t.Fatalf("unexpected stream error: %v", streamErr)
				}
			} else {
				if streamErr == nil || !strings.Contains(streamErr.Error(), tc.wantErr) {
					t.Fatalf("stream error = %v, want %q", streamErr, tc.wantErr)
				}
				status, ok := streamErr.(interface{ StatusCode() int })
				if !ok || status.StatusCode() != http.StatusBadGateway {
					t.Fatalf("stream error status = %v, want %d", streamErr, http.StatusBadGateway)
				}
			}

			var text strings.Builder
			var clientModel string
			for _, payload := range payloads {
				payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:")))
				if !gjson.ValidBytes(payload) {
					t.Fatalf("invalid forwarded JSON: %s", payload)
				}
				if value := gjson.GetBytes(payload, "model").String(); value != "" {
					clientModel = value
				}
				if tc.nonStream {
					text.WriteString(gjson.GetBytes(payload, "choices.0.message.content").String())
				} else {
					text.WriteString(gjson.GetBytes(payload, "choices.0.delta.content").String())
				}
			}
			if text.String() != tc.wantText {
				t.Errorf("forwarded content = %q, want %q", text.String(), tc.wantText)
			}
			if tc.wantErr == "" && clientModel != tc.wantModel {
				t.Errorf("forwarded model = %q, want %q", clientModel, tc.wantModel)
			}

			record := capture.await(t)
			if record.Model != model || record.Alias != t.Name() || record.AuthIndex != auth.Index {
				t.Errorf("usage attribution = model %q, alias %q, auth index %q", record.Model, record.Alias, record.AuthIndex)
			}
			if record.UpstreamModel != tc.wantModel {
				t.Errorf("UpstreamModel = %q, want %q", record.UpstreamModel, tc.wantModel)
			}
			if record.ResponseModel != tc.wantModel {
				t.Errorf("ResponseModel = %q, want %q", record.ResponseModel, tc.wantModel)
			}
			if record.Detail.TotalTokens != tc.wantTokens {
				t.Errorf("TotalTokens = %d, want %d", record.Detail.TotalTokens, tc.wantTokens)
			}
			if tc.wantTokens != 0 && (record.Detail.InputTokens != 3 || record.Detail.OutputTokens != 4) {
				t.Errorf("input/output tokens = %d/%d, want 3/4", record.Detail.InputTokens, record.Detail.OutputTokens)
			}
			if record.ResponseServiceTier != tc.wantTier {
				t.Errorf("ResponseServiceTier = %q, want %q", record.ResponseServiceTier, tc.wantTier)
			}
			if record.Failed != (tc.wantErr != "") {
				t.Errorf("Failed = %t, want %t", record.Failed, tc.wantErr != "")
			}
			if tc.wantErr != "" && record.Fail.StatusCode != http.StatusBadGateway {
				t.Errorf("failure status = %d, want %d", record.Fail.StatusCode, http.StatusBadGateway)
			}

			barrierID := t.Name() + "/barrier"
			coreusage.PublishRecord(ctx, coreusage.Record{RequestID: barrierID, Alias: t.Name()})
			if next := capture.await(t); next.RequestID != barrierID {
				t.Fatalf("unexpected duplicate usage record before queue barrier: %q", next.RequestID)
			}
		})
	}
}
