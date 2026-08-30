package pluginhost

import (
	"context"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type noopFormalPluginUsageSink struct{}

func (noopFormalPluginUsageSink) HandleUsage(context.Context, coreusage.Record) {}

func TestPluginExecutorUsageDetailParsesChatUsage(t *testing.T) {
	detail := pluginExecutorUsageDetail(sdktranslator.FormatOpenAI, []byte(`{
		"choices":[],
		"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}
	}`))
	if detail.InputTokens != 20 || detail.OutputTokens != 3 || detail.TotalTokens != 23 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestPluginExecutorUsageReportedMatchesSupportedProviderPaths(t *testing.T) {
	tests := []struct {
		name    string
		format  sdktranslator.Format
		payload string
	}{
		{name: "gemini snake case", format: sdktranslator.FormatGemini, payload: `{"usage_metadata":{"promptTokenCount":1}}`},
		{name: "antigravity nested", format: sdktranslator.FormatAntigravity, payload: `{"response":{"usageMetadata":{"promptTokenCount":1}}}`},
		{name: "interactions total", format: sdktranslator.FormatInteractions, payload: `{"total_usage":{"input_tokens":1}}`},
		{name: "interactions metadata usage", format: sdktranslator.FormatInteractions, payload: `{"metadata":{"usage":{"input_tokens":1}}}`},
		{name: "interactions nested total", format: sdktranslator.FormatInteractions, payload: `{"interaction":{"metadata":{"total_usage":{"input_tokens":1}}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !pluginExecutorUsageReported(tt.format, []byte(tt.payload)) {
				t.Fatalf("usage path was not recognized: %s", tt.payload)
			}
		})
	}
}

func TestObserveFormalPluginStreamUsagePublishesSelectedAuthAttribution(t *testing.T) {
	const sinkName = "test:formal-plugin-provider-usage"
	records := make(chan coreusage.Record, 1)
	coreusage.RegisterNamedPlugin(sinkName, coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
		if record.Provider == "codebuddy" {
			records <- record
		}
	}))
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(sinkName, noopFormalPluginUsageSink{}) })

	auth := &coreauth.Auth{ID: "auth-1", Provider: "codebuddy"}
	reporter := helps.NewUsageReporter(context.Background(), "codebuddy", "hy3-preview-agent", auth)
	reporter.StartResponseTTFT()
	in := make(chan pluginapi.ExecutorStreamChunk, 2)
	in <- pluginapi.ExecutorStreamChunk{Payload: []byte(`{"choices":[{"delta":{"content":"ok"}}]}`)}
	in <- pluginapi.ExecutorStreamChunk{Payload: []byte(`{"choices":[],"usage":{"prompt_tokens":26,"completion_tokens":8,"total_tokens":34}}`)}
	close(in)
	for range observeFormalPluginStreamUsage(context.Background(), reporter, sdktranslator.FormatOpenAI, in) {
	}

	select {
	case record := <-records:
		if record.Provider != "codebuddy" || record.Model != "hy3-preview-agent" || record.AuthID != "auth-1" || record.AuthIndex == "" {
			t.Fatalf("record attribution = %+v", record)
		}
		if record.UsageProvenance != coreusage.UsageProvenanceProviderReportedUnverified {
			t.Fatalf("record usage provenance = %q", record.UsageProvenance)
		}
		if record.Detail.InputTokens != 26 || record.Detail.OutputTokens != 8 || record.Detail.TotalTokens != 34 {
			t.Fatalf("record detail = %+v", record.Detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for formal plugin usage record")
	}
}

func TestFormalUsageReporterSkipsDirectExecutorRoute(t *testing.T) {
	adapter := &executorAdapter{provider: "codebuddy"}
	prepared := preparedExecutorCall{
		req:         coreexecutor.Request{Model: "hy3-preview-agent", Payload: []byte(`{"model":"hy3-preview-agent"}`)},
		inputFormat: sdktranslator.FormatOpenAI,
	}
	if reporter := adapter.formalUsageReporter(context.Background(), nil, prepared); reporter != nil {
		t.Fatal("direct plugin executor route received a second usage reporter")
	}
}

func TestObserveFormalPluginStreamUsageRecordsEmptyAndCancelledStreamsAsFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		ctx    func() context.Context
		closed bool
	}{
		{name: "nil stream", ctx: context.Background},
		{name: "empty stream", ctx: context.Background, closed: true},
		{name: "cancelled closed stream", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, closed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const sinkName = "test:formal-plugin-empty-stream-usage"
			records := make(chan coreusage.Record, 1)
			coreusage.RegisterNamedPlugin(sinkName, coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
				if record.Provider == "empty-plugin" {
					records <- record
				}
			}))
			t.Cleanup(func() { coreusage.RegisterNamedPlugin(sinkName, noopFormalPluginUsageSink{}) })

			reporter := helps.NewUsageReporter(tt.ctx(), "empty-plugin", "empty-model", &coreauth.Auth{ID: "auth-empty"})
			var input chan pluginapi.ExecutorStreamChunk
			if tt.closed {
				input = make(chan pluginapi.ExecutorStreamChunk)
				close(input)
			}
			for range observeFormalPluginStreamUsage(tt.ctx(), reporter, sdktranslator.FormatOpenAI, input) {
			}
			select {
			case record := <-records:
				if !record.Failed {
					t.Fatalf("empty/cancelled stream recorded success: %+v", record)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for failure usage record")
			}
		})
	}
}

func TestFormalPluginResponseHeadersReachUsageRecord(t *testing.T) {
	const sinkName = "test:formal-plugin-response-headers"
	records := make(chan coreusage.Record, 1)
	coreusage.RegisterNamedPlugin(sinkName, coreUsagePluginFunc(func(_ context.Context, record coreusage.Record) {
		if record.Provider == "plugin-provider" {
			records <- record
		}
	}))
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(sinkName, noopFormalPluginUsageSink{}) })

	host := New()
	adapter := newCurrentExecutorAdapterForTest(host, "usage-headers", &fakeExecutor{
		execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{
				Payload: []byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`),
				Headers: http.Header{"X-Provider-Request-Id": {"provider-request-1"}},
			}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	_, errExecute := adapter.Execute(ctx, &coreauth.Auth{ID: "auth-headers", Provider: "plugin-provider"}, coreexecutor.Request{
		Model: "model-headers", Payload: []byte(`{"model":"model-headers"}`),
	}, coreexecutor.Options{})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	select {
	case record := <-records:
		if got := record.ResponseHeaders.Get("X-Provider-Request-Id"); got != "provider-request-1" {
			t.Fatalf("usage response header = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage response headers")
	}
}
