package claudecommon

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PluginResponseState validates plugin events after the SDK's before hook.
// Built-in executors retain their existing data-line contract and state.
type PluginResponseState struct {
	Native   any
	Chat     bool
	Err      error
	Started  bool
	Terminal bool
	stop     string
	blocks   map[int]*pluginContentBlock
	searches map[string]bool
}

type pluginContentBlock struct {
	kind   string
	closed bool
	args   strings.Builder
	deltas bool
}

func PluginResponseError() error {
	return errors.New("invalid or unsupported Anthropic plugin response")
}

func supportedPluginStop(reason string, chat bool) bool {
	switch reason {
	case "refusal", "sensitive":
		return chat
	case "end_turn", "stop_sequence", "tool_use", "max_tokens":
		return true
	default:
		return false
	}
}

func validatePluginContent(block gjson.Result, searches map[string]bool, chat bool) error {
	blockType := block.Get("type").String()
	if chat && (blockType == "server_tool_use" || blockType == "web_search_tool_result" || blockType == "tool_result") {
		return PluginResponseError()
	}
	switch block.Get("type").String() {
	case "text":
		if block.Get("text").Type != gjson.String {
			return PluginResponseError()
		}
	case "thinking":
		if block.Get("thinking").Type != gjson.String {
			return PluginResponseError()
		}
	case "redacted_thinking":
		if block.Get("data").Type != gjson.String || block.Get("data").String() == "" {
			return PluginResponseError()
		}
	case "tool_use", "server_tool_use":
		if block.Get("id").String() == "" || block.Get("name").String() == "" {
			return PluginResponseError()
		}
		// GLM Coding Plan stream starts omit the input object on server_tool_use;
		// only reject an explicitly malformed non-object input.
		if input := block.Get("input"); input.Exists() && !input.IsObject() {
			return PluginResponseError()
		}
		if block.Get("type").String() == "server_tool_use" {
			// GLM Coding Plan reports its provider-executed search as web_search_prime.
			if name := block.Get("name").String(); name != "web_search" && name != "web_search_prime" {
				return PluginResponseError()
			}
			searches[block.Get("id").String()] = true
		}
	case "web_search_tool_result":
		if !searches[block.Get("tool_use_id").String()] {
			return PluginResponseError()
		}
	case "tool_result":
		// GLM Coding Plan returns provider-executed search hits as assistant-side
		// bare tool_result blocks paired with a preceding web_search(_prime)
		// server_tool_use; unpaired ones stay invalid.
		if !searches[block.Get("tool_use_id").String()] {
			return PluginResponseError()
		}
	default:
		return PluginResponseError()
	}
	return nil
}

func ValidatePluginMessage(message gjson.Result, chat bool) error {
	if message.Get("type").String() != "message" || message.Get("id").String() == "" || message.Get("role").String() != "assistant" || !message.Get("content").IsArray() || !supportedPluginStop(message.Get("stop_reason").String(), chat) {
		return PluginResponseError()
	}
	searches := make(map[string]bool)
	for _, block := range message.Get("content").Array() {
		if err := validatePluginContent(block, searches, chat); err != nil {
			return err
		}
	}
	return nil
}

func (s *PluginResponseState) Accept(line []byte) bool {
	if s.Err != nil || s.Terminal {
		return false
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if !json.Valid(payload) {
		s.Err = PluginResponseError()
		return false
	}
	root := gjson.ParseBytes(payload)
	kind := root.Get("type").String()
	fail := func() bool { s.Err = PluginResponseError(); return false }
	switch kind {
	case "message_start":
		msg := root.Get("message")
		if s.Started || msg.Get("id").String() == "" || msg.Get("type").String() != "message" || msg.Get("role").String() != "assistant" {
			return fail()
		}
		if content := msg.Get("content"); content.Exists() && (!content.IsArray() || len(content.Array()) != 0) {
			return fail()
		}
		s.Started = true
		s.blocks = make(map[int]*pluginContentBlock)
		s.searches = make(map[string]bool)
	case "content_block_start":
		index := root.Get("index")
		idx := int(index.Int())
		if !s.Started || index.Type != gjson.Number || index.Float() != float64(idx) || idx < 0 || s.blocks[idx] != nil {
			return fail()
		}
		for _, previous := range s.blocks {
			if !previous.closed {
				return fail()
			}
		}
		cb := root.Get("content_block")
		if validatePluginContent(cb, s.searches, s.Chat) != nil {
			return fail()
		}
		block := &pluginContentBlock{kind: cb.Get("type").String()}
		if input := cb.Get("input"); input.Exists() {
			block.args.WriteString(input.Raw)
		}
		s.blocks[idx] = block
	case "content_block_delta":
		index := root.Get("index")
		if index.Type != gjson.Number || index.Float() != float64(index.Int()) || index.Int() < 0 {
			return fail()
		}
		block := s.blocks[int(index.Int())]
		if block == nil || block.closed {
			return fail()
		}
		delta := root.Get("delta")
		switch delta.Get("type").String() {
		case "text_delta":
			if block.kind != "text" || delta.Get("text").Type != gjson.String {
				return fail()
			}
		case "citations_delta":
			if block.kind != "text" || !delta.Get("citation").IsObject() {
				return fail()
			}
		case "thinking_delta", "signature_delta":
			field := "thinking"
			if delta.Get("type").String() == "signature_delta" {
				field = "signature"
			}
			if block.kind != "thinking" || delta.Get(field).Type != gjson.String {
				return fail()
			}
		case "input_json_delta":
			if (block.kind != "tool_use" && block.kind != "server_tool_use") || delta.Get("partial_json").Type != gjson.String {
				return fail()
			}
			if !block.deltas {
				block.args.Reset()
				block.deltas = true
			}
			if block.args.Len()+len(delta.Get("partial_json").String()) > 50*1024*1024 {
				return fail()
			}
			block.args.WriteString(delta.Get("partial_json").String())
		default:
			return fail()
		}
	case "content_block_stop":
		index := root.Get("index")
		if index.Type != gjson.Number || index.Float() != float64(index.Int()) || index.Int() < 0 {
			return fail()
		}
		block := s.blocks[int(index.Int())]
		if block == nil || block.closed {
			return fail()
		}
		block.closed = true
	case "message_delta":
		if !s.Started {
			return fail()
		}
		if reason := root.Get("delta.stop_reason"); reason.Exists() && reason.Type != gjson.Null {
			s.stop = reason.String()
		}
	case "message_stop":
		if !s.Started || !supportedPluginStop(s.stop, s.Chat) {
			return fail()
		}
		for _, block := range s.blocks {
			if !block.closed {
				return fail()
			}
			// GLM Coding Plan stream server_tool_use blocks carry no input at all;
			// only client tool_use (and server blocks that accumulated input)
			// must end with a JSON object body.
			requiresObjectArgs := block.kind == "tool_use" || block.args.Len() > 0
			if (block.kind == "tool_use" || block.kind == "server_tool_use") && s.stop != "max_tokens" && requiresObjectArgs && (!json.Valid([]byte(block.args.String())) || !gjson.Parse(block.args.String()).IsObject()) {
				return fail()
			}
		}
		s.Terminal = true
	case "error":
		return fail()
	case "ping":
		return false
	default:
		if kind == "" {
			return fail()
		}
		return false
	}
	return true
}

func (s *PluginResponseState) ContentSeed(line []byte) []byte {
	root := gjson.ParseBytes(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
	index := root.Get("index").Int()
	var deltaType, field, value string
	switch root.Get("type").String() {
	case "content_block_start":
		block := root.Get("content_block")
		switch block.Get("type").String() {
		case "text":
			deltaType, field, value = "text_delta", "text", block.Get("text").String()
		case "thinking":
			deltaType, field, value = "thinking_delta", "thinking", block.Get("thinking").String()
		}
	case "content_block_stop":
		block := s.blocks[int(index)]
		if block != nil && !block.deltas && (block.kind == "tool_use" || block.kind == "server_tool_use") {
			deltaType, field, value = "input_json_delta", "partial_json", block.args.String()
		}
	}
	if value == "" {
		return nil
	}
	// 有些兼容端点在 start 中携带初值，旧逐行转换器只累计 delta，不能丢弃初值。
	delta := []byte(`{"type":"content_block_delta","index":0,"delta":{}}`)
	delta, _ = sjson.SetBytes(delta, "index", index)
	delta, _ = sjson.SetBytes(delta, "delta.type", deltaType)
	delta, _ = sjson.SetBytes(delta, "delta."+field, value)
	return append([]byte("data: "), delta...)
}
