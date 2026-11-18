package auth

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestResourceAccessFiltersUpstreamAliasPool(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ownerScope := session.CallerScope("fixture-owner")
	manager.SetConfig(&config.Config{
		ResourceAllowlists: &config.ResourceAllowlists{
			Models: map[string][]string{"private-upstream": {ownerScope}},
		},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:   "resource-fixture",
			Prefix: "tenant",
			Models: []config.OpenAICompatibilityModel{
				{Name: "private-upstream", Alias: "model-pool"},
				{Name: "public-upstream", Alias: "model-pool"},
			},
		}},
	})
	credential := &Auth{
		ID:       t.Name(),
		Provider: "openai-compatibility:resource-fixture",
		Prefix:   "tenant",
		Attributes: map[string]string{
			AttributeSource: "config:resource-fixture[0]",
			"compat_name":   "resource-fixture",
			"provider_key":  "openai-compatibility:resource-fixture",
		},
	}
	registerCapabilityTestAuth(t, manager, credential)
	const alias = "tenant/model-pool"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: alias}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(credential.ID) })

	guest := usage.WithCapturedIdentity(context.Background(), "fixture-guest", "config-inline")
	if visible := manager.ResourceModelVisibility(guest, []string{alias}); visible == nil || !visible(alias) {
		t.Fatal("the public pool target must keep the alias visible")
	}
	// The catalog and candidate check must not rotate the underlying alias pool.
	if candidates, _, _, _ := manager.executionModelCandidatesWithAlias(credential, alias); len(candidates) != 2 || candidates[0] != "private-upstream" {
		t.Fatalf("first rotation got %v", candidates)
	}
	for i := 0; i < 3; i++ {
		models, pooled, _, _ := manager.preparedExecutionModelsWithAliasForRequest(guest, credential, alias)
		if !pooled || !slices.Equal(models, []string{"public-upstream"}) {
			t.Fatalf("guest upstream pool = %v, pooled = %v", models, pooled)
		}
	}

	// Routing metadata is not an authenticated identity. Direct and rewritten
	// model checks must still reject this dispatch.
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.CallerScopeMetadataKey: ownerScope,
	}}
	req := cliproxyexecutor.Request{Model: "private-upstream"}
	if _, err := manager.admitFlowExecution(guest, fakeAdmitExecutor{}, credential, req, opts); !errors.Is(err, ErrResourceAccessDenied) {
		t.Fatalf("unauthorized final dispatch = %v", err)
	}
	owner := usage.WithCapturedIdentity(context.Background(), "fixture-owner", "config-inline")
	if _, err := manager.admitFlowExecution(owner, fakeAdmitExecutor{}, credential, req, opts); err != nil {
		t.Fatalf("authorized final dispatch = %v", err)
	}
}
