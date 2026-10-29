package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func TestThreeProtocolClientTools(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t)
	for _, protocol := range []struct {
		path, fields, callPath string
	}{
		{"/v1/responses", `"input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"custom","name":"mcp__search__lookup","description":"Client search"}]`, "output.1.name"},
		{"/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}},{"type":"function","function":{"name":"mcp__search__lookup","parameters":{"type":"object"}}}]`, "choices.0.message.tool_calls.0.function.name"},
		{"/v1/messages", `"max_tokens":1000,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}},{"type":"custom","name":"mcp__search__lookup","input_schema":{"type":"object"}}]`, "content.1.name"},
	} {
		for _, stream := range []bool{false, true} {
			body := fmt.Sprintf(`{"model":%q,"stream":%t,%s}`, v2Model, stream, protocol.fields)
			req := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, req)
			if rec.Code != 200 || (!stream && gjson.GetBytes(rec.Body.Bytes(), protocol.callPath).String() != "lookup") || (stream && !strings.Contains(rec.Body.String(), "lookup")) {
				t.Fatalf("client tools failed: path=%s stream=%t status=%d body=%s", protocol.path, stream, rec.Code, rec.Body.String())
			}
			f.checkUsage(t, false)
			wire := f.transport.captured[len(f.transport.captured)-1]
			tools := gjson.GetBytes(wire, "tools").Array()
			if len(tools) != 2 || tools[0].Get("name").String() != "lookup" || tools[1].Get("name").String() != "mcp__search__lookup" || !tools[0].Get("input_schema").IsObject() || !tools[1].Get("input_schema").IsObject() {
				t.Fatal("client tools dropped or their schemas were lost")
			}
		}
	}
}

func TestNativeSearchToolsDispatch(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	f := newV2Fixture(t)
	for _, protocol := range []struct {
		path, fields string
	}{
		{"/v1/responses", `"input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}},{"type":"web_search"}]`},
		{"/v1/responses", `"input":[{"role":"user","content":"hello"},{"type":"additional_tools","tools":[{"type":"web_search"}]}]`},
		{"/v1/messages", `"max_tokens":1000,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search_20260209","name":"web_search"}]`},
		{"/v1/messages", `"max_tokens":1000,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search_20250305","name":"web_search","input_schema":{"type":"object"}}]`},
	} {
		for _, stream := range []bool{false, true} {
			body := fmt.Sprintf(`{"model":%q,"stream":%t,%s}`, v2Model, stream, protocol.fields)
			req := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, req)
			// Native web_search now dispatches to the fake upstream instead of
			// failing validation; only the model round-trip is asserted here.
			if rec.Code != 200 {
				t.Fatalf("native search dispatch failed: path=%s stream=%t status=%d body=%s", protocol.path, stream, rec.Code, rec.Body.String())
			}
			f.checkUsage(t, false)
			captured := f.transport.captured[len(f.transport.captured)-1]
			if !gjson.GetBytes(captured, "tools.#").Exists() || gjson.GetBytes(captured, "tools.#").Int() == 0 {
				t.Fatalf("native search tool was dropped: %s", gjson.GetBytes(captured, "tools").Raw)
			}
			if typ := gjson.GetBytes(captured, "tools.@reverse.0.type").String(); typ != "web_search_20250305" && typ != "web_search_20260209" {
				t.Fatalf("dispatched tool type = %q", typ)
			}
		}
	}
}

func TestCodingPlanDeclaresNativeSearchCapability(t *testing.T) {
	if isolateDynamic(t) {
		return
	}
	newV2Fixture(t, "glm-5.3", "glm-5.3-flash", "custom-model")
	reg := registry.GetGlobalRegistry()
	for _, model := range reg.GetModelsForClient("synthetic.json") {
		if model.ID == "custom-model" {
			// Custom IDs never inherit the probe-verified builtin capability
			// or the BigModel replay protocol discriminator.
			if model.NativeCapabilities != nil && (model.NativeCapabilities.WebSearch != nil || model.NativeCapabilities.WebSearchReplay != "") {
				t.Fatal("custom model inherited built-in native search capability or replay protocol")
			}
			continue
		}
		if model.NativeCapabilities == nil || model.NativeCapabilities.WebSearch == nil || !*model.NativeCapabilities.WebSearch {
			t.Fatal("Coding Plan native search capability was not preserved by the dynamic host")
		}
		if model.NativeCapabilities.WebSearchReplay != registry.NativeWebSearchReplayBigModel {
			t.Fatalf("Coding Plan built-in %s search replay protocol = %q, want %q", model.ID, model.NativeCapabilities.WebSearchReplay, registry.NativeWebSearchReplayBigModel)
		}
		// The registry resolver stays conservative for plugin routes (nil), so
		// accept explicit true or unknown; the plugin metadata itself is asserted
		// above. Flipping the resolver for plugin providers is a Core follow-up.
		if supported := reg.GetResponsesWebSearchCapability(model.ID); supported != nil && !*supported {
			t.Fatal("Coding Plan search capability resolved to explicit false")
		}
	}
	peer := "native-search-peer"
	off := false
	reg.RegisterClient(peer, "claude", []*registry.ModelInfo{{ID: "glm-5.3", NativeCapabilities: &registry.NativeCapabilities{WebSearch: &off}}})
	t.Cleanup(func() { reg.UnregisterClient(peer) })
	if supported := reg.GetResponsesWebSearchCapability("glm-5.3"); supported != nil && *supported {
		t.Fatal("peer false capability did not win for its own route")
	}
}
