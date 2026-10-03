package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type readinessFixture struct {
	calls int
	req   pluginapi.ReadinessRequest
	err   error
	wait  bool
}

func (*readinessFixture) Identifier() string { return "fixture-provider" }
func (*readinessFixture) Execute(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	panic("readiness must not execute")
}
func (*readinessFixture) ExecuteStream(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	panic("readiness must not stream")
}
func (*readinessFixture) CountTokens(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	panic("readiness must not count")
}
func (*readinessFixture) HttpRequest(context.Context, pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	panic("readiness must not request HTTP")
}
func (f *readinessFixture) ProbeReadiness(ctx context.Context, r pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
	if f.wait {
		<-ctx.Done()
		return pluginapi.ReadinessResponse{}, ctx.Err()
	}
	f.calls++
	f.req = r
	checks := []pluginapi.ReadinessCheck{}
	for _, level := range []pluginapi.ReadinessLevel{pluginapi.ReadinessLevelPluginInstalled, pluginapi.ReadinessLevelRunnerInstalled, pluginapi.ReadinessLevelProtocolReady, pluginapi.ReadinessLevelAuthReady} {
		checks = append(checks, pluginapi.ReadinessCheck{Level: level, State: pluginapi.ReadinessStateReady})
	}
	return pluginapi.ReadinessResponse{Provider: f.Identifier(), Ready: true, Checks: checks}, f.err
}
func TestPluginReadinessManagementScopeAndErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{AuthDir: t.TempDir(), Plugins: config.PluginsConfig{Enabled: true, Dir: t.TempDir()}}
	manager := coreauth.NewManager(nil, nil, nil)
	for _, a := range []*coreauth.Auth{{ID: "selected", Provider: "fixture-provider", Status: coreauth.StatusActive, Metadata: map[string]any{"type": "fixture-provider", "secret": "synthetic-private-value"}}, {ID: "foreign", Provider: "other", Status: coreauth.StatusActive}, {ID: "disabled", Provider: "fixture-provider", Disabled: true}} {
		if _, err := manager.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	selected, _ := manager.GetByID("selected")
	foreign, _ := manager.GetByID("foreign")
	disabled, _ := manager.GetByID("disabled")
	f := &readinessFixture{}
	host := pluginhost.New()
	host.RegisterPluginForTest("fixture", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Executor: f, ProviderReadiness: f, ExecutorInputFormats: []string{"claude"}, ExecutorOutputFormats: []string{"claude"}}})
	host.SetAuthManager(manager)
	host.RegisterExecutors(manager, nil)
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	h.SetPluginHost(host)
	request := func(id, index string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Request = httptest.NewRequest("GET", "/v0/management/plugins/"+id+"/readiness?auth_index="+index, nil)
		h.GetPluginReadiness(c)
		return rec
	}
	res := request("fixture", selected.Index)
	if res.Code != 200 || !gjson.GetBytes(res.Body.Bytes(), "Ready").Bool() || f.calls != 1 || f.req.AuthID != "selected" || f.req.Purpose != pluginapi.ReadinessPurposeDiagnostic {
		t.Fatalf("selected scope failed: %d %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "synthetic-private-value") {
		t.Fatal("credential escaped provider scope")
	}
	for _, c := range []struct {
		id, index string
		status    int
	}{{"missing", "", 404}, {"fixture", "missing", 404}, {"fixture", foreign.Index, 400}, {"fixture", disabled.Index, 400}} {
		res = request(c.id, c.index)
		if res.Code != c.status {
			t.Fatalf("status=%d want=%d", res.Code, c.status)
		}
	}
	if f.calls != 1 {
		t.Fatal("invalid scope called provider")
	}
	f.err = errors.New("synthetic-private-error")
	res = request("fixture", selected.Index)
	if res.Code != http.StatusBadGateway || strings.Contains(res.Body.String(), "synthetic-private-error") {
		t.Fatal("unsafe plugin error exposed")
	}
	if _, err := host.ProbePluginReadiness(context.Background(), "different-plugin", f.Identifier(), pluginapi.ReadinessRequest{}); err == nil {
		t.Fatal("foreign plugin ownership accepted")
	}
	f.wait = true
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "fixture"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest("GET", "/v0/management/plugins/fixture/readiness", nil).WithContext(ctx)
	h.GetPluginReadiness(c)
	if rec.Code != http.StatusGatewayTimeout || strings.Contains(rec.Body.String(), "context canceled") {
		t.Fatal("canceled diagnostic was not safely terminated")
	}
	host.RegisterPluginForTest("legacy", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Executor: f}})
	res = request("legacy", "")
	if res.Code != http.StatusNotImplemented {
		t.Fatal("legacy executor advertised readiness")
	}
	cfg.Plugins.Enabled = false
	res = request("fixture", "")
	if res.Code != 503 {
		t.Fatal("disabled host probed")
	}
}
