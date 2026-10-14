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
		Attributes: map[string]string{AttributeModelPolicy: `{"denylist":{"enabled":true,"models":["gpt-4.1"]}}`},
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
		Attributes: map[string]string{AttributeModelPolicy: `{"denylist":{"enabled":true,"models":["gpt-4.1"]}}`},
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

func TestModelPolicySelectionResolvesAliasAndPrefix(t *testing.T) {
	manager := deniedPolicyManager(t)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"codex": {{Name: "gpt-5-codex", Alias: "public"}},
	})
	auth := &Auth{ID: t.Name(), Provider: "codex", Prefix: "tenant", Status: StatusActive}
	SetModelPolicyAttribute(auth, &AccountModelPolicy{Allowlist: &OAuthModelPolicy{Enabled: true, Models: []string{"gpt-5-codex"}}})
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "tenant/public"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if selected, _, err := manager.pickNextLegacy(context.Background(), "codex", "tenant/public(high)", cliproxyexecutor.Options{}, nil); err != nil || selected == nil {
		t.Fatalf("allowed upstream model was rejected through its alias: %v", err)
	}
	if _, err := manager.admitFlowExecution(context.Background(), fakeAdmitExecutor{}, auth, cliproxyexecutor.Request{Model: "gpt-5-codex(high)"}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("thinking suffix changed policy outcome: %v", err)
	}
}

func TestModelPolicyConfiguredAPIKeyScope(t *testing.T) {
	manager := deniedPolicyManager(t)
	auth := &Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey, AttributeSource: AuthSourceConfig}}
	if _, err := manager.admitFlowExecution(context.Background(), fakeAdmitExecutor{}, auth, cliproxyexecutor.Request{Model: "gpt-4.1"}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("OAuth policy restricted configured API key: %v", err)
	}
	auth.Attributes[AttributeSource] = AuthSourceFile
	if _, allowed := manager.modelPolicyAllowedForExecution(auth, "codex", "gpt-4.1"); allowed {
		t.Fatal("file credential bypassed policy because it used API key authentication")
	}
}

func TestModelPolicyAttributePreservesLiteralSeparators(t *testing.T) {
	auth := &Auth{Provider: "codex"}
	SetModelPolicyAttribute(auth, &AccountModelPolicy{Allowlist: &OAuthModelPolicy{Enabled: true, Models: []string{"literal;allow=*", "x,y"}}})
	if EvaluateModelPolicy(nil, auth, "codex", "unlisted").Allowed || EvaluateModelPolicy(nil, auth, "codex", "x").Allowed {
		t.Fatal("model separators changed the allowlist")
	}
	if !EvaluateModelPolicy(nil, auth, "codex", "x,y").Allowed {
		t.Fatal("literal model ID was lost")
	}
	cfg := &internalconfig.Config{OAuthExcludedModels: map[string][]string{"codex": {"x,y"}}}
	if EvaluateModelPolicy(cfg, auth, "codex", "x,y").Allowed {
		t.Fatal("current channel legacy exclusion was ignored")
	}
}

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
