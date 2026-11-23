package pluginhost

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestPreserveZcodeLegacySearchHistory(t *testing.T) {
	payload := []byte(`{"input":[
		{"role":"user","content":"original question"},
		{"type":"web_search_call","results":[{"title":"Earlier hit","url":"https://example.invalid/hit","content":"hit text","refer":"ref_1"}],"cache_control":{"type":"ephemeral"}},
		{"type":"web_search_call","action":{"type":"open_page"},"results":{"type":"tool_result","content":"[{\"url\":\"https://example.invalid/page\",\"content\":\"page text\"}]"}},
		{"type":"web_search_call","id":"mixed","results":[{"url":"https://example.invalid/native","encrypted_content":"opaque"},{"refer":"ref_2","content":"another hit"}]},
		{"type":"web_search_call","id":"results-missing"}
	]}`)
	converted := ojson.ParseBytes(preserveZcodeLegacySearchHistory(payload))
	if got := converted.Get("input.#").Int(); got != 6 {
		t.Fatalf("rewrote %d history items, want 6", got)
	}
	if converted.Get("input.0.content").String() != "original question" ||
		converted.Get("input.1.role").String() != "assistant" ||
		converted.Get("input.1.cache_control.type").String() != "ephemeral" {
		t.Fatal("message order or cache control changed")
	}
	for i, want := range []struct {
		path string
		text string
	}{
		{"input.1.content.0.text", "Earlier hit\nhttps://example.invalid/hit\nhit text"},
		{"input.2.content.0.text", "https://example.invalid/page\npage text"},
		{"input.4.content.0.text", "another hit"},
	} {
		if !strings.Contains(converted.Get(want.path).String(), want.text) {
			t.Fatalf("snapshot %d did not retain the content", i)
		}
	}
	if converted.Get("input.3.results.0.encrypted_content").String() != "opaque" ||
		converted.Get("input.5.id").String() != "results-missing" {
		t.Fatal("native or unavailable search history changed")
	}
}
