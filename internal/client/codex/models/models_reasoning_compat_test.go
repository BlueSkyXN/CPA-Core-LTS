package models

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestThinkingMetadataDoesNotInheritIncompatibleLevels(t *testing.T) {
	for _, tt := range []struct {
		name    string
		levels  []string
		version string
	}{
		{"empty", []string{}, "0.153.3"},
		{"budget only", nil, "0.153.3"},
		{"old client with max only", []string{"max", "ultra"}, "0.143.9"},
		{"unknown level", []string{"future-effort"}, "0.153.3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := map[string]any{"supported_reasoning_levels": []any{map[string]any{"effort": "medium"}}, "default_reasoning_level": "medium"}
			applyCodexClientThinkingMetadata(entry, &registry.ThinkingSupport{Levels: tt.levels}, tt.version)
			raw, err := json.Marshal(entry["supported_reasoning_levels"])
			if err != nil || string(raw) != "[]" {
				t.Fatalf("levels = %s, %v; want []", raw, err)
			}
			if _, exists := entry["default_reasoning_level"]; exists {
				t.Fatal("empty levels still have a default")
			}
		})
	}
}

func TestReasoningSanitizerKeepsRequiredArray(t *testing.T) {
	for _, levels := range [][]any{nil, {}, {map[string]any{"effort": "max"}, map[string]any{"effort": "ultra"}}} {
		entry := map[string]any{"supported_reasoning_levels": levels, "default_reasoning_level": "max"}
		sanitizeCodexClientReasoningMetadata(entry, "0.143.9")
		raw, err := json.Marshal(entry["supported_reasoning_levels"])
		if err != nil || string(raw) != "[]" {
			t.Fatalf("levels = %s, %v; want []", raw, err)
		}
		if _, exists := entry["default_reasoning_level"]; exists {
			t.Fatal("empty levels still have a default")
		}
	}
}
