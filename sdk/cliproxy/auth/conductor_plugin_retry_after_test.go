package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// pluginStyleError mirrors the shape of internal/pluginhost.rpcError as it
// reaches the conductor: a plugin-owned code namespace plus an optional
// declared RetryAfter.
type pluginStyleError struct {
	code       string
	msg        string
	statusCode int
	retryAfter *time.Duration
}

func (e pluginStyleError) Error() string              { return e.msg }
func (e pluginStyleError) StatusCode() int            { return e.statusCode }
func (e pluginStyleError) PluginCode() string         { return e.code }
func (e pluginStyleError) RetryAfter() *time.Duration { return e.retryAfter }

func pluginErrorExecutor(err error) ProviderExecutor {
	return &customStreamMockExecutor{
		identifier: "claude",
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "claude",
			executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return cliproxyexecutor.Response{}, err
			},
		},
	}
}

// A plugin that declares RetryAfterSeconds owns that value on the wire: the
// pool wrapper must not replace it with a locally extended deadline.
func TestPluginDeclaredRetryAfterIsNotRewrappedByPoolCooldown(t *testing.T) {
	declared := 7 * time.Second
	pluginErr := pluginStyleError{
		code:       "rate_limited",
		msg:        "plugin says wait 7s",
		statusCode: http.StatusTooManyRequests,
		retryAfter: &declared,
	}
	manager := newIssue7521Manager(t, nil, pluginErrorExecutor(pluginErr), newIssue7521Auth("plugin-declared"))
	// Single attempt: the assertion is about the terminal wrapper, not retries.
	manager.SetRetryConfig(0, 0, 0)

	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected terminal 429 error")
	}
	if got := SafeResponseHeaders(errExec).Get("Retry-After"); got != "7" {
		t.Fatalf("Retry-After = %q, want the plugin's declared 7", got)
	}
	var poolWrapped *poolCooldownTerminalError
	if errors.As(errExec, &poolWrapped) && poolWrapped != nil {
		t.Fatalf("declared plugin RetryAfter must not be re-wrapped as a pool cooldown: %#v", poolWrapped)
	}
}

// A plugin 429 without a declared hint has no contract to protect: it must
// still be annotated with the pool's real recovery so the client is not told
// to retry sooner than any credential can serve.
func TestPluginUndeclaredRetryAfterStillGetsPoolRecovery(t *testing.T) {
	pluginErr := pluginStyleError{
		code:       "rate_limited",
		msg:        "plugin rate limited without hint",
		statusCode: http.StatusTooManyRequests,
		retryAfter: nil,
	}
	manager := newIssue7521Manager(t, nil, pluginErrorExecutor(pluginErr), newIssue7521Auth("plugin-undeclared"))
	// Single attempt: the advertised wait is the full pool recovery window.
	manager.SetRetryConfig(0, 0, 0)

	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected terminal 429 error")
	}
	// An undeclared hint leaves no plugin contract to protect, so the normal
	// pool treatment applies: the error carries the pool's real recovery.
	var poolWrapped *poolCooldownTerminalError
	if !errors.As(errExec, &poolWrapped) || poolWrapped == nil {
		t.Fatalf("error %T must be wrapped as *poolCooldownTerminalError, got %T", errExec, errExec)
	}
	if poolWrapped.deadline.IsZero() || !HasTrustedRetryDeadline(errExec) {
		t.Fatal("the wrapped error must advertise the pool's recovery deadline")
	}
	if got := SafeResponseHeaders(errExec).Get("Retry-After"); got == "" {
		t.Fatal("Retry-After must be present once the pool cooldown is known")
	}
}
