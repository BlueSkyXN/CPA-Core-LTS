package auth

import (
	"context"
	"net/http"
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

type retryWithoutPenaltyExecutor struct {
	mu          sync.Mutex
	calls       int
	streamCalls int
	alwaysError bool
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
		return cliproxyexecutor.Response{}, retryWithoutPenaltyTestError{}
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
		ch <- cliproxyexecutor.StreamChunk{Err: retryWithoutPenaltyTestError{}}
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
	return cliproxyexecutor.Response{}, retryWithoutPenaltyTestError{}
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

func newRetryWithoutPenaltyTestManager(t *testing.T, alwaysError bool) (*Manager, *retryWithoutPenaltyExecutor, string) {
	t.Helper()

	manager := NewManager(nil, nil, nil)
	executor := &retryWithoutPenaltyExecutor{alwaysError: alwaysError}
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
