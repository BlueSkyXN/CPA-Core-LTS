package watcher

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestReasoningEffortPolicyHotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	calls := 0
	w := &Watcher{configPath: path, authDir: dir, reloadCallback: func(*config.Config) { calls++ }}
	w.SetConfig(&config.Config{Port: 8080, AuthDir: dir})
	for _, effort := range []string{"max", "invalid", "high"} {
		raw := []byte("config-version: 8\nserver:\n  port: 8080\nrequests:\n  reasoning-effort:\n    - models: ['glm-*']\n      mode: force\n      effort: " + effort + "\n")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		w.reloadConfigIfChanged()
		want, wantCalls := effort, 2
		if effort == "max" {
			wantCalls = 1
		}
		if effort == "invalid" {
			want, wantCalls = "max", 1
		}
		if calls != wantCalls || len(w.config.ReasoningEffort) != 1 || w.config.ReasoningEffort[0].Effort != want {
			t.Fatalf("reload %s: callbacks=%d", effort, calls)
		}
	}
}
