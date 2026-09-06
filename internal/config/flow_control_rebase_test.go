package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The release baseline owns client-metadata. Flow must neither replace its
// parser/validation nor introduce the separate client-identity feature.
func TestFlowRebasePreservesClientMetadata(t *testing.T) {
	metadata := "port: 8317\ncodex:\n  client-metadata:\n    mode: strict\n    workspace-policy: drop\n"
	base, err := ParseConfigBytes([]byte(metadata))
	if err != nil {
		t.Fatal(err)
	}
	combined, err := ParseConfigBytes([]byte(metadata + "flow-control:\n  version: 3\n  enabled: true\n  rules:\n    - id: total\n      stage: attempt\n      scope: global\n      max-concurrent: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base.Codex, combined.Codex) {
		t.Fatal("Flow changed existing Codex configuration")
	}
	if base.FlowControl.Enabled || base.FlowControl.Observation.Realtime || base.FlowControl.Observation.Resources {
		t.Fatal("absent Flow configuration enabled a new feature")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(metadata + "flow-control:\n  version: 3\n  enabled: false\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base.Codex.ClientMetadata, loaded.Codex.ClientMetadata) {
		t.Fatal("load and parse disagree about client-metadata")
	}
}

func TestFlowRebaseInvalidConfigDoesNotPersist(t *testing.T) {
	for _, extra := range []string{
		"codex:\n  client-metadata:\n    mode: repiar\nflow-control:\n  enabled: false\n",
		"codex:\n  client-metadata:\n    mode: repair\nflow-control:\n  enabled: true\n  rules:\n    - id: bad\n      stage: missing\n      scope: global\n      max-concurrent: 1\n",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		original := "remote-management:\n  secret-key: fixture-only-not-a-real-secret\n" + extra
		if err := os.WriteFile(path, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfigOptional(path, false); err == nil {
			t.Fatal("invalid configuration accepted")
		}
		if _, err := ParseConfigBytes([]byte(original)); err == nil {
			t.Fatal("byte parser accepted invalid configuration")
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != original {
			t.Fatal("invalid configuration modified the file")
		}
	}
}
