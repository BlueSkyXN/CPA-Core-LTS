package test

// This file consumes the public SDK the way an external embedder does: only
// exported identifiers from sdk/cliproxy/auth, no package-internal helpers.
// It guards source compatibility of the original AuthExecutionSessionCloser
// contract (two-argument CloseExecutionSessionsForAuth) and of the newer
// ID-scoped CloseAuthExecutionSessions capability under its own interface name.

import (
	"context"
	"net/http"
	"sync"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// legacyCloserExecutor models an embedder written against the original public
// contract: it implements only CloseExecutionSessionsForAuth. If the SDK ever
// renames the interface or changes the method shape, these assertions stop
// compiling — which is exactly the regression to catch.
type legacyCloserExecutor struct {
	mu       sync.Mutex
	closed   [][2]string
	provider string
}

func (e *legacyCloserExecutor) Identifier() string { return e.provider }

func (e *legacyCloserExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *legacyCloserExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *legacyCloserExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *legacyCloserExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *legacyCloserExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *legacyCloserExecutor) CloseExecutionSessionsForAuth(authID, authIndex string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = append(e.closed, [2]string{authID, authIndex})
}

// idOnlyCloserExecutor models an executor that only supports the newer
// credential-ID-scoped close (no per-index separation).
type idOnlyCloserExecutor struct {
	provider  string
	closedIDs []string
}

func (e *idOnlyCloserExecutor) Identifier() string { return e.provider }

func (e *idOnlyCloserExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *idOnlyCloserExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *idOnlyCloserExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *idOnlyCloserExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *idOnlyCloserExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *idOnlyCloserExecutor) CloseAuthExecutionSessions(authID string) {
	e.closedIDs = append(e.closedIDs, authID)
}

// Compile-time proof that an implementation of the original two-argument
// method still satisfies the original public interface name, and that each
// capability stays addressable under its own exported name.
var (
	_ coreauth.ProviderExecutor             = (*legacyCloserExecutor)(nil)
	_ coreauth.AuthExecutionSessionCloser   = (*legacyCloserExecutor)(nil)
	_ coreauth.ProviderExecutor             = (*idOnlyCloserExecutor)(nil)
	_ coreauth.AuthExecutionSessionIDCloser = (*idOnlyCloserExecutor)(nil)
)

func removeExecutorAuth(t *testing.T, exec coreauth.ProviderExecutor, auth *coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.Remove(context.Background(), auth.ID)
}

// An embedder implementing only the original two-argument contract must keep
// working end to end: removing the credential routes through it with both the
// credential ID and its runtime index.
func TestSDKLegacyAuthExecutionSessionCloserCompat(t *testing.T) {
	exec := &legacyCloserExecutor{provider: "codex"}
	removeExecutorAuth(t, exec, &coreauth.Auth{ID: "auth-1", Index: "index-1", Provider: "codex"})

	closed := exec.closed
	if len(closed) != 1 {
		t.Fatalf("CloseExecutionSessionsForAuth calls = %d, want 1", len(closed))
	}
	if closed[0] != [2]string{"auth-1", "index-1"} {
		t.Fatalf("CloseExecutionSessionsForAuth args = %v, want [auth-1 index-1]", closed[0])
	}
}

// An executor exposing only the ID-scoped capability falls back to it when the
// credential is removed.
func TestSDKAuthExecutionSessionIDCloserFallback(t *testing.T) {
	exec := &idOnlyCloserExecutor{provider: "codex"}
	removeExecutorAuth(t, exec, &coreauth.Auth{ID: "auth-2", Index: "index-2", Provider: "codex"})

	if len(exec.closedIDs) != 1 || exec.closedIDs[0] != "auth-2" {
		t.Fatalf("CloseAuthExecutionSessions calls = %v, want [auth-2]", exec.closedIDs)
	}
}
