package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type replaceAwareExecutor struct {
	id string

	mu               sync.Mutex
	closedSessionIDs []string
	closedAuths      [][2]string
}

func (e *replaceAwareExecutor) Identifier() string {
	return e.id
}

func (e *replaceAwareExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *replaceAwareExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *replaceAwareExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *replaceAwareExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *replaceAwareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *replaceAwareExecutor) CloseExecutionSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closedSessionIDs = append(e.closedSessionIDs, sessionID)
}

func (e *replaceAwareExecutor) ClosedSessionIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.closedSessionIDs))
	copy(out, e.closedSessionIDs)
	return out
}

func (e *replaceAwareExecutor) CloseExecutionSessionsForAuth(authID, authIndex string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closedAuths = append(e.closedAuths, [2]string{authID, authIndex})
}

func (e *replaceAwareExecutor) ClosedAuths() [][2]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][2]string(nil), e.closedAuths...)
}

func TestManagerRegisterExecutorClosesReplacedExecutionSessions(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	replaced := &replaceAwareExecutor{id: "codex"}
	current := &replaceAwareExecutor{id: "codex"}

	manager.RegisterExecutor(replaced)
	manager.RegisterExecutor(current)

	closed := replaced.ClosedSessionIDs()
	if len(closed) != 1 {
		t.Fatalf("expected replaced executor close calls = 1, got %d", len(closed))
	}
	if closed[0] != CloseAllExecutionSessionsID {
		t.Fatalf("expected close marker %q, got %q", CloseAllExecutionSessionsID, closed[0])
	}
	if len(current.ClosedSessionIDs()) != 0 {
		t.Fatalf("expected current executor to stay open")
	}
}

func TestManagerExecutorReturnsRegisteredExecutor(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	current := &replaceAwareExecutor{id: "codex"}
	manager.RegisterExecutor(current)

	resolved, okResolved := manager.Executor("CODEX")
	if !okResolved {
		t.Fatal("expected registered executor to be found")
	}
	resolvedExecutor, okResolvedExecutor := resolved.(*replaceAwareExecutor)
	if !okResolvedExecutor {
		t.Fatalf("expected resolved executor type %T, got %T", current, resolved)
	}
	if resolvedExecutor != current {
		t.Fatal("expected resolved executor to match registered executor")
	}

	_, okMissing := manager.Executor("unknown")
	if okMissing {
		t.Fatal("expected unknown provider lookup to fail")
	}
}

func TestManagerUnregisterExecutorClosesExecutionSessions(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	executor := &replaceAwareExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	manager.UnregisterExecutor("CODEX")

	closed := executor.ClosedSessionIDs()
	if len(closed) != 1 || closed[0] != CloseAllExecutionSessionsID {
		t.Fatalf("unregister close calls = %#v, want %q", closed, CloseAllExecutionSessionsID)
	}
	if _, ok := manager.Executor("codex"); ok {
		t.Fatal("executor remains registered after UnregisterExecutor")
	}
}

func TestManagerRemoveClosesOnlyRemovedAuthSessionsWhenSupported(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	executor := &replaceAwareExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "auth-1", Index: "index-1", Provider: "codex"}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	manager.Remove(context.Background(), auth.ID)
	closedAuths := executor.ClosedAuths()
	if len(closedAuths) != 1 || closedAuths[0] != [2]string{"auth-1", "index-1"} {
		t.Fatalf("auth-scoped close calls = %#v", closedAuths)
	}
	if closed := executor.ClosedSessionIDs(); len(closed) != 0 {
		t.Fatalf("provider-wide close calls = %#v, want none", closed)
	}
}
