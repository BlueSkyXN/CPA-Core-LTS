package watcher

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestReloadRetainsValidCodexCacheAffinity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{Port: 8080, AuthDir: dir, Codex: config.CodexConfig{CacheAffinity: config.CodexCacheAffinityConfig{Strategy: "stable-id"}}}
	calls := 0
	w := &Watcher{configPath: path, authDir: dir, reloadCallback: func(*config.Config) { calls++ }}
	w.SetConfig(cfg)
	for _, strategy := range []string{"invalid", "client-aware"} {
		raw := []byte("port: 8080\nauth-dir: " + dir + "\ncodex:\n  cache-affinity:\n    strategy: " + strategy + "\n")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		w.reloadConfigIfChanged()
		if strategy == "invalid" {
			if calls != 0 || w.config.Codex.CacheAffinity.Strategy != "stable-id" {
				t.Fatal("invalid reload replaced config")
			}
		} else if calls != 1 || w.config.Codex.CacheAffinity.Strategy != "client-aware" {
			t.Fatal("valid reload not applied")
		}
	}
}
