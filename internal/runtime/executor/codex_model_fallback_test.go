package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func codexFallbackTestOptions(sourceModel, mode string) cliproxyexecutor.Options {
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Metadata: map[string]any{
			cliproxyexecutor.CodexModelFallbackSourceModelMetadataKey:         sourceModel,
			cliproxyexecutor.CodexModelFallbackReasoningContinuityMetadataKey: mode,
		},
	}
	if mode == config.CodexModelFallbackReasoningContinuityContextReset {
		opts.Metadata[cliproxyexecutor.CodexModelFallbackContextResetReplayMetadataKey] = true
	}
	return opts
}

func newCodexFallbackExecutorTestServer(t *testing.T, bodies *[][]byte, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("ReadAll() error = %v", errRead)
		}
		*bodies = append(*bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fallback\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"gpt-target\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
}

func TestCodexExecutorModelFallbackSameModelOnlyBlocksClientReasoning(t *testing.T) {
	var calls atomic.Int32
	var bodies [][]byte
	server := newCodexFallbackExecutorTestServer(t, &bodies, &calls)
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	signature := validCodexReasoningEncryptedContentForTestSeed(31)
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-fallback-block-client",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-target",
		Payload: []byte(`{"model":"gpt-source","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"` + signature + `"}]},{"role":"user","content":[{"type":"text","text":"continue"}]}]}`),
	}, codexFallbackTestOptions("gpt-source", config.CodexModelFallbackReasoningContinuitySameModelOnly))
	if err == nil || !isCodexModelFallbackBlockedError(err) {
		t.Fatalf("Execute() error = %v, want model-fallback continuity block", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestCodexExecutorModelFallbackBlockedStreamDoesNotPublishFailureUsage(t *testing.T) {
	recorder := &codexAbnormalReasoningRetryUsageRecorder{}
	usage.RegisterNamedPlugin("codex-model-fallback-blocked-stream-usage", recorder)
	t.Cleanup(func() { usage.RegisterNamedPlugin("codex-model-fallback-blocked-stream-usage", noopUsagePlugin{}) })

	executor := NewCodexExecutor(&config.Config{})
	signature := validCodexReasoningEncryptedContentForTestSeed(34)
	_, err := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{ID: "auth-fallback-stream-block"}, cliproxyexecutor.Request{
		Model:   "gpt-target",
		Payload: []byte(`{"model":"gpt-source","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"` + signature + `"}]}]}`),
	}, codexFallbackTestOptions("gpt-source", config.CodexModelFallbackReasoningContinuitySameModelOnly))
	if err == nil || !isCodexModelFallbackBlockedError(err) {
		t.Fatalf("ExecuteStream() error = %v, want fallback continuity block", err)
	}
	// A blocked fallback has no upstream dispatch. Give the async default usage
	// sink a scheduling opportunity, then prove no record for this auth was
	// emitted. The named plugin also observes unrelated parallel package tests.
	time.Sleep(100 * time.Millisecond)
	recorder.mu.Lock()
	got := 0
	for _, record := range recorder.records {
		if record.AuthID == "auth-fallback-stream-block" {
			got++
		}
	}
	recorder.mu.Unlock()
	if got != 0 {
		t.Fatalf("usage records = %d, want zero for blocked stream fallback", got)
	}
}

func TestCodexExecutorModelFallbackSameModelOnlyBlocksSourceReplayCache(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
	signature := validCodexReasoningEncryptedContentForTestSeed(32)
	internalcache.CacheCodexReasoningReplayItem("gpt-source", "claude:session-fallback-source:agent:main", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+signature+`"}`))

	var calls atomic.Int32
	var bodies [][]byte
	server := newCodexFallbackExecutorTestServer(t, &bodies, &calls)
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-fallback-block-cache",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-target",
		Payload: []byte(`{"model":"gpt-source","metadata":{"user_id":"{\"device_id\":\"device\",\"account_uuid\":\"\",\"session_id\":\"session-fallback-source\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"continue"}]}]}`),
	}, codexFallbackTestOptions("gpt-source", config.CodexModelFallbackReasoningContinuitySameModelOnly))
	if err == nil || !isCodexModelFallbackBlockedError(err) {
		t.Fatalf("Execute() error = %v, want cached continuity block", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
}

func TestCodexExecutorModelFallbackContextResetDropsReasoningAndKeepsToolPair(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
	signature := validCodexReasoningEncryptedContentForTestSeed(33)
	internalcache.CacheCodexReasoningReplayItems("gpt-source", "claude:session-fallback-reset:agent:main", [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + signature + `"}`),
		[]byte(`{"type":"function_call","call_id":"call_reset","name":"lookup","arguments":"{\"q\":\"x\"}"}`),
	})

	var calls atomic.Int32
	var bodies [][]byte
	server := newCodexFallbackExecutorTestServer(t, &bodies, &calls)
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-fallback-reset",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-target",
		Payload: []byte(`{"model":"gpt-source","metadata":{"user_id":"{\"device_id\":\"device\",\"account_uuid\":\"\",\"session_id\":\"session-fallback-reset\"}"},"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_reset","content":"ok"}]}]}`),
	}, codexFallbackTestOptions("gpt-source", config.CodexModelFallbackReasoningContinuityContextReset))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := calls.Load(); got != 1 || len(bodies) != 1 {
		t.Fatalf("upstream calls/bodies = %d/%d, want 1/1", got, len(bodies))
	}
	input := gjson.GetBytes(bodies[0], "input")
	if !input.IsArray() {
		t.Fatalf("input is not array: %s", bodies[0])
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "reasoning" {
			t.Fatalf("context-reset request retained reasoning item: %s", bodies[0])
		}
	}
	if got := gjson.GetBytes(bodies[0], "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call; body=%s", got, bodies[0])
	}
	if got := gjson.GetBytes(bodies[0], "input.1.type").String(); got != "function_call_output" {
		t.Fatalf("input.1.type = %q, want function_call_output; body=%s", got, bodies[0])
	}
}

func TestPrepareCodexModelFallbackContextResetDropsPreviousResponseID(t *testing.T) {
	opts := codexFallbackTestOptions("gpt-source", config.CodexModelFallbackReasoningContinuityContextReset)
	body := []byte(`{"model":"gpt-target","previous_response_id":"resp-source","input":[{"type":"message","role":"user","content":"replay the visible transcript"}]}`)
	updated, _, skipReplay, err := prepareCodexModelFallbackBody(context.Background(), sdktranslator.FromString("openai"), cliproxyexecutor.Request{
		Model:   "gpt-target",
		Payload: body,
	}, opts, body)
	if err != nil || !skipReplay {
		t.Fatalf("prepare error/skip = %v/%v, want safe reset", err, skipReplay)
	}
	if gjson.GetBytes(updated, "previous_response_id").Exists() {
		t.Fatalf("context-reset retained previous_response_id: %s", updated)
	}
}

func TestPrepareCodexModelFallbackContextResetRejectsUnattestedPreviousResponseID(t *testing.T) {
	opts := codexFallbackTestOptions("gpt-source", config.CodexModelFallbackReasoningContinuityContextReset)
	delete(opts.Metadata, cliproxyexecutor.CodexModelFallbackContextResetReplayMetadataKey)
	body := []byte(`{"model":"gpt-target","previous_response_id":"resp-source","input":[{"type":"message","role":"user","content":"incremental"}]}`)
	_, _, _, err := prepareCodexModelFallbackBody(context.Background(), sdktranslator.FromString("openai"), cliproxyexecutor.Request{Model: "gpt-target", Payload: body}, opts, body)
	if err == nil || !isCodexModelFallbackBlockedError(err) {
		t.Fatalf("prepare error = %v, want continuity block", err)
	}
}

func TestCodexExecutorModelFallbackWithoutReasoningReachesTarget(t *testing.T) {
	var calls atomic.Int32
	var bodies [][]byte
	server := newCodexFallbackExecutorTestServer(t, &bodies, &calls)
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-fallback-stateless",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-target",
		Payload: []byte(`{"model":"gpt-source","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, codexFallbackTestOptions("gpt-source", config.CodexModelFallbackReasoningContinuitySameModelOnly))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := calls.Load(); got != 1 || len(bodies) != 1 {
		t.Fatalf("upstream calls/bodies = %d/%d, want 1/1", got, len(bodies))
	}
	if got := gjson.GetBytes(bodies[0], "model").String(); got != "gpt-target" {
		t.Fatalf("upstream model = %q, want gpt-target; body=%s", got, bodies[0])
	}
}
