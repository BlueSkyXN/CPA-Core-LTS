package registry

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestAstraGuidanceRefreshOnlyChangesKnownSentence(t *testing.T) {
	legacy := "You can use the `functions.send_user_message_async` or `functions.request_user_input_async` tool (depending on which is available) to ask."
	doc := map[string]any{"revision_hint": "preserve", "models": []any{
		map[string]any{"slug": "other", "model_messages": map[string]any{"instructions_template": legacy}},
		map[string]any{"slug": "gpt-6-astra", "future": 42, "model_messages": map[string]any{"instructions_template": legacy, "future_prompt": "preserve"}},
	}}
	raw, _ := json.Marshal(doc)
	before := bytes.Clone(raw)
	result, err := qualifyAstraAsyncGuidance(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, raw) {
		t.Fatal("input modified")
	}
	doc["models"].([]any)[1].(map[string]any)["model_messages"].(map[string]any)["instructions_template"] = "When available, y" + legacy[1:]
	expected, _ := json.Marshal(doc)
	var gotJSON, wantJSON any
	_ = json.Unmarshal(result, &gotJSON)
	_ = json.Unmarshal(expected, &wantJSON)
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatal("changes outside the known Astra sentence")
	}
	repeated, err := qualifyAstraAsyncGuidance(result)
	if err != nil || !bytes.Equal(result, repeated) {
		t.Fatal("patch is not idempotent")
	}
}

func TestAstraGuidanceFutureCatalogIsNotRewritten(t *testing.T) {
	raw := []byte(`{"models":[{"slug":"gpt-6-astra","model_messages":{"instructions_template":"New official guidance."}}], "new_top_level": true}`)
	got, err := qualifyAstraAsyncGuidance(raw)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("future catalog changed, err %v", err)
	}
}
