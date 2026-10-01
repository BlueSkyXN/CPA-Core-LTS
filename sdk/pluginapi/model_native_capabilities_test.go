package pluginapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelNativeCapabilitiesJSONTriState(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		present   bool
		want      bool
	}{
		{"legacy", `{"ID":"model"}`, false, false},
		{"false", `{"ID":"model","NativeCapabilities":{"WebSearch":false}}`, true, false},
		{"true", `{"ID":"model","NativeCapabilities":{"WebSearch":true}}`, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var model ModelInfo
			if err := json.Unmarshal([]byte(tt.raw), &model); err != nil {
				t.Fatal(err)
			}
			present := model.NativeCapabilities != nil && model.NativeCapabilities.WebSearch != nil
			if present != tt.present || (present && *model.NativeCapabilities.WebSearch != tt.want) {
				t.Fatal("native capability presence or value changed")
			}
			raw, err := json.Marshal(model)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if _, present := fields["NativeCapabilities"]; present != tt.present {
				t.Fatal("unknown capability was serialized or explicit false was omitted")
			}
			var legacy struct{ ID string }
			if err := json.Unmarshal(raw, &legacy); err != nil || legacy.ID != "model" {
				t.Fatal("legacy model decoder rejected additive capability metadata", err)
			}
		})
	}
}

// WebSearchReplay is an additive protocol discriminator: it must survive the
// wire round-trip when set and stay absent for legacy payloads.
func TestModelNativeCapabilitiesWebSearchReplayRoundTrip(t *testing.T) {
	var model ModelInfo
	if err := json.Unmarshal([]byte(`{"ID":"model","NativeCapabilities":{"WebSearch":true,"WebSearchReplay":"bigmodel"}}`), &model); err != nil {
		t.Fatal(err)
	}
	if model.NativeCapabilities == nil || model.NativeCapabilities.WebSearchReplay != "bigmodel" {
		t.Fatal("replay protocol lost in decode")
	}
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"WebSearchReplay":"bigmodel"`) {
		t.Fatalf("replay protocol omitted from wire form: %s", raw)
	}

	var legacy ModelInfo
	if err := json.Unmarshal([]byte(`{"ID":"model","NativeCapabilities":{"WebSearch":true}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.NativeCapabilities == nil || legacy.NativeCapabilities.WebSearchReplay != "" {
		t.Fatal("legacy payload gained a replay protocol")
	}
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "WebSearchReplay") {
		t.Fatalf("legacy payload serialized a replay protocol: %s", raw)
	}
}
