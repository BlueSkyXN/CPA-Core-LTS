package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const codexSelfHealInvalidSignatureSSE = `data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"message":"Invalid signature in thinking block","type":"invalid_request_error","code":"invalid_request_error"}}}` + "\n\n"

func selfHealTestAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: "auth-replay-self-heal",
		Attributes: map[string]string{
			"base_url": serverURL,
			"api_key":  "test",
		},
	}
}

func selfHealTestRequest(sessionID string) cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"` + sessionID + `\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}
}

func selfHealOptions(stream bool) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       stream,
	}
}

func primeSelfHealReplayCache(t *testing.T, sessionKey string) string {
	t.Helper()
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
	cached := validCodexReasoningEncryptedContentForTestSeed(21)
	internalcache.CacheCodexReasoningReplayItem("gpt-5.4", sessionKey, []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+cached+`"}`))
	return cached
}

// A signature rejection of CPA-injected replay must be retried once without
// the injection: the second upstream request omits the cached reasoning item
// and succeeds.
func TestCodexReplaySelfHealRetriesOnceWithoutInjectionNonStream(t *testing.T) {
	const sessionKey = "claude:session-self-heal-nonstream:agent:main"
	primeSelfHealReplayCache(t, sessionKey)

	var requestCount int32
	var bodies [][]byte
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		<-mu
		bodies = append(bodies, body)
		requestCount++
		mu <- struct{}{}
		if atomic.LoadInt32(&requestCount) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(codexSelfHealInvalidSignatureSSE))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, err := executor.Execute(context.Background(), selfHealTestAuth(server.URL), selfHealTestRequest("session-self-heal-nonstream"), selfHealOptions(false))
	if err != nil {
		t.Fatalf("self-heal retry should succeed: %v", err)
	}
	if got := atomic.LoadInt32(&requestCount); got != 2 {
		t.Fatalf("upstream request count = %d, want 2 (original + self-heal)", got)
	}
	<-mu
	if len(bodies) != 2 {
		t.Fatalf("captured bodies = %d, want 2", len(bodies))
	}
	firstHasReplay := strings.Contains(string(bodies[0]), `"type":"reasoning"`)
	secondHasReplay := strings.Contains(string(bodies[1]), `"type":"reasoning"`)
	if !firstHasReplay {
		t.Fatalf("first attempt should carry injected replay item; body=%s", string(bodies[0]))
	}
	if secondHasReplay {
		t.Fatalf("self-heal retry must not re-inject replay item; body=%s", string(bodies[1]))
	}
	_ = resp
	// The poisoned entry stays cleared after a successful retry.
	if _, ok := internalcache.GetCodexReasoningReplayItem("gpt-5.4", sessionKey); ok {
		t.Fatal("poisoned replay entry should remain cleared")
	}
}

// A retry that fails again keeps the error: exactly one extra attempt total.
func TestCodexReplaySelfHealRetryFailsOnlyOnce(t *testing.T) {
	const sessionKey = "claude:session-self-heal-fail:agent:main"
	primeSelfHealReplayCache(t, sessionKey)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSelfHealInvalidSignatureSSE))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), selfHealTestAuth(server.URL), selfHealTestRequest("session-self-heal-fail"), selfHealOptions(false))
	if err == nil {
		t.Fatal("expected error when retry also fails")
	}
	if !strings.Contains(err.Error(), "Invalid signature") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&requestCount); got != 2 {
		t.Fatalf("upstream request count = %d, want exactly 2 (no second retry)", got)
	}
}

// Without a CPA replay injection the signature rejection surfaces directly:
// client-provided ciphertext is never stripped or retried.
func TestCodexReplaySelfHealSkippedWithoutInjection(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSelfHealInvalidSignatureSSE))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), selfHealTestAuth(server.URL), selfHealTestRequest("session-no-injection"), selfHealOptions(false))
	if err == nil {
		t.Fatal("expected signature error without self-heal")
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("upstream request count = %d, want 1 (no retry without injection)", got)
	}
}

// The streaming counterpart heals inside the bootstrap scan, before any chunk
// is delivered to the client.
func TestCodexReplaySelfHealStreamRetriesOnceWithoutInjection(t *testing.T) {
	const sessionKey = "claude:session-self-heal-stream:agent:main"
	primeSelfHealReplayCache(t, sessionKey)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		count := atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			if !strings.Contains(string(body), `"type":"reasoning"`) {
				t.Errorf("first stream attempt should carry injected replay item")
			}
			_, _ = w.Write([]byte(codexSelfHealInvalidSignatureSSE))
			return
		}
		if strings.Contains(string(body), `"type":"reasoning"`) {
			t.Errorf("stream self-heal retry must not re-inject replay item")
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: true}})
	result, err := executor.ExecuteStream(context.Background(), selfHealTestAuth(server.URL), selfHealTestRequest("session-self-heal-stream"), selfHealOptions(true))
	if err != nil {
		t.Fatalf("stream self-heal retry should succeed: %v", err)
	}
	sawPayload := false
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
		if len(chunk.Payload) > 0 {
			sawPayload = true
		}
	}
	if streamErr != nil {
		t.Fatalf("stream should complete without error: %v", streamErr)
	}
	if !sawPayload {
		t.Fatal("stream should deliver a payload after self-heal")
	}
	if got := atomic.LoadInt32(&requestCount); got != 2 {
		t.Fatalf("upstream request count = %d, want 2", got)
	}
}

// Review fix P1: a successful self-heal must still publish the first attempt's
// failure usage. Two upstream attempts yield one failed record plus one
// success record, not a lone success.
func TestCodexReplaySelfHealPublishesFirstAttemptFailureUsage(t *testing.T) {
	const sessionKey = "claude:session-self-heal-usage:agent:main"
	primeSelfHealReplayCache(t, sessionKey)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		count := atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			_, _ = w.Write([]byte(codexSelfHealInvalidSignatureSSE))
			return
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
	}))
	defer server.Close()

	alias := "self-heal-usage-alias"
	capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 8)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{}) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ctx = coreusage.WithRequestedModelAlias(ctx, alias)
	ctx = coreusage.WithTraceID(ctx, alias)

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(ctx, selfHealTestAuth(server.URL), selfHealTestRequest("session-self-heal-usage"), selfHealOptions(false))
	if err != nil {
		t.Fatalf("self-heal retry should succeed: %v", err)
	}
	if got := atomic.LoadInt32(&requestCount); got != 2 {
		t.Fatalf("upstream request count = %d, want 2", got)
	}
	coreusage.PublishRecord(ctx, coreusage.Record{Alias: alias, Model: "dispatch-barrier"})
	failedSeen := false
	successSeen := false
	for drain := 0; drain < 8; drain++ {
		record := capture.await(t)
		if record.Model == "dispatch-barrier" {
			break
		}
		if record.Failed {
			failedSeen = true
		} else {
			successSeen = true
		}
	}
	if !failedSeen || !successSeen {
		t.Fatalf("usage records: failed=%v success=%v, want both (first attempt failure + retry success)", failedSeen, successSeen)
	}
}

// Review fix P2a: the streaming bootstrap self-heal must clear the poisoned
// cache entry before retrying; after a healed stream the entry stays deleted.
func TestCodexReplaySelfHealStreamClearsPoisonedCache(t *testing.T) {
	const sessionKey = "claude:session-self-heal-stream-clear:agent:main"
	primeSelfHealReplayCache(t, sessionKey)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		count := atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			_, _ = w.Write([]byte(codexSelfHealInvalidSignatureSSE))
			return
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.4","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: true}})
	result, err := executor.ExecuteStream(context.Background(), selfHealTestAuth(server.URL), selfHealTestRequest("session-self-heal-stream-clear"), selfHealOptions(true))
	if err != nil {
		t.Fatalf("stream self-heal retry should succeed: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream should complete without error: %v", chunk.Err)
		}
	}
	if _, ok := internalcache.GetCodexReasoningReplayItem("gpt-5.4", sessionKey); ok {
		t.Fatal("poisoned replay entry should be cleared before the bootstrap self-heal retry")
	}
}

// Review fix P2b: the self-heal retry skips injecting old replay content but
// must keep storing fresh replay items from the successful retry response.
func TestCodexReplaySelfHealStoresFreshReplayAfterRetry(t *testing.T) {
	const sessionKey = "claude:session-self-heal-store:agent:main"
	primeSelfHealReplayCache(t, sessionKey)
	freshEncrypted := validCodexReasoningEncryptedContentForTestSeed(22)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		count := atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid signature in thinking block","type":"invalid_request_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_fresh","type":"reasoning","summary":[],"encrypted_content":"` + freshEncrypted + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.4","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), selfHealTestAuth(server.URL), selfHealTestRequest("session-self-heal-store"), selfHealOptions(false))
	if err != nil {
		t.Fatalf("self-heal retry should succeed: %v", err)
	}
	item, ok := internalcache.GetCodexReasoningReplayItem("gpt-5.4", sessionKey)
	if !ok {
		t.Fatal("fresh replay content from the retry response should be stored")
	}
	if !strings.Contains(string(item), freshEncrypted) {
		t.Fatalf("stored replay should carry the fresh encrypted content; item=%s", string(item))
	}
}

// Review fix P2c: a cache entry that the injection filters out completely
// (client history already carries the reasoning) is not an injection. A
// signature rejection then surfaces directly without a second attempt.
func TestCodexReplaySelfHealSkipsWhenInjectionFilteredOut(t *testing.T) {
	const sessionKey = "claude:session-self-heal-filtered:agent:main"
	primeSelfHealReplayCache(t, sessionKey)

	// Client history already contains the same reasoning item, so the injection
	// helper deduplicates it away and nothing proxy-owned is added.
	cached := validCodexReasoningEncryptedContentForTestSeed(21)
	request := cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-self-heal-filtered\"}"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"first"}]},` +
			`{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":"` + cached + `"},{"type":"text","text":"answer"}]},` +
			`{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSelfHealInvalidSignatureSSE))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), selfHealTestAuth(server.URL), request, selfHealOptions(false))
	if err == nil {
		t.Fatal("expected signature error without self-heal")
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("upstream request count = %d, want 1 (filtered injection must not retry)", got)
	}
}
