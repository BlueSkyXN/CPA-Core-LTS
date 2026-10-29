package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const codexAbnormalImageCompleted = `{"type":"response.completed","response":{"id":"resp_img","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"x"}]}],"usage":{"input_tokens":1,"output_tokens":5,"total_tokens":6,"output_tokens_details":{"reasoning_tokens":516}},"tool_usage":{"image_gen":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}}}`

func codexUsageCapture(t *testing.T) (context.Context, *codexResponseModelUsageCapture) {
	t.Helper()
	alias := t.Name()
	capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 8)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{}) })
	ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
	return coreusage.WithTraceID(ctx, "abcd1234"), capture
}

func awaitCodexImageRecord(t *testing.T, capture *codexResponseModelUsageCapture, ctx context.Context) {
	t.Helper()
	coreusage.PublishRecord(ctx, coreusage.Record{Alias: t.Name(), Model: "dispatch-barrier"})
	main := capture.await(t)
	if !main.Failed || main.Detail.TotalTokens != 6 {
		t.Fatalf("main record=%+v, want failed abnormal attempt with 6 tokens", main)
	}
	image := capture.await(t)
	if image.Model != codexDefaultImageToolModel || image.Failed || image.Detail.TotalTokens != 30 {
		t.Fatalf("image record=%+v, want independent image tool consumption", image)
	}
	if next := capture.await(t); next.Model != "dispatch-barrier" {
		t.Fatalf("unexpected extra record %+v", next)
	}
}

// F10/F18: an abnormal-reasoning retry must not drop independent image tool usage.
func TestCodexAbnormalRetryPublishesImageToolUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+codexAbnormalImageCompleted+"\n\n")
	}))
	t.Cleanup(server.Close)
	cfg := codexAbnormalReasoningRetryTestConfigWithMaxAndExhausted(0, config.CodexAbnormalReasoningRetryExhaustedBehaviorPassThrough)
	auth := codexAbnormalReasoningRetryTestAuth(server.URL)
	req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"hi"}`)}

	t.Run("non-stream", func(t *testing.T) {
		ctx, capture := codexUsageCapture(t)
		_, err := NewCodexExecutor(cfg).Execute(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
		assertRetryWithoutPenaltyError(t, err)
		awaitCodexImageRecord(t, capture, ctx)
	})
	t.Run("stream", func(t *testing.T) {
		ctx, capture := codexUsageCapture(t)
		result, err := NewCodexExecutor(cfg).ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
		if err != nil {
			t.Fatalf("ExecuteStream: %v", err)
		}
		for range result.Chunks {
		}
		awaitCodexImageRecord(t, capture, ctx)
	})
}

// F17: a successful completed response without usage still counts as a request.
func TestCodexSuccessWithoutUsagePublishesMainRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_nousage\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
	}))
	t.Cleanup(server.Close)
	ctx, capture := codexUsageCapture(t)
	auth := codexOAuthTestAuth(server.URL)
	auth.ID, auth.Index = "nousage-auth", "nousage-index"
	resp, err := NewCodexExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{
		Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"hi"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil || len(resp.Payload) == 0 {
		t.Fatalf("Execute: err=%v bytes=%d", err, len(resp.Payload))
	}
	coreusage.PublishRecord(ctx, coreusage.Record{Alias: t.Name(), Model: "dispatch-barrier"})
	main := capture.await(t)
	if main.Model == "dispatch-barrier" {
		t.Fatal("successful request published no main usage record")
	}
	if main.Failed || main.AuthID != auth.ID || main.AuthIndex != auth.Index || main.RequestID == "" {
		t.Fatalf("main record=%+v, want successful attributed record", main)
	}
}
