package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func setupAutoRouteTestAuth(t *testing.T, manager *Manager, id string, models ...*registry.ModelInfo) *Auth {
	t.Helper()
	a := &Auth{
		ID:       id,
		Provider: "codex",
		FileName: id + ".json",
		Status:   StatusActive,
	}
	registerCapabilityTestAuth(t, manager, a)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(a.ID, a.Provider, models)
	t.Cleanup(func() { reg.UnregisterClient(a.ID) })
	return a
}

func TestResolveAutoRouteChecksSuffixPreservedResourceAllowlist(t *testing.T) {
	ownerScope := session.CallerScope("auto-route-owner")
	guestCtx := usage.WithCapturedIdentity(context.Background(), "auto-route-guest", "config-inline")
	ownerCtx := usage.WithCapturedIdentity(context.Background(), "auto-route-owner", "config-inline")

	t.Run("skips suffix-restricted candidate and falls back to allowed candidate", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.RegisterExecutor(fakeAdmitExecutor{})
		cfg := &config.Config{
			ResourceAllowlists: &config.ResourceAllowlists{
				Models: map[string][]string{"blocked(high)": {ownerScope}},
			},
			Routing: config.RoutingConfig{
				AutoModel: config.AutoModelConfig{
					Enabled:    true,
					Candidates: []string{"blocked", "allowed"},
				},
			},
		}
		manager.SetConfig(cfg)
		setupAutoRouteTestAuth(t, manager, "auto-allowlist-both",
			&registry.ModelInfo{ID: "blocked"},
			&registry.ModelInfo{ID: "allowed"},
		)

		_, model, err := manager.ResolveAutoRoute(guestCtx, "auto(high)", nil)
		if err != nil {
			t.Fatalf("ResolveAutoRoute(guest, auto(high)) error = %v", err)
		}
		if model != "allowed(high)" {
			t.Fatalf("ResolveAutoRoute(guest, auto(high)) = %q, want %q", model, "allowed(high)")
		}
	})

	t.Run("denies guest on suffix-restricted sole candidate while allowing unsuffixed and owner", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.RegisterExecutor(fakeAdmitExecutor{})
		cfg := &config.Config{
			ResourceAllowlists: &config.ResourceAllowlists{
				Models: map[string][]string{"blocked(high)": {ownerScope}},
			},
			Routing: config.RoutingConfig{
				AutoModel: config.AutoModelConfig{
					Enabled:    true,
					Candidates: []string{"blocked"},
				},
			},
		}
		manager.SetConfig(cfg)
		setupAutoRouteTestAuth(t, manager, "auto-allowlist-sole", &registry.ModelInfo{ID: "blocked"})

		if _, model, err := manager.ResolveAutoRoute(guestCtx, "auto(high)", nil); !errors.Is(err, ErrResourceAccessDenied) {
			t.Fatalf("ResolveAutoRoute(guest, auto(high)) = (%q, %v), want ErrResourceAccessDenied", model, err)
		}
		if _, model, err := manager.ResolveAutoRoute(guestCtx, "auto", nil); err != nil || model != "blocked" {
			t.Fatalf("ResolveAutoRoute(guest, auto) = (%q, %v), want (\"blocked\", nil)", model, err)
		}
		if _, model, err := manager.ResolveAutoRoute(ownerCtx, "auto(high)", nil); err != nil || model != "blocked(high)" {
			t.Fatalf("ResolveAutoRoute(owner, auto(high)) = (%q, %v), want (\"blocked(high)\", nil)", model, err)
		}
	})
}

func TestResolveAutoRouteChecksSuffixSpecificAliasResourceRoute(t *testing.T) {
	ownerScope := session.CallerScope("auto-alias-owner")
	guestCtx := usage.WithCapturedIdentity(context.Background(), "auto-alias-guest", "config-inline")

	t.Run("suffixed alias targets restricted upstream while base alias targets public upstream", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.RegisterExecutor(fakeAdmitExecutor{})
		cfg := &config.Config{
			ResourceAllowlists: &config.ResourceAllowlists{
				Models: map[string][]string{"private-upstream": {ownerScope}},
			},
			OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"codex": {
					{Name: "private-upstream", Alias: "alias(high)"},
					{Name: "public-upstream", Alias: "alias"},
					{Name: "public-fallback", Alias: "fallback"},
				},
			},
			Routing: config.RoutingConfig{
				AutoModel: config.AutoModelConfig{
					Enabled:    true,
					Candidates: []string{"alias", "fallback"},
				},
			},
		}
		manager.SetConfig(cfg)
		manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
		setupAutoRouteTestAuth(t, manager, "auto-alias-forward",
			&registry.ModelInfo{ID: "alias"},
			&registry.ModelInfo{ID: "fallback"},
		)

		if _, model, err := manager.ResolveAutoRoute(guestCtx, "auto(high)", nil); err != nil || model != "fallback(high)" {
			t.Fatalf("ResolveAutoRoute(guest, auto(high)) = (%q, %v), want (\"fallback(high)\", nil)", model, err)
		}
		if _, model, err := manager.ResolveAutoRoute(guestCtx, "auto", nil); err != nil || model != "alias" {
			t.Fatalf("ResolveAutoRoute(guest, auto) = (%q, %v), want (\"alias\", nil)", model, err)
		}
	})

	t.Run("suffixed alias targets public upstream while base alias targets restricted upstream", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.RegisterExecutor(fakeAdmitExecutor{})
		cfg := &config.Config{
			ResourceAllowlists: &config.ResourceAllowlists{
				Models: map[string][]string{"private-upstream": {ownerScope}},
			},
			OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"codex": {
					{Name: "public-upstream", Alias: "alias(high)"},
					{Name: "private-upstream", Alias: "alias"},
				},
			},
			Routing: config.RoutingConfig{
				AutoModel: config.AutoModelConfig{
					Enabled:    true,
					Candidates: []string{"alias"},
				},
			},
		}
		manager.SetConfig(cfg)
		manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
		setupAutoRouteTestAuth(t, manager, "auto-alias-reverse", &registry.ModelInfo{ID: "alias"})

		if _, model, err := manager.ResolveAutoRoute(guestCtx, "auto(high)", nil); err != nil || model != "alias(high)" {
			t.Fatalf("ResolveAutoRoute(guest, auto(high)) = (%q, %v), want (\"alias(high)\", nil)", model, err)
		}
		if _, model, err := manager.ResolveAutoRoute(guestCtx, "auto", nil); !errors.Is(err, ErrResourceAccessDenied) {
			t.Fatalf("ResolveAutoRoute(guest, auto) = (%q, %v), want ErrResourceAccessDenied", model, err)
		}
	})
}

func TestResolveAutoRoutePreservesExistingCatalogSuffix(t *testing.T) {
	ownerScope := session.CallerScope("auto-preset-owner")
	guestCtx := usage.WithCapturedIdentity(context.Background(), "auto-preset-guest", "config-inline")
	ownerCtx := usage.WithCapturedIdentity(context.Background(), "auto-preset-owner", "config-inline")

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(fakeAdmitExecutor{})
	cfg := &config.Config{
		ResourceAllowlists: &config.ResourceAllowlists{
			Models: map[string][]string{"preset(low)": {ownerScope}},
		},
		Routing: config.RoutingConfig{
			AutoModel: config.AutoModelConfig{
				Enabled:    true,
				Candidates: []string{"preset(low)"},
			},
		},
	}
	manager.SetConfig(cfg)
	setupAutoRouteTestAuth(t, manager, "auto-preset-suffix", &registry.ModelInfo{ID: "preset(low)"})

	if _, model, err := manager.ResolveAutoRoute(guestCtx, "auto(high)", nil); !errors.Is(err, ErrResourceAccessDenied) {
		t.Fatalf("ResolveAutoRoute(guest, auto(high)) = (%q, %v), want ErrResourceAccessDenied", model, err)
	}
	if _, model, err := manager.ResolveAutoRoute(ownerCtx, "auto(high)", nil); err != nil || model != "preset(low)" {
		t.Fatalf("ResolveAutoRoute(owner, auto(high)) = (%q, %v), want (\"preset(low)\", nil)", model, err)
	}
}
