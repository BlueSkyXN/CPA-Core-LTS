package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestConfigExamplePayloadParses(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(data), "# payload:\n")
	if !found {
		t.Fatal("payload example not found")
	}
	var payload strings.Builder
	payload.WriteString("payload:\n")
	for line := range strings.SplitSeq(section, "\n") {
		if line == "" {
			break
		}
		uncommented, ok := strings.CutPrefix(line, "# ")
		if !ok {
			t.Fatalf("unexpected line in payload example: %q", line)
		}
		payload.WriteString(uncommented)
		payload.WriteByte('\n')
	}

	cfg, err := ParseConfigBytes([]byte(payload.String()))
	if err != nil {
		t.Fatalf("parse uncommented payload example: %v", err)
	}
	blueRules := 0
	responseFormatFound := false
	for _, rule := range cfg.Payload.OverrideRaw {
		if _, ok := rule.Params["response_format"]; ok {
			responseFormatFound = true
		}
		for _, model := range rule.Models {
			if model.Name != "gpt-5.6-sol-blue" {
				continue
			}
			blueRules++
			if model.Protocol != "codex" || model.Scope != "requested" {
				t.Fatalf("Blue rule must match the requested Codex alias: %+v", model)
			}
			program, err := json.Marshal(rule.Params["access_programs"])
			if err != nil {
				t.Fatal(err)
			}
			if string(program) != `{"cyber":"daybreak_blue"}` {
				t.Fatalf("Blue access_programs = %s", program)
			}
		}
	}
	if blueRules != 1 {
		t.Fatalf("Blue payload rules = %d, want 1", blueRules)
	}
	if !responseFormatFound {
		t.Fatal("existing response_format override example was lost")
	}
}
