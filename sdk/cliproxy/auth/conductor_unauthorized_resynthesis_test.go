package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func driveToTerminalUnauthorized(t *testing.T) (*Manager, *unauthorizedRefreshExecutor, *Auth, string) {
	t.Helper()
	m, executor, primary, _, model := newUnauthorizedRefreshFixture(t, false)
	executor.mu.Lock()
	executor.refreshErr = errors.New(`token refresh failed with status 400: {"error": "invalid_grant"}`)
	executor.mu.Unlock()
	updated, _ := m.GetByID(primary.ID)
	updated.Metadata["expired"] = time.Now().Add(6 * time.Hour).Format(time.RFC3339)
	if _, err := m.Update(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	final, _ := m.GetByID(primary.ID)
	if !hasUnauthorizedAuthFailure(final) {
		t.Fatal("fixture did not reach terminal unauthorized state")
	}
	return m, executor, final, model
}

// F07: re-synthesizing an auth with unchanged credentials (note/model settings
// edits) must keep the credential-level terminal unauthorized state.
func TestManager_Update_SameCredentialsKeepsTerminalUnauthorized(t *testing.T) {
	m, executor, terminal, model := driveToTerminalUnauthorized(t)
	resynth := terminal.Clone()
	resynth.Unavailable = false
	resynth.Status = StatusActive
	resynth.LastError = nil
	resynth.StatusMessage = ""
	resynth.Metadata["note"] = "edited note"
	if _, err := m.Update(context.Background(), resynth); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, _ := m.GetByID(terminal.ID)
	if !hasUnauthorizedAuthFailure(after) {
		t.Fatalf("terminal unauthorized lost: unavailable=%v status=%s last_error=%+v", after.Unavailable, after.Status, after.LastError)
	}
	before := executor.RefreshCalls()
	primaryBefore := 0
	for _, id := range executor.ExecuteCalls() {
		if id == terminal.ID {
			primaryBefore++
		}
	}
	if _, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute after update: %v", err)
	}
	primaryAfter := 0
	for _, id := range executor.ExecuteCalls() {
		if id == terminal.ID {
			primaryAfter++
		}
	}
	if primaryAfter != primaryBefore || executor.RefreshCalls() != before {
		t.Fatalf("terminal auth was executed/refreshed again: executes %d->%d refresh %d->%d", primaryBefore, primaryAfter, before, executor.RefreshCalls())
	}
}

func TestManager_Update_NewCredentialsClearsTerminalUnauthorized(t *testing.T) {
	m, _, terminal, _ := driveToTerminalUnauthorized(t)
	replaced := terminal.Clone()
	replaced.Metadata["access_token"] = "fresh-access-token"
	replaced.Metadata["refresh_token"] = "fresh-refresh-token"
	if _, err := m.Update(context.Background(), replaced); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, _ := m.GetByID(terminal.ID)
	if hasUnauthorizedAuthFailure(after) || after.Unavailable {
		t.Fatalf("new credentials did not clear terminal state: unavailable=%v status=%s", after.Unavailable, after.Status)
	}
}
