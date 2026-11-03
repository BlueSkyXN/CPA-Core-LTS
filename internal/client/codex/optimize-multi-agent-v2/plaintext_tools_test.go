package multiagentv2

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

func TestPreparePlaintextToolsKeepsClientEligibility(t *testing.T) {
	before := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}}]}]}`)
	previous := CapturePlaintextToolProvenance(before, rewriteCodexMultiAgentMessageEncryption(before))
	response := []byte(`{"output":[{"type":"function_call","namespace":"collaboration","name":"send_message","arguments":"{}"}]}`)
	for _, tc := range []struct {
		name, userAgent string
		enabled         bool
	}{
		{"disabled", "Codex Desktop/0.163.0", false},
		{"unrelated client", "zcode/1.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Client.Codex.OptimizeMultiAgentV2 = tc.enabled
			headers := http.Header{"User-Agent": []string{tc.userAgent}}
			body, proof := PreparePlaintextTools(context.Background(), headers, before, cfg)
			if !bytes.Equal(body, before) {
				t.Fatal("ineligible client tool schema changed")
			}
			if got := proof.InheritTurn(previous, []byte(`{"input":[]}`)).Restore(response); !bytes.Equal(got, response) {
				t.Fatal("ineligible client inherited a previous plaintext contract")
			}
		})
	}
}

func TestPlaintextToolProvenanceRestoresOnlyConfirmedCalls(t *testing.T) {
	before := []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}]}]}]}`)
	after := rewriteCodexMultiAgentMessageEncryption(before)
	proof := CapturePlaintextToolProvenance(before, after)
	response := []byte(`{"response":{"output":[
		{"type":"function_call","namespace":"collaboration","name":"send_message","call_id":"call-1","arguments":"{\"message\":\"task\"}"},
		{"type":"function_call","namespace":"other","name":"send_message","arguments":"{}"},
		{"type":"custom_tool_call","namespace":"collaboration","name":"send_message","input":"task"},
		{"type":"function_call","namespace":"collaboration","name":"send_message","encrypted_function_args":["message"],"arguments":"{}"},
		{"type":"tool_search_call","namespace":"collaboration","name":"send_message","arguments":{}},
		{"type":"function_call","name":"collaboration__send_message","arguments":"{}"}
	]}}`)
	got := proof.Restore(response)
	for index, item := range gjson.GetBytes(got, "response.output").Array() {
		marker := item.Get("encrypted_function_args")
		switch index {
		case 0:
			if marker.Raw != "[]" || item.Get("arguments").String() != `{"message":"task"}` || item.Get("call_id").String() != "call-1" {
				t.Fatalf("confirmed tool changed beyond its marker: %s", item.Raw)
			}
		case 3:
			if marker.Raw != `["message"]` {
				t.Fatal("existing encryption metadata was overwritten")
			}
		default:
			if marker.Exists() {
				t.Fatalf("unconfirmed tool %d received a plaintext marker", index)
			}
		}
	}
	if repeated := proof.Restore(got); !bytes.Equal(repeated, got) {
		t.Fatal("restoration is not idempotent")
	}
}

func TestPlaintextToolProvenanceRejectsAmbiguousOrUnprovenSchemas(t *testing.T) {
	for _, tools := range []string{
		`[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}}]`,
		`[{"type":"namespace","name":"agents","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}}]}]`,
		`[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":false}}}}]}]`,
		`[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}},{"type":"function","name":"send_message","parameters":{"properties":{"message":{"type":"string"}}}}]}]`,
	} {
		before := []byte(`{"tools":` + tools + `}`)
		proof := CapturePlaintextToolProvenance(before, rewriteCodexMultiAgentMessageEncryption(before))
		payload := []byte(`{"item":{"type":"function_call","namespace":"collaboration","name":"send_message","arguments":"{}"}}`)
		if got := proof.Restore(payload); !bytes.Equal(got, payload) {
			t.Fatalf("unproven declaration changed a response: %s", tools)
		}
	}
}

func TestPlaintextToolProvenanceTurnInheritance(t *testing.T) {
	before := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}},{"type":"function","name":"followup_task","parameters":{"properties":{"message":{"encrypted":true}}}}]}]}`)
	after := rewriteCodexMultiAgentMessageEncryption(before)
	first := CapturePlaintextToolProvenance(before, after)
	cached := CapturePlaintextToolProvenance(after, after)
	response := []byte(`{"output":[{"type":"function_call","namespace":"collaboration","name":"send_message","arguments":"{}"},{"type":"function_call","namespace":"collaboration","name":"followup_task","arguments":"{}"}]}`)
	inherited := cached.InheritTurn(first, []byte(`{"input":[]}`))
	if gjson.GetBytes(inherited.Restore(response), "output.1.encrypted_function_args").Raw != "[]" {
		t.Fatal("omitted declarations lost the accepted prior tool contract")
	}
	override := []byte(`{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":false}}}}]}]}]}`)
	next := CapturePlaintextToolProvenance(override, override).InheritTurn(first, override)
	got := next.Restore(response)
	if gjson.GetBytes(got, "output.0.encrypted_function_args").Exists() || gjson.GetBytes(got, "output.1.encrypted_function_args").Raw != "[]" {
		t.Fatalf("inline override did not revoke exactly its own identity: %s", got)
	}
	if gjson.GetBytes(first.Restore(response), "output.0.encrypted_function_args").Raw != "[]" {
		t.Fatal("the next turn mutated a prior request snapshot")
	}
	cleared := cached.InheritTurn(first, []byte(`{"tools":[]}`))
	if got := cleared.Restore(response); !bytes.Equal(got, response) {
		t.Fatal("explicit replacement inherited old plaintext proof")
	}
	if got := (PlaintextToolProvenance{}).InheritTurn(first, []byte(`{}`)).Restore(response); !bytes.Equal(got, response) {
		t.Fatal("disabled client adaptation inherited plaintext proof")
	}
}
