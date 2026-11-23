package pluginhost

import (
	"encoding/json"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// preserveZcodeLegacySearchHistory keeps the readable parts of history already
// present in the effective request as assistant text when native replay is unknown.
func preserveZcodeLegacySearchHistory(payload []byte) []byte {
	input := ojson.GetBytes(payload, "input").Array()
	var rewritten []json.RawMessage
	for i, item := range input {
		snapshot, keepItem := zcodeLegacySearchSnapshot(item)
		if snapshot == "" {
			if rewritten != nil {
				rewritten = append(rewritten, json.RawMessage(item.Raw))
			}
			continue
		}
		if rewritten == nil {
			rewritten = make([]json.RawMessage, 0, len(input))
			for _, earlier := range input[:i] {
				rewritten = append(rewritten, json.RawMessage(earlier.Raw))
			}
		}

		message := ojson.NewDocString(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}`)
		_ = message.Set("content.0.text", snapshot)
		if keepItem {
			rewritten = append(rewritten, json.RawMessage(item.Raw))
		} else if control := item.Get("cache_control"); control.Exists() {
			_ = message.SetRawBytes("cache_control", []byte(control.Raw))
		}
		rewritten = append(rewritten, message.Bytes())
	}
	if rewritten == nil {
		return payload
	}
	raw, err := json.Marshal(rewritten)
	if err != nil {
		return payload
	}
	updated, err := ojson.SetRawBytes(payload, "input", raw)
	if err != nil {
		return payload
	}
	return updated
}

func zcodeLegacySearchSnapshot(item ojson.Result) (string, bool) {
	if item.Get("type").String() != "web_search_call" {
		return "", false
	}
	var text strings.Builder
	appendSource := func(source ojson.Result) {
		title := strings.TrimSpace(source.Get("title").String())
		url := strings.TrimSpace(source.Get("url").String())
		content := strings.TrimSpace(source.Get("content").String())
		if title != "" || url != "" || content != "" {
			text.WriteString("\n\n" + strings.TrimSpace(strings.Join([]string{title, url, content}, "\n")))
		}
	}
	results := item.Get("results")
	keepItem := false
	if results.IsArray() {
		for _, hit := range results.Array() {
			if strings.TrimSpace(hit.Get("encrypted_content").String()) != "" || hit.Get("type").String() == "web_search_tool_result_error" {
				keepItem = true
				continue
			}
			if strings.TrimSpace(hit.Get("refer").String()) != "" {
				appendSource(hit)
			}
		}
	} else if item.Get("action.type").String() == "open_page" && results.Get("type").String() == "tool_result" {
		for _, page := range ojson.Parse(results.Get("content").String()).Array() {
			appendSource(page)
		}
	}
	if text.Len() == 0 {
		return "", false
	}
	return "Sources retrieved earlier in this conversation:" + text.String(), keepItem
}
