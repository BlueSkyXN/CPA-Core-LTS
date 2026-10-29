package diff

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"strings"
	"testing"
)

func TestZcodeContextUsageDiff(t *testing.T) {
	old := &config.Config{}
	next := &config.Config{Codex: config.CodexConfig{ZcodeContextUsage: config.ZcodeContextUsageCorrect}}
	changes := strings.Join(BuildConfigChangeDetails(old, next), "\n")
	if !strings.Contains(changes, "codex.zcode-context-usage: disabled -> correct") {
		t.Fatal(changes)
	}
	next.Codex.ZcodeContextUsage = config.ZcodeContextUsageDisabled
	if changes = strings.Join(BuildConfigChangeDetails(old, next), "\n"); strings.Contains(changes, "zcode-context-usage") {
		t.Fatal("default changed", changes)
	}
}
