package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type xaiBadCredentialsRefreshExecutor struct {
	mu            sync.Mutex
	executeCalls  []string
	refreshCalls  int
	refreshFail   bool
	refreshTokens map[string]string
	forbiddenText string
}

func (*xaiBadCredentialsRefreshExecutor) Identifier() string { return "xai" }

func (e *xaiBadCredentialsRefreshExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	token := authAccessToken(auth)
	e.mu.Unlock()
	if token == "stale-access-token" {
		message := e.forbiddenText
		if message == "" {
			message = "unauthenticated:bad-credentials"
		}
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusForbidden, Message: message}
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID + ":" + token)}, nil
}

func (*xaiBadCredentialsRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *xaiBadCredentialsRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refreshCalls++
	if e.refreshFail {
		return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "refresh token invalid"}
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = e.refreshTokens[auth.ID]
	return auth, nil
}

func (*xaiBadCredentialsRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*xaiBadCredentialsRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *xaiBadCredentialsRefreshExecutor) calls() ([]string, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.executeCalls...), e.refreshCalls
}

func newXaiBadCredentialsRefreshFixture(t *testing.T, refreshFail bool) (*Manager, *xaiBadCredentialsRefreshExecutor, *Auth, *Auth, string) {
	t.Helper()
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	model := "grok-4"
	primary := &Auth{ID: "aa-primary-xai", Provider: "xai", Metadata: map[string]any{
		"access_token": "stale-access-token", "refresh_token": "primary-refresh-token",
	}}
	backup := &Auth{ID: "bb-backup-xai", Provider: "xai", Metadata: map[string]any{
		"access_token": "backup-access-token", "refresh_token": "backup-refresh-token",
	}}
	executor := &xaiBadCredentialsRefreshExecutor{refreshFail: refreshFail, refreshTokens: map[string]string{primary.ID: "fresh-access-token"}}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "xai", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(backup.ID, "xai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
		reg.UnregisterClient(backup.ID)
	})
	if _, err := m.Register(context.Background(), primary); err != nil {
		t.Fatalf("register primary: %v", err)
	}
	if _, err := m.Register(context.Background(), backup); err != nil {
		t.Fatalf("register backup: %v", err)
	}
	return m, executor, primary, backup, model
}

func TestManager_Execute_XaiBadCredentialsRefreshesCurrentAuthBeforeFallback(t *testing.T) {
	m, executor, primary, backup, model := newXaiBadCredentialsRefreshFixture(t, false)
	resp, err := m.Execute(context.Background(), []string{"xai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if got := string(resp.Payload); got != primary.ID+":fresh-access-token" {
		t.Fatalf("payload = %q, want refreshed primary", got)
	}
	calls, refreshCalls := executor.calls()
	if refreshCalls != 1 || len(calls) != 2 || calls[0] != primary.ID || calls[1] != primary.ID {
		t.Fatalf("execute calls = %v, refresh calls = %d", calls, refreshCalls)
	}
	for _, id := range calls {
		if id == backup.ID {
			t.Fatal("backup must not run when primary refresh succeeds")
		}
	}
}

func TestManager_Execute_XaiBadCredentialsRefreshFailureFallsBackAndMarksUnauthorized(t *testing.T) {
	m, executor, primary, backup, model := newXaiBadCredentialsRefreshFixture(t, true)
	resp, err := m.Execute(context.Background(), []string{"xai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("payload = %q, want backup", got)
	}
	calls, refreshCalls := executor.calls()
	if refreshCalls != 1 || len(calls) != 2 || calls[0] != primary.ID || calls[1] != backup.ID {
		t.Fatalf("execute calls = %v, refresh calls = %d", calls, refreshCalls)
	}
	updated, ok := m.GetByID(primary.ID)
	if !ok || updated == nil {
		t.Fatal("primary auth missing")
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Unavailable || state.LastError == nil || state.LastError.Code != "unauthorized" {
		t.Fatalf("expected unauthorized suspension, got %+v", state)
	}
}

func TestManager_Execute_XaiGenericForbiddenDoesNotRefresh(t *testing.T) {
	m, executor, primary, backup, model := newXaiBadCredentialsRefreshFixture(t, false)
	executor.forbiddenText = "forbidden"

	resp, err := m.Execute(context.Background(), []string{"xai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("payload = %q, want backup", got)
	}
	calls, refreshCalls := executor.calls()
	if refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, want 0", refreshCalls)
	}
	if len(calls) != 2 || calls[0] != primary.ID || calls[1] != backup.ID {
		t.Fatalf("execute calls = %v, want [primary, backup]", calls)
	}
}
