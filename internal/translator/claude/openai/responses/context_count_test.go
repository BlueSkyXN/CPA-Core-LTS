package responses

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
	"testing"
)

func TestContextCountPreservesDeliveredReplayWithoutSyntheticFuture(t *testing.T) {
	native := []byte(`{"model":"glm-fixture","system":"system","tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`)
	output := []byte(`[
	 {"type":"reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"first thought"}]},
	 {"type":"web_search_call","id":"ws_srvtoolu_1","status":"completed","action":{"type":"search","query":"fixture"},"results":[{"type":"web_search_result","url":"https://example.com/","title":"Fixture","refer":"ref_1","content":"search body"}]},
	 {"type":"reasoning","encrypted_content":"opaque-2","summary":[{"type":"summary_text","text":"last thought"}]},
	 {"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"fixture\"}"}
	]`)
	info := &registry.ModelInfo{ID: "glm-fixture", IsCompat: true, NativeCapabilities: &registry.NativeCapabilities{WebSearchReplay: registry.NativeWebSearchReplayBigModel}}
	got, err := AppendResponseOutputForTokenCount("glm-fixture", native, output, info)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(got, "messages.#").Int() != 2 {
		t.Fatalf("synthetic future message added: %s", got)
	}
	blocks := gjson.GetBytes(got, "messages.1.content").Array()
	want := []string{"thinking", "server_tool_use", "web_search_tool_result", "thinking", "tool_use"}
	if len(blocks) != len(want) {
		t.Fatal(string(got))
	}
	for i, kind := range want {
		if blocks[i].Get("type").String() != kind {
			t.Fatalf("block %d = %s", i, blocks[i].Raw)
		}
	}
	if blocks[0].Get("thinking").String() != "first thought" || blocks[3].Get("thinking").String() != "last thought" || blocks[2].Get("content.0.content").String() != "search body" {
		t.Fatal("replay text lost", string(got))
	}
	for _, key := range []string{"model", "system", "tools", "messages.0"} {
		if gjson.GetBytes(native, key).Raw != gjson.GetBytes(got, key).Raw {
			t.Fatal("input changed", key)
		}
	}
	trailing := []byte(`[{"type":"reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"trailing"}]}]`)
	got, err = AppendResponseOutputForTokenCount("glm-fixture", native, trailing, info)
	if err != nil || gjson.GetBytes(got, "messages.1.content.0.thinking").String() != "trailing" {
		t.Fatal("trailing thinking dropped", err)
	}
}

func TestContextCountRejectsUnknownOutput(t *testing.T) {
	for _, output := range []string{`[{"type":"new_builtin"}]`, `[{"type":"message","role":"user"}]`, `{}`, `invalid`} {
		if _, err := AppendResponseOutputForTokenCount("glm", []byte(`{"messages":[]}`), []byte(output), nil); err == nil {
			t.Fatal("unknown output accepted", output)
		}
	}
}
