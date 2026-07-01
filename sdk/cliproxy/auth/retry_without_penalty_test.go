package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type retryWithoutPenaltyTestError struct{}

func (retryWithoutPenaltyTestError) Error() string {
	return "retry without penalty"
}

func (retryWithoutPenaltyTestError) RetryWithoutPenalty() bool {
	return true
}

type retryWithoutPenaltyClassLimitTestError struct {
	class             string
	maxRetries        int
	exhaustedBehavior string
	fallbackPayload   []byte
	fallbackChunks    []cliproxyexecutor.StreamChunk
}

func (e retryWithoutPenaltyClassLimitTestError) Error() string {
	return "codex_abnormal_reasoning_response: retry without penalty class limit"
}

func (e retryWithoutPenaltyClassLimitTestError) RetryWithoutPenalty() bool {
	return true
}

func (e retryWithoutPenaltyClassLimitTestError) RetryWithoutPenaltyClass() string {
	if e.class != "" {
		return e.class
	}
	return "codex.abnormal-reasoning-retry"
}

func (e retryWithoutPenaltyClassLimitTestError) RetryWithoutPenaltyMaxRetries() int {
	return e.maxRetries
}

func (e retryWithoutPenaltyClassLimitTestError) RetryWithoutPenaltyExhaustedBehavior() string {
	return e.exhaustedBehavior
}

func (e retryWithoutPenaltyClassLimitTestError) RetryWithoutPenaltyFallbackResponse() (cliproxyexecutor.Response, bool) {
	if len(e.fallbackPayload) == 0 {
		return cliproxyexecutor.Response{}, false
	}
	return cliproxyexecutor.Response{
		Payload: e.fallbackPayload,
		Headers: http.Header{
			"X-Fallback": []string{"response"},
		},
	}, true
}

func (e retryWithoutPenaltyClassLimitTestError) RetryWithoutPenaltyFallbackStreamChunks() (http.Header, []cliproxyexecutor.StreamChunk, bool) {
	if len(e.fallbackChunks) == 0 {
		return nil, nil, false
	}
	return http.Header{"X-Fallback": []string{"stream"}}, e.fallbackChunks, true
}

type retryWithoutPenaltyExecutor struct {
	mu          sync.Mutex
	calls       int
	streamCalls int
	alwaysError bool
	retryErr    error
	afterFirst  error
}

func (e *retryWithoutPenaltyExecutor) Identifier() string {
	return "codex"
}

func (e *retryWithoutPenaltyExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	alwaysError := e.alwaysError
	e.mu.Unlock()
	if alwaysError || call == 1 {
		return cliproxyexecutor.Response{}, e.errorForRetry()
	}
	if e.afterFirst != nil {
		return cliproxyexecutor.Response{}, e.afterFirst
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *retryWithoutPenaltyExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls++
	call := e.streamCalls
	alwaysError := e.alwaysError
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if alwaysError || call == 1 {
		ch <- cliproxyexecutor.StreamChunk{Err: e.errorForRetry()}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	if e.afterFirst != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: e.afterFirst}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *retryWithoutPenaltyExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *retryWithoutPenaltyExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.errorForRetry()
}

func (e *retryWithoutPenaltyExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *retryWithoutPenaltyExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *retryWithoutPenaltyExecutor) StreamCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.streamCalls
}

func (e *retryWithoutPenaltyExecutor) errorForRetry() error {
	if e.retryErr != nil {
		return e.retryErr
	}
	return retryWithoutPenaltyTestError{}
}

func TestManagerExecute_RetryWithoutPenaltyConsumesRequestRetryWithoutAuthPenalty(t *testing.T) {
	manager, executor, authID := newRetryWithoutPenaltyTestManager(t, false)
	manager.SetRetryConfig(1, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(resp.Payload))
	}
	if calls := executor.Calls(); calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	assertAuthNoPenaltyState(t, manager, authID, 1, 0)
}

func TestManagerExecuteStream_RetryWithoutPenaltyConsumesRequestRetryWithoutAuthPenalty(t *testing.T) {
	manager, executor, authID := newRetryWithoutPenaltyTestManager(t, false)
	manager.SetRetryConfig(1, 0, 0)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil", err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(payload))
	}
	if calls := executor.StreamCalls(); calls != 2 {
		t.Fatalf("stream calls = %d, want 2", calls)
	}
	assertAuthNoPenaltyState(t, manager, authID, 1, 0)
}

func TestManagerExecute_RetryWithoutPenaltyBudgetExceededDoesNotMarkFailure(t *testing.T) {
	manager, executor, authID := newRetryWithoutPenaltyTestManager(t, true)
	manager.SetRetryConfig(0, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want retry error")
	}
	if !isRetryWithoutPenaltyError(err) {
		t.Fatalf("error = %T %v, want retry without penalty", err, err)
	}
	if calls := executor.Calls(); calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	assertAuthNoPenaltyState(t, manager, authID, 0, 0)
}

func TestManagerExecute_RetryWithoutPenaltyClassLimitExhaustedDoesNotMarkFailure(t *testing.T) {
	retryErr := retryWithoutPenaltyClassLimitTestError{maxRetries: 1}
	manager, executor, authID := newRetryWithoutPenaltyTestManagerWithError(t, true, retryErr, nil)
	manager.SetRetryConfig(3, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	assertRetryWithoutPenaltyExhausted(t, err, "codex_abnormal_reasoning_retry_exhausted")
	if isRetryWithoutPenaltyError(err) {
		t.Fatalf("terminal exhausted error = %T %v, want not RetryWithoutPenalty", err, err)
	}
	if calls := executor.Calls(); calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	assertAuthNoPenaltyState(t, manager, authID, 0, 0)
}

func TestManagerExecute_RetryWithoutPenaltyClassMaxRetriesZeroDoesNotRetryOrMarkFailure(t *testing.T) {
	retryErr := retryWithoutPenaltyClassLimitTestError{maxRetries: 0}
	manager, executor, authID := newRetryWithoutPenaltyTestManagerWithError(t, true, retryErr, nil)
	manager.SetRetryConfig(3, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	assertRetryWithoutPenaltyExhausted(t, err, "codex_abnormal_reasoning_retry_exhausted")
	if calls := executor.Calls(); calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	assertAuthNoPenaltyState(t, manager, authID, 0, 0)
}

func TestManagerExecute_RetryWithoutPenaltyClassLimitIgnoresGlobalRequestRetry(t *testing.T) {
	retryErr := retryWithoutPenaltyClassLimitTestError{maxRetries: 1}
	manager, executor, authID := newRetryWithoutPenaltyTestManagerWithError(t, false, retryErr, nil)
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil", err)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(resp.Payload))
	}
	if calls := executor.Calls(); calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	assertAuthNoPenaltyState(t, manager, authID, 1, 0)
}

func TestManagerExecute_RetryWithoutPenaltyPassThroughOnClassLimitExhausted(t *testing.T) {
	retryErr := retryWithoutPenaltyClassLimitTestError{
		maxRetries:        0,
		exhaustedBehavior: retryWithoutPenaltyExhaustedBehaviorPassThrough,
		fallbackPayload:   []byte("abnormal"),
	}
	manager, executor, authID := newRetryWithoutPenaltyTestManagerWithError(t, true, retryErr, nil)
	manager.SetRetryConfig(0, 0, 0)

	resp, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want nil pass-through", err)
	}
	if string(resp.Payload) != "abnormal" {
		t.Fatalf("payload = %q, want abnormal", string(resp.Payload))
	}
	if got := resp.Headers.Get("X-Fallback"); got != "response" {
		t.Fatalf("fallback header = %q, want response", got)
	}
	if calls := executor.Calls(); calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	assertAuthNoPenaltyState(t, manager, authID, 0, 0)
}

func TestManagerExecuteStream_RetryWithoutPenaltyPassThroughOnClassLimitExhausted(t *testing.T) {
	retryErr := retryWithoutPenaltyClassLimitTestError{
		maxRetries:        0,
		exhaustedBehavior: retryWithoutPenaltyExhaustedBehaviorPassThrough,
		fallbackChunks: []cliproxyexecutor.StreamChunk{
			{Payload: []byte("abnormal")},
		},
	}
	manager, executor, authID := newRetryWithoutPenaltyTestManagerWithError(t, true, retryErr, nil)
	manager.SetRetryConfig(0, 0, 0)

	result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream error = %v, want nil pass-through", err)
	}
	if got := result.Headers.Get("X-Fallback"); got != "stream" {
		t.Fatalf("fallback header = %q, want stream", got)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want nil", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "abnormal" {
		t.Fatalf("payload = %q, want abnormal", string(payload))
	}
	if calls := executor.StreamCalls(); calls != 1 {
		t.Fatalf("stream calls = %d, want 1", calls)
	}
	assertAuthNoPenaltyState(t, manager, authID, 0, 0)
}

func TestManagerExecute_RetryWithoutPenaltyThenProviderErrorPreservesTerminalProviderError(t *testing.T) {
	retryErr := retryWithoutPenaltyClassLimitTestError{maxRetries: 3}
	upstreamErr := &Error{Code: "rate_limit", Message: "quota", HTTPStatus: http.StatusTooManyRequests}
	manager, executor, _ := newRetryWithoutPenaltyTestManagerWithError(t, false, retryErr, upstreamErr)
	manager.SetRetryConfig(3, 0, 0)

	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if !errors.Is(err, upstreamErr) && err != upstreamErr {
		t.Fatalf("error = %T %v, want upstream error", err, err)
	}
	if strings.Contains(err.Error(), "codex_abnormal_reasoning_retry_exhausted") {
		t.Fatalf("error = %v, want provider terminal error not abnormal exhausted wrapper", err)
	}
	if calls := executor.Calls(); calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func newRetryWithoutPenaltyTestManager(t *testing.T, alwaysError bool) (*Manager, *retryWithoutPenaltyExecutor, string) {
	return newRetryWithoutPenaltyTestManagerWithError(t, alwaysError, nil, nil)
}

func newRetryWithoutPenaltyTestManagerWithError(t *testing.T, alwaysError bool, retryErr error, afterFirst error) (*Manager, *retryWithoutPenaltyExecutor, string) {
	t.Helper()

	manager := NewManager(nil, nil, nil)
	executor := &retryWithoutPenaltyExecutor{alwaysError: alwaysError, retryErr: retryErr, afterFirst: afterFirst}
	manager.RegisterExecutor(executor)

	authID := uuid.NewString()
	auth := &Auth{ID: authID, Provider: "codex"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	return manager, executor, authID
}

func assertRetryWithoutPenaltyExhausted(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want retry without penalty exhausted error")
	}
	se, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error = %T %v, want StatusCode()", err, err)
	}
	if got := se.StatusCode(); got != http.StatusBadGateway {
		t.Fatalf("StatusCode() = %d, want %d", got, http.StatusBadGateway)
	}
	if !strings.Contains(err.Error(), wantCode) {
		t.Fatalf("error = %v, want code %q", err, wantCode)
	}
	if strings.Contains(err.Error(), "codex_abnormal_reasoning_response") {
		t.Fatalf("terminal error = %v, want terminal code not attempt usage reason", err)
	}
}

func assertAuthNoPenaltyState(t *testing.T, manager *Manager, authID string, wantSuccess, wantFailed int64) {
	t.Helper()

	manager.mu.RLock()
	auth := manager.auths[authID]
	manager.mu.RUnlock()
	if auth == nil {
		t.Fatalf("auth %s not found", authID)
	}
	if auth.Success != wantSuccess {
		t.Fatalf("auth.Success = %d, want %d", auth.Success, wantSuccess)
	}
	if auth.Failed != wantFailed {
		t.Fatalf("auth.Failed = %d, want %d", auth.Failed, wantFailed)
	}
	if auth.LastError != nil {
		t.Fatalf("auth.LastError = %#v, want nil", auth.LastError)
	}
	if auth.Unavailable {
		t.Fatal("auth.Unavailable = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
	if auth.Quota.Exceeded {
		t.Fatalf("auth.Quota.Exceeded = true, want false")
	}
}
