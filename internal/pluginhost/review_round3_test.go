package pluginhost

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	pluginapi "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// 评审第三轮复现用例（拷贝自评审 overlay；translateClaudePluginStream 按
// 新签名补第 7 参 nil——评审针对的是双发取消，与该参数默认值无关）。
func TestReviewRound3ClaudeBridgeAndBootstrapShareCancellation(t *testing.T) {
	host := NewForTest(nil)
	adapter := newCurrentExecutorAdapterForTest(host, "review-claude", &fakeExecutor{identifier: "plugin-provider"}, []sdktranslator.Format{sdktranslator.FormatClaude}, []sdktranslator.Format{sdktranslator.FormatClaude})
	cancels := make(chan pluginapi.CancelExecutionRequest, 4)
	adapter.canceller = executionCancellerFunc(func(_ context.Context, r pluginapi.CancelExecutionRequest) error { cancels <- r; return nil })
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := pluginapi.ExecutorRequest{RequestID: "review-bridge-overlap", ExecutionSessionID: "review-session"}
	shared := adapter.pluginCancelOnce(streamCtx, req)
	stopBootstrap := adapter.watchExecutionCancellationShared(streamCtx, req, shared)
	defer stopBootstrap()
	prepared := preparedExecutorCall{outputFormat: sdktranslator.FormatClaude, requestedFormat: sdktranslator.FormatOpenAIResponse}
	out := adapter.translateClaudePluginStream(context.Background(), cancel, req, prepared, nil, nil, shared)
	for range out {
	}
	stopBootstrap()
	select {
	case <-cancels:
	case <-time.After(time.Second):
		t.Fatal("no cancellation")
	}
	select {
	case r := <-cancels:
		t.Fatalf("duplicate CancelExecution across bootstrap/Claude bridge: %s", r.RequestID)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestReviewRound3ClaudeExecuteStreamCancelsOnce(t *testing.T) {
	headers := make(map[string][]string)
	for i := 0; i < 4096; i++ {
		headers["X-Review-"+strconv.Itoa(i)] = []string{"synthetic"}
	}
	for i := 0; i < 10; i++ {
		cancels := make(chan pluginapi.CancelExecutionRequest, 4)
		adapter := newCurrentExecutorAdapterForTest(NewForTest(nil), "review-claude-execute", &fakeExecutor{identifier: "plugin-provider", executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			in := make(chan pluginapi.ExecutorStreamChunk, 1)
			in <- pluginapi.ExecutorStreamChunk{Err: errors.New("synthetic upstream error")}
			return pluginapi.ExecutorStreamResponse{Chunks: in, Headers: headers}, nil
		}}, []sdktranslator.Format{sdktranslator.FormatClaude}, []sdktranslator.Format{sdktranslator.FormatClaude})
		adapter.canceller = executionCancellerFunc(func(_ context.Context, r pluginapi.CancelExecutionRequest) error { cancels <- r; return nil })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := adapter.ExecuteStream(ctx, nil, coreexecutor.Request{Model: "fixture", Payload: []byte(`{"input":"hello"}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true, Metadata: map[string]any{coreexecutor.RequestIDMetadataKey: "review-execute"}})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		for range result.Chunks {
		}
		select {
		case <-cancels:
		case <-ctx.Done():
			cancel()
			t.Fatal("no cancellation")
		}
		select {
		case r := <-cancels:
			cancel()
			t.Fatalf("iteration %d: duplicate cancellation from actual Claude ExecuteStream: %s", i, r.RequestID)
		case <-time.After(2 * time.Millisecond):
		}
		cancel()
	}
}
