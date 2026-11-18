package diagnostics

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPayloadProtocolContentDifferences(t *testing.T) {
	for _, field := range []string{"input", "messages", "system", "contents", "systemInstruction", "system_instruction", "generationConfig", "generation_config", "thinking", "reasoning_effort", "request"} {
		t.Run(field, func(t *testing.T) {
			before, _ := json.Marshal(map[string]any{"model": "model", field: map[string]string{"content": "private-before"}})
			after, _ := json.Marshal(map[string]any{"model": "model", field: map[string]string{"content": "private-after"}})
			a, b := Snapshot(before), Snapshot(after)
			if !a.Known || !b.Known || !reflect.DeepEqual(Differences(a, b), []string{field}) {
				t.Fatalf("content change was not recorded: before=%+v after=%+v", a, b)
			}
			encoded, _ := json.Marshal(a)
			if a.Fields[field].Value != "" || strings.Contains(string(encoded), "private-before") {
				t.Fatal("snapshot retained raw protocol content")
			}
			store := NewStore()
			ref := FromContext(store.Begin(t.Context(), "request", "caller", "", "model", "openai", before))
			ObserveTransform(ref, "request_translation", before, after)
			trace, _ := store.Get(ref.ID())
			if len(trace.Events) != 1 || !reflect.DeepEqual(trace.Events[0].Changes, []string{field}) {
				t.Fatalf("transform lost content change: %+v", trace.Events)
			}
		})
	}
}

func TestPayloadProtocolContentBounds(t *testing.T) {
	body := []byte(`{"messages":[],"messages":[{"role":"user","content":"private"}]}`)
	if snapshot := Snapshot(body); snapshot.Known || snapshot.Reason != "duplicate_fields" {
		t.Fatalf("duplicate messages accepted: %+v", snapshot)
	}
	body = []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", MaxPayloadBytes) + `"}]}`)
	if snapshot := Snapshot(body); snapshot.Known || snapshot.Reason != "payload_limit" || len(snapshot.Fields) != 0 {
		t.Fatal("oversized messages bypassed evidence bounds")
	}
}
