package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func newCodexUpstreamModelServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexCompletedSSE("gpt-5.5-sol-2026-0815", 516)))
	}))
}

func TestCodexExecutorRecordsUpstreamResponseModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "non-stream", stream: false},
		{name: "stream", stream: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			recorder := &codexAbnormalReasoningRetryUsageRecorder{}
			usage.RegisterNamedPlugin("codex-upstream-model-test", recorder)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin("codex-upstream-model-test", noopUsagePlugin{})
			})

			server := newCodexUpstreamModelServer(t)
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := codexAbnormalReasoningRetryTestAuth(server.URL)
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.5-sol",
				Payload: []byte(`{"model":"gpt-5.5-sol","input":"hello"}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Stream:       tc.stream,
			}

			if tc.stream {
				result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}
				for range result.Chunks {
				}
			} else if _, errExec := executor.Execute(context.Background(), auth, req, opts); errExec != nil {
				t.Fatalf("Execute: %v", errExec)
			}

			record := recorder.waitForRecord(t, func(record usage.Record) bool {
				return record.AuthID == "codex-oauth-1" && record.Model == "gpt-5.5-sol"
			})
			if record.UpstreamModel != "gpt-5.5-sol-2026-0815" {
				t.Fatalf("record.UpstreamModel = %q, want gpt-5.5-sol-2026-0815", record.UpstreamModel)
			}
		})
	}
}
