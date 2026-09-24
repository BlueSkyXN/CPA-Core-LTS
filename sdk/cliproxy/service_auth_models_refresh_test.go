package cliproxy

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type refreshTestAuthProvider struct{}

func (refreshTestAuthProvider) Identifier() string { return "qoder" }
func (refreshTestAuthProvider) ParseAuth(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return pluginapi.AuthParseResponse{}, nil
}
func (refreshTestAuthProvider) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{}, nil
}
func (refreshTestAuthProvider) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{}, nil
}
func (refreshTestAuthProvider) RefreshAuth(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return pluginapi.AuthRefreshResponse{}, nil
}

type refreshTestModelProvider struct{ fail bool }

func (*refreshTestModelProvider) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{}, nil
}
func (p *refreshTestModelProvider) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	if p.fail {
		return pluginapi.ModelResponse{}, errors.New("catalog unavailable")
	}
	return pluginapi.ModelResponse{Provider: "qoder", Models: []pluginapi.ModelInfo{{ID: "qmodel_38max", DisplayName: "Qwen3.8-Max"}}}, nil
}

func TestRefreshAuthFileModelsRetriesPluginDiscoveryAndRegistersResult(t *testing.T) {
	auth := &coreauth.Auth{ID: "qoder-refresh-service-test", FileName: "qoder-refresh-service-test.json", Provider: "qoder", Status: coreauth.StatusActive}
	reg := GlobalModelRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	models := &refreshTestModelProvider{fail: true}
	host := pluginhost.New()
	host.RegisterPluginForTest("qoder", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		AuthProvider:  refreshTestAuthProvider{},
		ModelProvider: models,
	}})
	service := &Service{cfg: &config.Config{}, coreManager: manager, pluginHost: host}

	if err := service.refreshAuthFileModels(context.Background(), auth); err == nil {
		t.Fatal("failed plugin discovery was reported as success")
	}
	if got := reg.GetModelsForClient(auth.ID); len(got) != 0 {
		t.Fatalf("models after failed discovery = %#v", got)
	}

	models.fail = false
	if err := service.refreshAuthFileModels(context.Background(), auth); err != nil {
		t.Fatalf("retry discovery failed: %v", err)
	}
	got := reg.GetModelsForClient(auth.ID)
	if len(got) != 1 || got[0].ID != "qmodel_38max" {
		t.Fatalf("registered models = %#v", got)
	}
}
