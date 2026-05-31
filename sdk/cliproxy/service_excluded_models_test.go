package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_UsesPreMergedExcludedModelsAttribute(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"gemini-cli": {"gemini-2.5-pro"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini-cli",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"excluded_models": "gemini-2.5-flash",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetAvailableModelsByProvider("gemini-cli")
	if len(models) == 0 {
		t.Fatal("expected gemini-cli models to be registered")
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if strings.EqualFold(modelID, "gemini-2.5-flash") {
			t.Fatalf("expected model %q to be excluded by auth attribute", modelID)
		}
	}

	seenGlobalExcluded := false
	for _, model := range models {
		if model == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(model.ID), "gemini-2.5-pro") {
			seenGlobalExcluded = true
			break
		}
	}
	if !seenGlobalExcluded {
		t.Fatal("expected global excluded model to be present when attribute override is set")
	}
}

func TestRegisterModelsForAuth_MarksOpenAICompatibilityImageModels(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "openrouter",
				BaseURL: "https://example.invalid/v1",
				Models: []config.OpenAICompatibilityModel{{
					Name:  "upstream-image",
					Alias: "image-alias",
					Image: true,
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-image",
		Provider: "openai-compatibility",
		Label:    "openrouter",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name": "openrouter",
		},
	}

	modelRegistry := GlobalModelRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	info := registry.LookupModelInfo("image-alias", "openai-compatibility")
	if info == nil {
		t.Fatal("expected image-alias to be registered")
	}
	if info.Type != registry.OpenAIImageModelType {
		t.Fatalf("model type = %q, want %q", info.Type, registry.OpenAIImageModelType)
	}
	if info.Thinking != nil {
		t.Fatalf("expected image model not to receive text reasoning defaults")
	}
}

func TestRegisterModelsForAuth_OpenAICompatibilityInheritsStaticThinking(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name: "openrouter",
				Models: []config.OpenAICompatibilityModel{
					{Name: "gpt-5.5", Alias: "static-gpt-5.5"},
					{
						Name:  "gpt-5.5",
						Alias: "explicit-gpt-5.5",
						Thinking: &registry.ThinkingSupport{
							Levels: []string{"low"},
						},
					},
					{Name: "custom-upstream", Alias: "gpt-5.5"},
				},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-thinking",
		Provider: "openai-compatibility",
		Label:    "openrouter",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name": "openrouter",
		},
	}

	modelRegistry := GlobalModelRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	inherited := registry.LookupModelInfo("static-gpt-5.5", "openai-compatibility")
	if inherited == nil || inherited.Thinking == nil {
		t.Fatal("expected static-gpt-5.5 to inherit static thinking support")
	}
	hasXHigh := false
	for _, level := range inherited.Thinking.Levels {
		if level == "xhigh" {
			hasXHigh = true
			break
		}
	}
	if !hasXHigh {
		t.Fatalf("static-gpt-5.5 thinking levels = %v, want xhigh", inherited.Thinking.Levels)
	}

	explicit := registry.LookupModelInfo("explicit-gpt-5.5", "openai-compatibility")
	if explicit == nil || explicit.Thinking == nil {
		t.Fatal("expected explicit-gpt-5.5 to keep configured thinking support")
	}
	if got := explicit.Thinking.Levels; len(got) != 1 || got[0] != "low" {
		t.Fatalf("explicit thinking levels = %v, want [low]", got)
	}

	aliasOnly := registry.LookupModelInfo("gpt-5.5", "openai-compatibility")
	if aliasOnly == nil || aliasOnly.Thinking == nil {
		t.Fatal("expected custom-upstream alias to keep default thinking support")
	}
	for _, level := range aliasOnly.Thinking.Levels {
		if level == "xhigh" {
			t.Fatalf("custom-upstream alias thinking levels = %v, want no inherited xhigh", aliasOnly.Thinking.Levels)
		}
	}
}

func TestShouldRefreshAuthForModelCatalogChange(t *testing.T) {
	changedProviders := map[string]bool{"codex": true}

	if !shouldRefreshAuthForModelCatalogChange(&coreauth.Auth{Provider: "codex"}, changedProviders) {
		t.Fatal("expected direct codex auth to refresh")
	}
	if !shouldRefreshAuthForModelCatalogChange(&coreauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}, changedProviders) {
		t.Fatal("expected openai-compatible auth to refresh after static catalog change")
	}
	if shouldRefreshAuthForModelCatalogChange(&coreauth.Auth{Provider: "gemini"}, changedProviders) {
		t.Fatal("did not expect unrelated provider auth to refresh")
	}
	if shouldRefreshAuthForModelCatalogChange(&coreauth.Auth{Provider: "openai-compatibility"}, nil) {
		t.Fatal("did not expect refresh without changed providers")
	}
}
