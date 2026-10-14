package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// deniedPolicyManager builds a manager with a codex channel denylist on
// "gpt-4.1" and a registered fake executor.
func deniedPolicyManager(t *testing.T) *Manager {
	t.Helper()
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(fakeAdmitExecutor{})
	manager.runtimeConfig.Store(&internalconfig.Config{
		OAuthModelPolicy: map[string]internalconfig.OAuthModelPolicyEntry{
			"codex": {
				Denylist: &internalconfig.OAuthModelPolicyList{Enabled: true, Models: []string{"gpt-4.1"}},
			},
		},
	})
	return manager
}

// A channel denylist removes the denied model for every account of the
// provider, while other models keep routing.
func TestPickNextLegacyChannelDeny(t *testing.T) {
	manager := deniedPolicyManager(t)
	reg := registry.GetGlobalRegistry()

	auth := &Auth{ID: "policy-channel-auth", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "tok"}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}, {ID: "gpt-4.1"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, _, errDenied := manager.pickNextLegacy(context.Background(), "codex", "gpt-4.1", cliproxyexecutor.Options{}, nil); errDenied == nil {
		t.Fatal("channel-denied model must not select any auth")
	}
	selected, _, errAllowed := manager.pickNextLegacy(context.Background(), "codex", "gpt-5-codex", cliproxyexecutor.Options{}, nil)
	if errAllowed != nil || selected == nil || selected.ID != auth.ID {
		t.Fatalf("pickNextLegacy() allowed model = %#v %v", selected, errAllowed)
	}
}

// An account denylist removes the model only for that account; another account
// of the same provider still serves it.
func TestPickNextLegacyAccountDenyScopedToAuth(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(fakeAdmitExecutor{})
	manager.runtimeConfig.Store(&internalconfig.Config{})

	denied := &Auth{
		ID:         "policy-account-denied",
		Provider:   "codex",
		Status:     StatusActive,
		Metadata:   map[string]any{"access_token": "tok"},
		Attributes: map[string]string{AttributeModelPolicy: "deny=gpt-4.1"},
	}
	allowed := &Auth{ID: "policy-account-allowed", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "tok"}}
	for _, auth := range []*Auth{denied, allowed} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(denied.ID, "codex", []*registry.ModelInfo{{ID: "gpt-4.1"}})
	reg.RegisterClient(allowed.ID, "codex", []*registry.ModelInfo{{ID: "gpt-4.1"}})
	t.Cleanup(func() {
		reg.UnregisterClient(denied.ID)
		reg.UnregisterClient(allowed.ID)
	})

	for i := 0; i < 3; i++ {
		selected, _, errPick := manager.pickNextLegacy(context.Background(), "codex", "gpt-4.1", cliproxyexecutor.Options{}, nil)
		if errPick != nil || selected == nil || selected.ID != allowed.ID {
			t.Fatalf("pick %d selected = %#v %v, want the non-denied auth", i, selected, errPick)
		}
	}
}

func TestAdmitFlowExecutionRejectsPolicyDeniedModel(t *testing.T) {
	manager := deniedPolicyManager(t)
	auth := &Auth{
		ID:         "admit-policy-auth",
		Provider:   "codex",
		Status:     StatusActive,
		Attributes: map[string]string{AttributeModelPolicy: "deny=gpt-4.1"},
		Metadata:   map[string]any{"access_token": "tok"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}

	_, errAdmit := manager.admitFlowExecution(context.Background(), fakeAdmitExecutor{}, auth, cliproxyexecutor.Request{Model: "gpt-4.1"}, cliproxyexecutor.Options{})
	var denied *ModelPolicyDeniedError
	if !errors.As(errAdmit, &denied) {
		t.Fatalf("admitFlowExecution() error = %v, want ModelPolicyDeniedError", errAdmit)
	}
	if denied.Model != "gpt-4.1" {
		t.Fatalf("denied model = %q", denied.Model)
	}

	if _, errAllowed := manager.admitFlowExecution(context.Background(), fakeAdmitExecutor{}, auth, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{}); errAllowed != nil {
		t.Fatalf("admitFlowExecution() allowed model error = %v", errAllowed)
	}
}

type fakeAdmitExecutor struct{}

func (fakeAdmitExecutor) Identifier() string { return "codex" }
func (fakeAdmitExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (fakeAdmitExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (fakeAdmitExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (fakeAdmitExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (fakeAdmitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
