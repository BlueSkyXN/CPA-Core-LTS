package test

import (
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPATProviderDistributionConfig(t *testing.T) {
	raw, err := os.ReadFile("../examples/plugin/pat-providers.config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Plugins.Enabled || cfg.Plugins.Dir != "/opt/cpa-pat-plugins" {
		t.Fatal("PAT image must select its bundled plugins independently of standard Compose mounts")
	}
	if cfg.AuthDir != "/root/.cli-proxy-api" || cfg.RemoteManagement.SecretKey != "" {
		t.Fatal("distribution must use the persistent auth mount without shipping a management key")
	}
	for _, id := range []string{"cpa-provider-codebuddy", "cpa-provider-qoder"} {
		plugin, exists := cfg.Plugins.Configs[id]
		if !exists || plugin.Enabled == nil || !*plugin.Enabled || !plugin.Permissions.AuthRead || plugin.Permissions.AuthWrite {
			t.Fatalf("unexpected provider enablement/permissions: %s", id)
		}
	}
	qoder := cfg.Plugins.Configs["cpa-provider-qoder"]
	var runtime struct {
		Transport     string   `yaml:"transport"`
		Command       string   `yaml:"runner_command"`
		Args          []string `yaml:"runner_args"`
		WorkingDir    string   `yaml:"working_directory"`
		CatalogFormat string   `yaml:"direct_catalog_format"`
		CatalogURL    string   `yaml:"direct_models_endpoint"`
		Models        []any    `yaml:"direct_models"`
	}
	if err := qoder.Raw.Decode(&runtime); err != nil {
		t.Fatal(err)
	}
	if runtime.Transport != "direct_openai" || runtime.Command != "" || len(runtime.Args) != 0 || runtime.WorkingDir != "" {
		t.Fatal("Qoder configuration must use native Direct without requiring a runner or external CLI")
	}
	if runtime.CatalogFormat != "qoder" || runtime.CatalogURL != "https://gateway.qoder.com.cn/algo/api/v2/model/list" || len(runtime.Models) != 0 {
		t.Fatal("Qoder distribution must discover the account catalog instead of fixing a single model")
	}
}
