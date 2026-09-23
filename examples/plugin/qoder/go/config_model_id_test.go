package main

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDirectModelIDCharacters(t *testing.T) {
	for _, tt := range []struct {
		id    string
		valid bool
	}{
		{"qmodel_38max", true}, {"fixture-model", true}, {"model-rnx0", true},
		{"model\x00bad", false}, {"model\nbad", false}, {"model\rbad", false},
	} {
		t.Run(tt.id, func(t *testing.T) {
			raw, err := yaml.Marshal(map[string]any{
				"direct_endpoint": "http://127.0.0.1:9/v1/chat/completions",
				"direct_models":   []map[string]string{{"id": tt.id}},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodePluginConfig(raw)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
}
