package pluginhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestLifecycleCarriesEquivalentConfigJSON(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{registerResult: validTestPlugin("config-json")})
	_, err := registerRPCPlugin(context.Background(), nil, "config-json", lookup, pluginabi.MethodPluginRegister, []byte("enabled: true\nmode: preserve\nitems: [one, two]\nsettings: {limit: 5}\n"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(lookup.lastLifecycle)
	var wire struct {
		ConfigJSON []byte `json:"config_json"`
	}
	_ = json.Unmarshal(raw, &wire)
	var cfg map[string]any
	if json.Unmarshal(wire.ConfigJSON, &cfg) != nil || cfg["mode"] != "preserve" || cfg["enabled"] != true {
		t.Fatal("config_json was missing or changed semantics")
	}
	if len(cfg["items"].([]any)) != 2 || cfg["settings"].(map[string]any)["limit"] != float64(5) {
		t.Fatal("structured config lost")
	}
}
func TestLifecyclePreservesLegacyYAMLWithoutJSONProjection(t *testing.T) {
	for _, raw := range []string{"settings: {1: value}\n", "limit: .inf\n"} {
		t.Run(raw, func(t *testing.T) {
			lookup := newTestSymbolLookup(&testPlugin{registerResult: validTestPlugin("legacy-yaml")})
			_, err := registerRPCPlugin(context.Background(), nil, "legacy-yaml", lookup, pluginabi.MethodPluginRegister, []byte(raw))
			if err != nil {
				t.Fatalf("legacy YAML rejected: %v", err)
			}
			if string(lookup.lastLifecycle.ConfigYAML) != raw || len(lookup.lastLifecycle.ConfigJSON) != 0 {
				t.Fatalf("legacy YAML changed or lossy JSON projection supplied: YAML=%q JSON=%q", lookup.lastLifecycle.ConfigYAML, lookup.lastLifecycle.ConfigJSON)
			}
		})
	}
}

func TestAuthImportOnlyDoesNotAdvertiseOAuth(t *testing.T) {
	caps := pluginapi.Capabilities{AuthImportOnly: true, AuthProvider: fakeAuthProvider{identifier: "manual"}}
	lookup := newTestSymbolLookup(&testPlugin{registerResult: pluginapi.Plugin{Capabilities: caps}})
	registered, err := registerRPCPlugin(context.Background(), nil, "manual", lookup, pluginabi.MethodPluginRegister, nil)
	if err != nil || !registered.Capabilities.AuthImportOnly {
		t.Fatal("manual auth capability lost across RPC", err)
	}
	host := newHostWithRecords(capabilityRecord{id: "manual", plugin: pluginapi.Plugin{Capabilities: caps}})
	info := host.RegisteredPlugins()
	if len(info) != 1 || info[0].SupportsOAuth || !info[0].SupportsAuth || info[0].AuthProvider != "manual" {
		t.Fatal("manual-only auth advertised OAuth")
	}
	if !host.HasAuthProvider("manual") {
		t.Fatal("manual auth parser lost")
	}
	if _, handled, err := host.StartLogin(context.Background(), "manual", ""); !handled || err == nil {
		t.Fatal("manual-only start login was not rejected")
	}
	if _, handled, err := host.PollLogin(context.Background(), "manual", "state"); !handled || err == nil {
		t.Fatal("manual-only poll login was not rejected")
	}
}
