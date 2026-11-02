package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPayloadExecutionScopeSDKRejectsInvalidUpdates(t *testing.T) {
	for _, model := range []config.PayloadModelRule{
		{Name: "scope-model", AuthKind: "invalid"},
		{Name: "scope-model", RequestPaths: []string{}},
		{Name: "scope-model", RequestPaths: []string{"/v1/*"}},
	} {
		cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{model}, Params: map[string]any{"service_tier": "flex"}}}}}
		service, err := NewBuilder().WithConfig(cfg).WithConfigPath(t.TempDir() + "/config.yaml").Build()
		if service != nil || err == nil || !strings.Contains(err.Error(), "requests.payload.override") {
			t.Fatalf("Build result=%v err=%v", service, err)
		}
		original := &config.Config{}
		running := &Service{cfg: original}
		if running.applyConfigUpdateWithAuthSynthesis(nil, cfg, true) {
			t.Fatal("invalid execution scope accepted during hot reload")
		}
		if running.cfg != original || running.configSequence != 0 {
			t.Fatal("invalid reload replaced last good config")
		}
	}
}
