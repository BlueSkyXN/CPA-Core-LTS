package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type compactProbeExecutor struct{}

func (compactProbeExecutor) Identifier() string { return "codex" }
func (compactProbeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (compactProbeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (compactProbeExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }
func (compactProbeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (compactProbeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newCompactTestManager(t *testing.T) *Manager {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(compactProbeExecutor{})
	return manager
}

func TestPickNextMixed_CompactSkipsForceOff(t *testing.T) {
	manager := newCompactTestManager(t)
	ctx := context.Background()
	if _, err := manager.Register(ctx, &Auth{ID: "on", Provider: "codex", Attributes: map[string]string{"compact_allowed": "true"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(ctx, &Auth{ID: "off", Provider: "codex", Attributes: map[string]string{"compact_allowed": "false"}}); err != nil {
		t.Fatal(err)
	}

	opts := cliproxyexecutor.Options{Alt: cliproxyexecutor.ResponsesCompactAlt}
	for i := 0; i < 5; i++ {
		auth, _, _, err := manager.pickNextMixed(ctx, []string{"codex"}, "", opts, map[string]struct{}{})
		if err != nil {
			t.Fatalf("pick #%d error: %v", i, err)
		}
		if auth.ID != "on" {
			t.Fatalf("pick #%d auth = %q, want on", i, auth.ID)
		}
	}
}

func TestPickNextMixed_CompactNoneEligibleReturns503(t *testing.T) {
	manager := newCompactTestManager(t)
	ctx := context.Background()
	if _, err := manager.Register(ctx, &Auth{ID: "off", Provider: "codex", Attributes: map[string]string{"compact_allowed": "false"}}); err != nil {
		t.Fatal(err)
	}

	opts := cliproxyexecutor.Options{Alt: cliproxyexecutor.ResponsesCompactAlt}
	_, _, _, err := manager.pickNextMixed(ctx, []string{"codex"}, "", opts, map[string]struct{}{})
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != "compact_unsupported" || authErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want compact_unsupported/503", err)
	}
}

func TestPickNextMixed_NonCompactIgnoresCompactFlag(t *testing.T) {
	manager := newCompactTestManager(t)
	ctx := context.Background()
	if _, err := manager.Register(ctx, &Auth{ID: "off", Provider: "codex", Attributes: map[string]string{"compact_allowed": "false"}}); err != nil {
		t.Fatal(err)
	}

	auth, _, _, err := manager.pickNextMixed(ctx, []string{"codex"}, "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if err != nil || auth == nil || auth.ID != "off" {
		t.Fatalf("non-compact pick: auth=%v err=%v", auth, err)
	}
}
