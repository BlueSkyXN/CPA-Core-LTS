package pluginhost

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginModelNativeCapabilitiesRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  bool
		want bool
	}{
		{"legacy unknown", false, false},
		{"explicit false", true, false},
		{"explicit true", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := pluginapi.ModelInfo{ID: "native-capability-model"}
			value := tt.want
			if tt.set {
				model.NativeCapabilities = &pluginapi.NativeCapabilities{WebSearch: &value}
			}
			converted := pluginModelInfoToRegistryModelInfo(model)
			returned := registryModelInfoToPluginModelInfo(converted)
			if !tt.set {
				if converted.NativeCapabilities != nil || returned.NativeCapabilities != nil {
					t.Fatal("legacy unknown capability became explicit")
				}
				return
			}
			if converted.NativeCapabilities == nil || converted.NativeCapabilities.WebSearch == nil || *converted.NativeCapabilities.WebSearch != tt.want {
				t.Fatal("plugin native capability lost during registration")
			}
			if returned.NativeCapabilities == nil || returned.NativeCapabilities.WebSearch == nil || *returned.NativeCapabilities.WebSearch != tt.want {
				t.Fatal("registry native capability lost during plugin conversion")
			}
			value = !tt.want
			if *converted.NativeCapabilities.WebSearch != tt.want {
				t.Fatal("registry native capability aliases plugin memory")
			}
			*converted.NativeCapabilities.WebSearch = !tt.want
			if *returned.NativeCapabilities.WebSearch != tt.want {
				t.Fatal("returned native capability aliases registry memory")
			}
		})
	}
	model := pluginapi.ModelInfo{NativeCapabilities: &pluginapi.NativeCapabilities{}}
	converted := pluginModelInfoToRegistryModelInfo(model)
	returned := registryModelInfoToPluginModelInfo(converted)
	if converted.NativeCapabilities == nil || converted.NativeCapabilities.WebSearch != nil || returned.NativeCapabilities == nil || returned.NativeCapabilities.WebSearch != nil {
		t.Fatal("empty native capability declaration did not remain unknown")
	}
}

func TestCloneRegistryModelsClonesNativeCapabilities(t *testing.T) {
	value := false
	model := &registry.ModelInfo{ID: "native-capability-model", NativeCapabilities: &registry.NativeCapabilities{WebSearch: &value}}
	cloned := cloneRegistryModels([]*registry.ModelInfo{model, {ID: "unknown"}})
	if cloned[0].NativeCapabilities == nil || cloned[0].NativeCapabilities.WebSearch == nil || *cloned[0].NativeCapabilities.WebSearch {
		t.Fatal("explicit false capability lost in clone")
	}
	*cloned[0].NativeCapabilities.WebSearch = true
	if *model.NativeCapabilities.WebSearch {
		t.Fatal("cloned capability aliases source memory")
	}
	cloned[0].NativeCapabilities.WebSearch = nil
	if model.NativeCapabilities.WebSearch == nil || cloned[1].NativeCapabilities != nil {
		t.Fatal("cloned capability container aliases source or invents metadata")
	}
}
