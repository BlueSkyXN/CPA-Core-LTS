package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRegisterResolvedModelsForAuthAppliesModelPolicyAndSnapshot(t *testing.T) {
	reg := GlobalModelRegistry()
	authID := "model-policy-register-test"
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	cfg := &config.Config{OAuthModelPolicy: map[string]config.OAuthModelPolicyEntry{
		"codex": {
			Allowlist: &config.OAuthModelPolicyList{Enabled: true, Models: []string{"gpt-*"}},
			Denylist:  &config.OAuthModelPolicyList{Enabled: true, Models: []string{"*-preview"}},
		},
	}}
	service := &Service{cfg: cfg, accountCatalogs: newAccountCatalogStore()}
	auth := &coreauth.Auth{ID: authID, Provider: "codex"}

	service.registerResolvedModelsForAuth(auth, "codex", []*ModelInfo{
		{ID: "gpt-5-codex"},
		{ID: "gpt-5-preview"},
		{ID: "claude-sonnet-4"},
	})

	got := reg.GetModelsForClient(authID)
	if len(got) != 1 || got[0].ID != "gpt-5-codex" {
		t.Fatalf("registered models = %#v, want [gpt-5-codex]", got)
	}

	// The pre-policy snapshot keeps all three candidates so previews can show
	// what a policy change would re-enable without rediscovery.
	snapshot := service.accountCatalogs.snapshot(authID)
	if len(snapshot) != 3 {
		t.Fatalf("snapshot = %#v, want 3 pre-policy candidates", snapshot)
	}

	// Preview agrees with the registration outcome and carries reasons.
	if _, ok := service.PreviewModelPolicyForAuth(auth); !ok {
		t.Fatal("preview should have a catalog snapshot")
	}
	preview, _ := service.PreviewModelPolicyForAuth(auth)
	allowed := map[string]bool{}
	for _, evaluation := range preview.Evaluations {
		allowed[evaluation.ModelID] = evaluation.Allowed
		if !evaluation.Allowed && evaluation.Layer == "" {
			t.Fatalf("denied evaluation missing layer: %+v", evaluation)
		}
	}
	if !allowed["gpt-5-codex"] || allowed["gpt-5-preview"] || allowed["claude-sonnet-4"] {
		t.Fatalf("preview decisions = %#v", allowed)
	}
}

func TestAntigravityFencedPublicationRetainsModelPolicyAndCatalog(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	models := registry.GetAntigravityModels()
	if len(models) < 2 {
		t.Fatal("missing Antigravity catalog")
	}
	allowed, denied := models[0].ID, models[1].ID
	cfg := &config.Config{
		OAuthModelPolicy: map[string]config.OAuthModelPolicyEntry{"antigravity": {
			Allowlist: &config.OAuthModelPolicyList{Enabled: true, Models: []string{allowed}},
		}},
		OAuthModelAlias: map[string][]config.OAuthModelAlias{"antigravity": {{Name: allowed, Alias: "public"}}},
	}
	cfg.ForceModelPrefix = true
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager, accountCatalogs: newAccountCatalogStore()}
	auth := antigravityTestAuth(t.Name(), "http://127.0.0.1:1")
	auth.Prefix = "tenant"
	auth, err := manager.Register(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	reg := GlobalModelRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	key := service.antigravityCapabilityKey(auth)
	hints := antigravityModelCapabilityHints{revision: 1, ModelIDs: map[string]struct{}{
		normalizeAntigravityFetchedModelID(allowed): {}, normalizeAntigravityFetchedModelID(denied): {},
	}}
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{hints: hints}
	antigravityCapabilityMu.Unlock()
	service.applyAntigravityModelHints(t.Context(), auth, "antigravity", hints, key, auth.RegistrationEpoch, reg.ClientRegistrationEpoch(auth.ID))
	got := reg.GetModelsForClient(auth.ID)
	if len(got) != 1 || got[0].ID != "tenant/public" {
		t.Fatalf("fenced publication bypassed policy or changed alias/prefix order: %+v", got)
	}
	preview, ok := service.PreviewModelPolicyForAuth(auth)
	if !ok || len(preview.Evaluations) != 2 {
		t.Fatalf("missing pre-policy catalog: %+v", preview)
	}
	for _, evaluation := range preview.Evaluations {
		if evaluation.Allowed != (evaluation.ModelID == allowed) {
			t.Fatalf("preview disagrees with registration: %+v", evaluation)
		}
	}
}

func TestRegisterResolvedModelsForAuthAccountPolicyZeroModels(t *testing.T) {
	reg := GlobalModelRegistry()
	authID := "model-policy-zero-test"
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	service := &Service{cfg: &config.Config{}, accountCatalogs: newAccountCatalogStore()}
	auth := &coreauth.Auth{ID: authID, Provider: "qoder"}
	coreauth.SetModelPolicyAttribute(auth, &coreauth.AccountModelPolicy{
		Allowlist: &coreauth.OAuthModelPolicy{Enabled: true, Models: []string{}},
	})

	service.registerResolvedModelsForAuth(auth, "qoder", []*ModelInfo{{ID: "qmodel_38max"}})
	if got := reg.GetModelsForClient(authID); len(got) != 0 {
		t.Fatalf("enabled empty allowlist must yield zero models, got %#v", got)
	}
	// Snapshot still records the candidate for re-enable without discovery.
	if got := service.accountCatalogs.snapshot(authID); len(got) != 1 {
		t.Fatalf("snapshot = %#v, want the pre-policy candidate", got)
	}
}

func TestAccountCatalogStoreStaleEpochDoesNotOverwrite(t *testing.T) {
	store := newAccountCatalogStore()
	store.recordIDs("auth", 2, []string{"new"})
	store.recordIDs("auth", 1, []string{"stale"})
	if got := store.snapshot("auth"); len(got) != 1 || got[0] != "new" {
		t.Fatalf("snapshot = %#v, want [new]", got)
	}
	store.remove("auth")
	if got := store.snapshot("auth"); got != nil {
		t.Fatalf("snapshot after remove = %#v", got)
	}
}

// API-key auth entries must stay outside the model policy scope: they have
// their own static model lists and excluded-models semantics.
func TestRegisterResolvedModelsForAuthSkipsAPIKeyAuth(t *testing.T) {
	reg := GlobalModelRegistry()
	authID := "model-policy-apikey-test"
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	cfg := &config.Config{OAuthModelPolicy: map[string]config.OAuthModelPolicyEntry{
		"gemini": {Allowlist: &config.OAuthModelPolicyList{Enabled: true, Models: []string{}}},
	}}
	service := &Service{cfg: cfg, accountCatalogs: newAccountCatalogStore()}
	auth := &coreauth.Auth{
		ID:         authID,
		Provider:   "gemini",
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": "test-key"},
	}

	service.registerResolvedModelsForAuth(auth, "gemini", []*ModelInfo{{ID: "gemini-2.5-pro"}})
	if got := reg.GetModelsForClient(authID); len(got) != 1 {
		t.Fatalf("apikey models = %#v, want unrestricted registration", got)
	}
}

func TestModelPolicyNativeRegistrationBeforeAliasAndPrefix(t *testing.T) {
	models := registry.GetCodexProModels()
	if len(models) == 0 {
		t.Fatal("missing embedded model catalog")
	}
	model := models[0].ID
	cfg := &config.Config{
		OAuthModelAlias:  map[string][]config.OAuthModelAlias{"codex": {{Name: model, Alias: "public"}}},
		OAuthSettings:    map[string][]config.OAuthModelSetting{"codex": {{Name: model, Alias: "public", MaxContextLength: 123456}}},
		OAuthModelPolicy: map[string]config.OAuthModelPolicyEntry{"codex": {Allowlist: &config.OAuthModelPolicyList{Enabled: true, Models: []string{model}}}},
	}
	cfg.ForceModelPrefix = true
	service := &Service{cfg: cfg, accountCatalogs: newAccountCatalogStore()}
	auth := &coreauth.Auth{ID: t.Name(), Provider: "codex", Prefix: "tenant"}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	service.registerModelsForAuth(context.Background(), auth)
	got := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(got) != 1 || got[0].ID != "tenant/public" {
		t.Fatalf("native registration lost allowed alias: %+v", got)
	}
	if got[0].MaxContextLength != 123456 {
		t.Fatal("V8 OAuth model settings lost during policy registration")
	}
	preview, ok := service.PreviewModelPolicyForAuth(auth)
	if !ok {
		t.Fatal("missing preview")
	}
	found := false
	for _, row := range preview.Evaluations {
		if row.ModelID == model && row.Allowed {
			found = true
		}
	}
	if !found {
		t.Fatal("preview did not retain the real upstream model")
	}
}
