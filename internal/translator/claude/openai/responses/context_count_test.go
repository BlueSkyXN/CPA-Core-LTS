package responses

import (
	"encoding/json"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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
	if ojson.GetBytes(got, "messages.#").Int() != 2 {
		t.Fatalf("synthetic future message added: %s", got)
	}
	blocks := ojson.GetBytes(got, "messages.1.content").Array()
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
		if ojson.GetBytes(native, key).Raw != ojson.GetBytes(got, key).Raw {
			t.Fatal("input changed", key)
		}
	}
	trailing := []byte(`[{"type":"reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"trailing"}]}]`)
	got, err = AppendResponseOutputForTokenCount("glm-fixture", native, trailing, info)
	if err != nil || ojson.GetBytes(got, "messages.1.content.0.thinking").String() != "trailing" {
		t.Fatal("trailing thinking dropped", err)
	}
}

func TestContextCountMatchesCodexProjectedReplayWithHistoricalReasoning(t *testing.T) {
	request := []byte(`{"instructions":"system","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"role":"user","content":"first question"},{"type":"reasoning","encrypted_content":"history-signature","summary":[{"type":"summary_text","text":"historical thinking"}]},{"role":"assistant","content":"first answer"},{"role":"user","content":"search again"}]}`)
	output := []byte(`[
	 {"type":"reasoning","encrypted_content":"search-signature","summary":[{"type":"summary_text","text":"current thinking"}]},
	 {"type":"web_search_call","id":"ws_srvtoolu_1","status":"completed","action":{"type":"search","query":"first search"},"results":[{"type":"web_search_result","url":"https://example.com/first","title":"First","refer":"ref_1","content":"first search body"}]},
	 {"type":"web_search_call","id":"ws_srvtoolu_2","status":"completed","action":{"type":"search","query":"second search"},"results":[{"type":"web_search_result","url":"https://example.com/second","title":"Second","refer":"ref_2","content":"second search body"}]},
	 {"type":"message","role":"assistant","content":[{"type":"output_text","text":"delivered answer"}]}
	]`)
	info := &registry.ModelInfo{ID: "glm-fixture", IsCompat: true, NativeCapabilities: &registry.NativeCapabilities{WebSearchReplay: registry.NativeWebSearchReplayBigModel}}
	// Codex 的 WebSearchCall 不保留扩展 results；无缓存时下一轮只有搜索动作。
	for i, item := range util.ParseGJSONBytesNoCopy(output).Array() {
		if item.Get("type").String() == "web_search_call" {
			var err error
			output, err = ojson.DeleteBytes(output, fmt.Sprintf("%d.results", i))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	native, err := convertOpenAIResponsesRequestToClaude(info.ID, request, false, true, info, false)
	if err != nil {
		t.Fatal(err)
	}
	countPayload, err := AppendResponseOutputForTokenCount(info.ID, native, output, info)
	if err != nil {
		t.Fatal(err)
	}
	items := make([]json.RawMessage, 0)
	for _, item := range ojson.GetBytes(request, "input").Array() {
		items = append(items, json.RawMessage(item.Raw))
	}
	for _, item := range util.ParseGJSONBytesNoCopy(output).Array() {
		items = append(items, json.RawMessage(item.Raw))
	}
	items = append(items, json.RawMessage(`{"role":"user","content":"next question"}`))
	input, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	nextRequest, err := ojson.SetRawBytes(request, "input", input)
	if err != nil {
		t.Fatal(err)
	}
	nextNative, err := convertOpenAIResponsesRequestToClaude(info.ID, nextRequest, false, true, info, false)
	if err != nil {
		t.Fatal(err)
	}
	nextMessages := ojson.GetBytes(nextNative, "messages").Array()
	countMessages := ojson.GetBytes(countPayload, "messages").Array()
	if len(nextMessages) != len(countMessages)+1 || nextMessages[len(nextMessages)-1].Get("role").String() != "user" {
		t.Fatal("unexpected next-turn boundary")
	}
	for i, message := range countMessages {
		if message.Raw != nextMessages[i].Raw {
			t.Fatalf("counted history differs from actual next-turn replay at message %d", i)
		}
	}
	for _, key := range []string{"system", "tools"} {
		if ojson.GetBytes(countPayload, key).Raw != ojson.GetBytes(nextNative, key).Raw {
			t.Fatalf("counted %s differs from next-turn replay", key)
		}
	}
	for _, text := range []string{"historical thinking", "current thinking", "delivered answer"} {
		if !strings.Contains(string(countPayload), text) {
			t.Fatalf("counted replay lost %q", text)
		}
	}
	if strings.Contains(string(countPayload), "search body") {
		t.Fatal("count included results that the client did not preserve")
	}
}

func TestContextCountRejectsUnknownOutput(t *testing.T) {
	for _, output := range []string{`[{"type":"new_builtin"}]`, `[{"type":"message","role":"user"}]`, `{}`, `invalid`} {
		if _, err := AppendResponseOutputForTokenCount("glm", []byte(`{"messages":[]}`), []byte(output), nil); err == nil {
			t.Fatal("unknown output accepted", output)
		}
	}
}
