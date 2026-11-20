package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAutoModelConfig(t *testing.T) {
	for _, tc := range []struct {
		yaml    string
		enabled bool
		names   []string
		invalid bool
	}{
		{"port: 8317", false, nil, false}, {"config-version: 8", false, nil, false},
		{"routing: {auto-model: {enabled: true, candidates: [' free ', b]}}", true, []string{"free", "b"}, false},
		{"routing: {auto-model: {candidates: [free]}}", false, []string{"free"}, false},
		{"routing: {auto-model: {enabled: true, candidates: []}}", true, nil, false},
		{"routing: {auto-model: {enabled: true, candidates: [null]}}", false, nil, true},
		{"routing: {auto-model: {enabled: true, candidates: [' ']}}", false, nil, true},
		{"routing: {auto-model: {enabled: true, candidates: 1}}", false, nil, true},
	} {
		f := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(f, []byte(tc.yaml), 0600); err != nil {
			t.Fatal(err)
		}
		for _, read := range []func() (*Config, error){func() (*Config, error) { return LoadConfig(f) }, func() (*Config, error) { return ParseConfigBytes([]byte(tc.yaml)) }} {
			cfg, err := read()
			if (err != nil) != tc.invalid {
				t.Fatalf("%s: err=%v", tc.yaml, err)
			}
			if err == nil && (cfg.Routing.AutoModel.Enabled != tc.enabled || !slices.Equal(cfg.Routing.AutoModel.Candidates, tc.names)) {
				t.Fatalf("%s: got %+v", tc.yaml, cfg.Routing.AutoModel)
			}
		}
	}
}
