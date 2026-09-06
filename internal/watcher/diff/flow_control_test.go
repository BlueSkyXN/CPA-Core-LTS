package diff

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
	"strings"
	"testing"
)

func TestFlowControlChangeDetailsContainNoSelectors(t *testing.T) {
	oldCfg := &config.Config{}
	newCfg := &config.Config{FlowControl: flowcontrol.Config{Enabled: true, Rules: []flowcontrol.Rule{{ID: "r", Stage: "attempt", Scope: "account", Account: "private-ref", MaxConcurrent: 2}}}}
	text := strings.Join(BuildConfigChangeDetails(oldCfg, newCfg), "\n")
	if !strings.Contains(text, "flow-control") || strings.Contains(text, "private-ref") {
		t.Fatal(text)
	}
}
