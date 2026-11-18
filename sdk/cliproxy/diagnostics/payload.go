package diagnostics

import (
	"bytes"
	"encoding/json"
	"github.com/MoeForge/moego/ojson"
	"sort"
	"strings"
)

const MaxPayloadBytes = 32 << 20

type Field struct {
	Digest string `json:"digest"`
	Value  string `json:"value,omitempty"`
}
type Payload struct {
	Known  bool             `json:"known"`
	Reason string           `json:"reason,omitempty"`
	Bytes  int              `json:"bytes"`
	Fields map[string]Field `json:"fields,omitempty"`
}

// 摘要仅用于事后对比，不加入候选索引，也不证明上游 token 前缀相同。
func Snapshot(body []byte) Payload {
	out := Payload{Bytes: len(body)}
	if len(body) > MaxPayloadBytes {
		out.Reason = "payload_limit"
		return out
	}
	if !json.Valid(body) {
		out.Reason = "invalid_json"
		return out
	}
	root := ojson.ParseBytes(body)
	if !root.IsObject() {
		out.Reason = "not_object"
		return out
	}
	out.Known = true
	out.Fields = make(map[string]Field)
	selected := map[string]bool{}
	for _, key := range []string{"model", "input", "instructions", "tools", "reasoning", "text", "max_output_tokens", "service_tier", "store", "stream", "generate", "previous_response_id", "prompt_cache_key", "prompt_cache_retention", "prompt_cache_options", "context_management", "truncation"} {
		selected[key] = true
	}
	// Chat/Claude/Gemini 的内容与推理配置也参与对比，只保留摘要。
	// request 覆盖 Gemini CLI 的请求包装；连同下方子字段最多 32 项。
	for _, key := range []string{"messages", "system", "contents", "systemInstruction", "system_instruction", "generationConfig", "generation_config", "thinking", "reasoning_effort", "request"} {
		selected[key] = true
	}
	add := func(path string, value ojson.Result) {
		if !value.Exists() {
			return
		}
		f := Field{Digest: Hash(value.Raw)}
		switch path {
		case "model", "reasoning.effort", "service_tier", "prompt_cache_options.mode", "prompt_cache_options.ttl", "prompt_cache_retention", "truncation":
			f.Value = bounded(value.String())
		case "max_output_tokens", "store", "stream", "generate", "prompt_cache_options.prewarm":
			if value.Type == ojson.Number || value.Type == ojson.True || value.Type == ojson.False {
				f.Value = bounded(value.Raw)
			}
		}
		out.Fields[path] = f
	}
	// 大型 input 只扫描一次；逐路径从根检索会反复穿过长对话。
	root.ForEach(func(key, value ojson.Result) bool {
		path := strings.Clone(key.String())
		if !selected[path] {
			return true
		}
		if _, exists := out.Fields[path]; exists {
			out.Known, out.Reason = false, "duplicate_fields"
			return false
		}
		add(path, value)
		switch path {
		case "reasoning":
			add("reasoning.effort", value.Get("effort"))
		case "prompt_cache_options":
			for _, field := range []string{"comparison_response_id", "mode", "ttl", "prewarm"} {
				add(path+"."+field, value.Get(field))
			}
		}
		return true
	})
	return out
}

func Differences(a, b Payload) []string {
	if !a.Known || !b.Known {
		return nil
	}
	keys := make(map[string]bool)
	for k := range a.Fields {
		keys[k] = true
	}
	for k := range b.Fields {
		keys[k] = true
	}
	var out []string
	for k := range keys {
		if a.Fields[k].Digest != b.Fields[k].Digest {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func ObserveTransform(ctxRef *Ref, stage string, before, after []byte) {
	if ctxRef == nil || bytes.Equal(before, after) {
		return
	}
	a, b := Snapshot(before), Snapshot(after)
	changes := Differences(a, b)
	if len(changes) > 0 {
		ctxRef.Record(Event{Layer: "transform", Method: stage, Result: "changed", Changes: changes})
	}
}

func safeToken(v string) string {
	if len(v) > 192 {
		return ""
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-.:/", r)) {
			return ""
		}
	}
	return strings.Clone(v)
}
