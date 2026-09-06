package management

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type flowSaveExecutor struct{}

func (*flowSaveExecutor) Identifier() string { return "flow-save-test" }
func (*flowSaveExecutor) Execute(context.Context, *coreauth.Auth, executor.Request, executor.Options) (executor.Response, error) {
	return executor.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (*flowSaveExecutor) ExecuteStream(context.Context, *coreauth.Auth, executor.Request, executor.Options) (*executor.StreamResult, error) {
	ch := make(chan executor.StreamChunk)
	close(ch)
	return &executor.StreamResult{Chunks: ch}, nil
}
func (*flowSaveExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (e *flowSaveExecutor) CountTokens(ctx context.Context, a *coreauth.Auth, r executor.Request, o executor.Options) (executor.Response, error) {
	return e.Execute(ctx, a, r, o)
}
func (*flowSaveExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestFlowConfigSaveRejectsInapplicableHistoryBeforeWriting(t *testing.T) {
	original := []byte("port: 8317\nflow-control:\n  version: 3\n  enabled: true\n  rules:\n    - id: execution\n      stage: attempt\n      scope: global\n      model: public-test\n      max-concurrent: 2\n      windows:\n        - requests: 20\n          period-ms: 60000\n")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	defer manager.CloseFlowControl()
	manager.SetConfig(cfg)
	e := &flowSaveExecutor{}
	manager.RegisterExecutor(e)
	a, err := manager.Register(context.Background(), &coreauth.Auth{ID: t.Name(), Provider: e.Identifier(), Status: coreauth.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(a.ID, e.Identifier(), []*registry.ModelInfo{{ID: "public-test"}})
	defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
	if _, err := manager.Execute(context.Background(), []string{e.Identifier()}, executor.Request{Model: "public-test"}, executor.Options{}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, path, manager)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/config.yaml", strings.NewReader(strings.ReplaceAll(string(original), "model: public-test", "model: other-test")))
	c.Request.Header.Set("Content-Type", "application/yaml")
	h.PutConfigYAML(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("rejected policy overwrote configuration file", err)
	}
	if !manager.FlowControlPolicy().Enabled {
		t.Fatal("save rejection disabled the running policy")
	}
}
