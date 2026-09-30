package pluginapi

import (
	"encoding/json"
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
