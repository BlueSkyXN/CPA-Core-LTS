package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type retryAdmissionTestExecutor struct {
	mode           string
	admissionCalls int
	executeCalls   int
	countCalls     int
	streamCalls    int
	refreshCalls   int
}

func (*retryAdmissionTestExecutor) Identifier() string { return "retry-admission" }

func (e *retryAdmissionTestExecutor) AdmitExecution(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (context.Context, error) {
	e.admissionCalls++
	if e.admissionCalls == 2 {
		return ctx, &Error{Code: ErrorCodeConnectionLifecycle, Message: "retry readiness rejected"}
	}
	return ctx, nil
}

func (e *retryAdmissionTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls++
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
}

func (e *retryAdmissionTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls++
	unauthorized := &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
	if e.mode == "stream_bootstrap" {
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Err: unauthorized}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	return nil, unauthorized
}

func (e *retryAdmissionTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls++
	updated := auth.Clone()
	updated.Metadata["access_token"] = "fresh-access-token"
	return updated, nil
}

func (e *retryAdmissionTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls++
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired access token"}
}

func (*retryAdmissionTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type retryAdmissionResultHook struct {
	results []Result
}

type cancelingAdmissionTestExecutor struct {
	retryAdmissionTestExecutor
	cancel context.CancelFunc
}

func (e *cancelingAdmissionTestExecutor) AdmitExecution(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (context.Context, error) {
	e.admissionCalls++
	e.cancel()
	return ctx, nil
}

func (e *cancelingAdmissionTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls++
	return cliproxyexecutor.Response{}, nil
}

func (*retryAdmissionResultHook) OnAuthRegistered(context.Context, *Auth) {}
func (*retryAdmissionResultHook) OnAuthUpdated(context.Context, *Auth)    {}
func (h *retryAdmissionResultHook) OnResult(_ context.Context, result Result) {
	h.results = append(h.results, result)
}

func TestManagerRefreshRetryRequiresFreshPreDispatchAdmission(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		run       func(*Manager, cliproxyexecutor.Options) error
		callCount func(*retryAdmissionTestExecutor) int
	}{
		{
			name: "execute",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errExecute := manager.Execute(context.Background(), []string{"retry-admission"}, cliproxyexecutor.Request{}, opts)
				return errExecute
			},
			callCount: func(executor *retryAdmissionTestExecutor) int { return executor.executeCalls },
		},
		{
			name: "count_tokens",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{"retry-admission"}, cliproxyexecutor.Request{}, opts)
				return errCount
			},
			callCount: func(executor *retryAdmissionTestExecutor) int { return executor.countCalls },
		},
		{
			name: "stream_immediate",
			mode: "stream_immediate",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errStream := manager.ExecuteStream(context.Background(), []string{"retry-admission"}, cliproxyexecutor.Request{}, opts)
				return errStream
			},
			callCount: func(executor *retryAdmissionTestExecutor) int { return executor.streamCalls },
		},
		{
			name: "stream_bootstrap",
			mode: "stream_bootstrap",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, errStream := manager.ExecuteStream(context.Background(), []string{"retry-admission"}, cliproxyexecutor.Request{}, opts)
				return errStream
			},
			callCount: func(executor *retryAdmissionTestExecutor) int { return executor.streamCalls },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &retryAdmissionTestExecutor{mode: tt.mode}
			hook := &retryAdmissionResultHook{}
			manager := NewManager(nil, nil, hook)
			manager.RegisterExecutor(executor)
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       "retry-auth",
				Provider: "retry-admission",
				Status:   StatusActive,
				Metadata: map[string]any{
					"access_token":  "stale-access-token",
					"refresh_token": "refresh-token",
				},
			}); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			selected := 0
			dispatched := 0
			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(string) { selected++ },
				codexModelFallbackDispatchMetadataKey:            func(string) { dispatched++ },
			}}

			if errRun := tt.run(manager, opts); errRun == nil {
				t.Fatal("execution error = nil, want retry admission rejection")
			}
			if executor.admissionCalls != 2 {
				t.Fatalf("admission calls = %d, want one per attempted invocation", executor.admissionCalls)
			}
			if got := tt.callCount(executor); got != 1 {
				t.Fatalf("executor calls = %d, want retry blocked before second invocation", got)
			}
			if executor.refreshCalls != 1 {
				t.Fatalf("refresh calls = %d, want 1", executor.refreshCalls)
			}
			if selected != 1 {
				t.Fatalf("selected-auth callbacks = %d, want only the initial dispatched attempt", selected)
			}
			if tt.name != "count_tokens" && dispatched != 1 {
				t.Fatalf("dispatch callbacks = %d, want only the initial dispatched attempt", dispatched)
			}
			if len(hook.results) != 0 {
				t.Fatalf("result hook calls = %#v, want no result for rejected redispatch", hook.results)
			}
		})
	}
}

func TestManagerAdmissionCancellationDoesNotPublishOrInvoke(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	executor := &cancelingAdmissionTestExecutor{cancel: cancel}
	hook := &retryAdmissionResultHook{}
	manager := NewManager(nil, nil, hook)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "cancel-auth", Provider: executor.Identifier(), Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	selected := 0
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(string) { selected++ },
	}}

	_, errExecute := manager.Execute(ctx, []string{executor.Identifier()}, cliproxyexecutor.Request{}, opts)
	if errExecute != context.Canceled {
		t.Fatalf("Execute() error = %v, want context.Canceled", errExecute)
	}
	if executor.admissionCalls != 1 || executor.executeCalls != 0 {
		t.Fatalf("calls = admission:%d execute:%d, want 1/0", executor.admissionCalls, executor.executeCalls)
	}
	if selected != 0 || len(hook.results) != 0 {
		t.Fatalf("pre-dispatch publication = selected:%d results:%#v, want none", selected, hook.results)
	}
}
