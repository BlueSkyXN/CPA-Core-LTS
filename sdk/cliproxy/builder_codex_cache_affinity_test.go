package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"strings"
	"testing"
)

func TestBuilderRejectsInvalidCodexCacheAffinity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Codex.CacheAffinity.Strategy = "invalid"
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath("config.yaml").Build()
	if err == nil || service != nil || !strings.Contains(err.Error(), "codex.cache-affinity") {
		t.Fatal("SDK accepted invalid affinity strategy")
	}
}
