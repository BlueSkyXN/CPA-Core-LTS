package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	carbonImageModel   = "gemini-4-carbon"
	carbonImageLimit   = 10
	carbonImageOmitted = "[Earlier image not attached to this request.]"
)

type carbonImageSlot struct {
	parts []any
	index int
	url   string
	user  bool
}

func carbonImageError(total int) error {
	return newPluginCallError("image_budget_exceeded", fmt.Sprintf("Request contains %d images for Copilot gemini-4-carbon (limit 10); send fewer images or reattach only those needed for this task", total), http.StatusBadRequest, false)
}

// Plan against the final Chat sequence; change only a typed image in the outbound copy.
// A run of assistant tool calls does not prove the user's latest attachment was consumed.
func fitCarbonImages(req pluginapi.ExecutorRequest, messages []any) error {
	if req.Model != carbonImageModel {
		return nil
	}
	var images []carbonImageSlot
	var groups [][]carbonImageSlot
	var group []carbonImageSlot
	last := -1
	for i, v := range messages {
		msg, _ := v.(map[string]any)
		role, _ := msg["role"].(string)
		if role == "assistant" {
			if len(group) > 0 {
				groups = append(groups, group)
				group = nil
			}
			if calls, _ := msg["tool_calls"].([]any); len(calls) > 0 {
				last = i
			}
		}
		parts, _ := msg["content"].([]any)
		for j, p := range parts {
			part, _ := p.(map[string]any)
			if part["type"] != "image_url" {
				continue
			}
			image, _ := part["image_url"].(map[string]any)
			url, _ := image["url"].(string)
			slot := carbonImageSlot{parts, j, url, role == "user"}
			images = append(images, slot)
			group = append(group, slot)
		}
	}
	if len(images) <= carbonImageLimit {
		return nil
	}
	failure := carbonImageError(len(images))
	if len(group) > 0 {
		groups = append(groups, group)
	}
	if last < 0 || len(req.OriginalRequest) == 0 || len(req.OriginalRequest) > maxExecutorBodyBytes {
		return failure
	}
	// Only project after the latest tool batch has closed.
	end := last + 1
	for end < len(messages) {
		m, _ := messages[end].(map[string]any)
		if m["role"] != "tool" {
			break
		}
		end++
	}
	if end == last+1 || !completeChatToolBatch(messages[last:end]) {
		return failure
	}
	allowed, ok := carbonEvictable(req, images)
	if !ok {
		return failure
	}
	answered, continued := false, false
	for _, v := range messages[end:] {
		m, _ := v.(map[string]any)
		switch m["role"] {
		case "assistant":
			answered, continued = true, false
		case "user", "system", "developer":
			if !carbonContinuationContent(m["content"], allowed) {
				return failure
			}
			if answered && m["role"] == "user" {
				continued = true
			}
		default:
			return failure
		}
	}
	if answered && !continued {
		return failure
	}
	// Images on or after the active producer never become victims, including
	// when a later text-only continuation follows the assistant's answer.
	current := map[string]bool{}
	for _, v := range messages[last:] {
		m, _ := v.(map[string]any)
		parts, _ := m["content"].([]any)
		for _, p := range parts {
			b, _ := p.(map[string]any)
			if b["type"] == "image_url" {
				im, _ := b["image_url"].(map[string]any)
				u, _ := im["url"].(string)
				current[u] = true
			}
		}
	}
	over := len(images) - carbonImageLimit
	var omit []carbonImageSlot
	for _, g := range groups {
		for i := len(g) - 1; i >= 0; i-- {
			s := g[i]
			if allowed[s.url] && !current[s.url] {
				omit = append(omit, s)
				if len(omit) == over {
					break
				}
			}
		}
		if len(omit) == over {
			break
		}
	}
	if len(omit) != over {
		return failure
	}
	for _, s := range omit {
		s.parts[s.index] = map[string]any{"type": "text", "text": carbonImageOmitted}
	}
	return nil
}

func carbonContinuationContent(content any, allowed map[string]bool) bool {
	switch v := content.(type) {
	case string:
		return carbonPinInstruction(v, allowed)
	case []any:
		for _, p := range v {
			part, _ := p.(map[string]any)
			switch part["type"] {
			case "image_url":
			case "text":
				s, ok := part["text"].(string)
				if !ok || !carbonPinInstruction(s, allowed) {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Reconcile source and wire occurrences before editing. Repeated URLs, unsupported
// shapes, non-user carriers and ambiguous tool IDs fail closed. No URL is fetched or stored.
func carbonEvictable(req pluginapi.ExecutorRequest, images []carbonImageSlot) (map[string]bool, bool) {
	var src map[string]any
	if json.Unmarshal(req.OriginalRequest, &src) != nil {
		return nil, false
	}
	// Core negotiates SourceFormat to the plugin's input format. Identify the
	// original dialect from its image-bearing structure, not that negotiated value.
	format := "openai"
	items, _ := src["messages"].([]any)
	if input, exists := src["input"]; exists {
		if _, mixed := src["messages"]; mixed {
			return nil, false
		}
		format = "openai-response"
		items, _ = input.([]any)
	} else {
		for _, value := range items {
			m, _ := value.(map[string]any)
			parts, _ := m["content"].([]any)
			for _, p := range parts {
				b, _ := p.(map[string]any)
				switch b["type"] {
				case "tool_use", "tool_result", "image":
					format = "claude"
				}
			}
		}
	}
	if len(items) == 0 {
		return nil, false
	}
	boundary := carbonHistoryBoundary(items)
	if boundary < 0 {
		return nil, false
	}
	var source []string
	allowed := make(map[string]bool, len(images))
	add := func(url string, eligible bool) bool {
		if _, exists := allowed[url]; url == "" || exists {
			return false
		}
		source = append(source, url)
		allowed[url] = eligible
		return true
	}
	calls := map[string]bool{}
	results := map[string]bool{}
	register := func(id string) bool {
		if id == "" || calls[id] {
			return false
		}
		calls[id] = true
		return true
	}
	result := func(id string) bool {
		if !calls[id] || results[id] {
			return false
		}
		results[id] = true
		return true
	}
	for i, v := range items {
		m, _ := v.(map[string]any)
		role, _ := m["role"].(string)
		parts, _ := m["content"].([]any)
		eligible := i < boundary
		switch format {
		case "openai-response":
			typ, _ := m["type"].(string)
			id, _ := m["call_id"].(string)
			switch typ {
			case "function_call", "custom_tool_call":
				if !register(id) {
					return nil, false
				}
				continue
			case "function_call_output", "custom_tool_call_output":
				if !result(id) {
					return nil, false
				}
				parts, _ = m["output"].([]any)
			default:
				eligible = false
			}
			for _, p := range parts {
				part, _ := p.(map[string]any)
				if part["type"] == "input_image" {
					u, _ := part["image_url"].(string)
					if !add(u, eligible) {
						return nil, false
					}
				}
			}
		case "claude":
			for _, p := range parts {
				b, _ := p.(map[string]any)
				switch b["type"] {
				case "tool_use":
					id, _ := b["id"].(string)
					if role != "assistant" || !register(id) {
						return nil, false
					}
				case "tool_result":
					id, _ := b["tool_use_id"].(string)
					if role != "user" || !result(id) {
						return nil, false
					}
					pp, _ := b["content"].([]any)
					for _, v := range pp {
						img, _ := v.(map[string]any)
						if img["type"] == "image" && !add(carbonClaudeURL(img), eligible) {
							return nil, false
						}
					}
				case "image":
					if !add(carbonClaudeURL(b), false) {
						return nil, false
					}
				}
			}
		case "openai":
			if role == "assistant" {
				cs, _ := m["tool_calls"].([]any)
				for _, v := range cs {
					call, _ := v.(map[string]any)
					id, _ := call["id"].(string)
					if !register(id) {
						return nil, false
					}
				}
			} else if role == "tool" {
				id, _ := m["tool_call_id"].(string)
				if !result(id) {
					return nil, false
				}
			}
			// For native Chat, an answered user turn is the unit whose old attachments
			// the operator explicitly allows us to omit. All attachments of the ongoing turn stay.
			for _, p := range parts {
				b, _ := p.(map[string]any)
				if b["type"] == "image_url" {
					if role != "user" {
						return nil, false
					}
					img, _ := b["image_url"].(map[string]any)
					u, _ := img["url"].(string)
					if !add(u, eligible) {
						return nil, false
					}
				}
			}
		default:
			return nil, false
		}
	}
	if len(results) != len(calls) {
		return nil, false
	}
	// The active instruction often precedes the tool call. Check the original,
	// where that instruction is still separate from synthetic image carriers.
	for _, v := range items[boundary+1:] {
		m, _ := v.(map[string]any)
		if m["role"] == "user" && !eachCarbonText(m["content"], func(text string) bool {
			return carbonPinInstruction(text, allowed)
		}) {
			return nil, false
		}
	}
	if len(source) != len(images) {
		return nil, false
	}
	for i, s := range images {
		if !s.user || source[i] != s.url {
			return nil, false
		}
	}
	return allowed, true
}

const carbonToolImageNotice = "Images returned by the preceding tool call(s):"

func carbonHistoryBoundary(items []any) int {
	pendingAnswer, settledAnswer, boundary := -1, -1, -1
	for i, v := range items {
		m, _ := v.(map[string]any)
		typ, _ := m["type"].(string)
		role, _ := m["role"].(string)
		calls, _ := m["tool_calls"].([]any)
		producer := typ == "function_call" || typ == "custom_tool_call" || (role == "assistant" && len(calls) > 0)
		if role == "assistant" {
			parts, _ := m["content"].([]any)
			for _, p := range parts {
				b, _ := p.(map[string]any)
				producer = producer || b["type"] == "tool_use"
			}
		}
		if producer {
			boundary, pendingAnswer = settledAnswer, -1
			continue
		}
		switch role {
		case "assistant":
			noText := eachCarbonText(m["content"], func(text string) bool { return strings.TrimSpace(text) == "" })
			if !noText {
				pendingAnswer = i
			}
		case "user":
			if pendingAnswer < 0 || typ != "" && typ != "message" {
				continue
			}
			// Require a new instruction after an answer. A progress update,
			// tool attachment or bare "continue" can still belong to the same task.
			continuing := eachCarbonText(m["content"], carbonContinuationText)
			if !continuing {
				settledAnswer, pendingAnswer = pendingAnswer, -1
			}
		}
	}
	return boundary
}

func eachCarbonText(content any, check func(string) bool) bool {
	if text, ok := content.(string); ok {
		return check(text)
	}
	parts, _ := content.([]any)
	for _, v := range parts {
		part, _ := v.(map[string]any)
		switch part["type"] {
		case "text", "input_text", "output_text":
			text, ok := part["text"].(string)
			if !ok || !check(text) {
				return false
			}
		}
	}
	return true
}

func carbonContinuationText(text string) bool {
	text = strings.TrimSpace(text)
	return text == "" || text == carbonToolImageNotice || text == "继续" || strings.EqualFold(text, "continue")
}

var (
	carbonReferenceCommand  = regexp.MustCompile(`(?i)^(?:(?:read the new files and )?compare(?: them)? with[ \t]+|continue (?:with|using)[ \t]+|(?:继续并对照|对比|对照)[ \t]*)(.+)$`)
	carbonMarkdownReference = regexp.MustCompile(`^\[[^\[\]\r\n]+\]\(([^()\r\n]+)\)$`)
)

// A supported instruction is a fixed tool control or a single explicit image
// locator. Free text may refer to any historical image, even with no image keywords.
func carbonPinInstruction(text string, allowed map[string]bool) bool {
	text = strings.TrimSpace(text)
	if carbonContinuationText(text) || strings.EqualFold(text, "run the tools") || text == "执行工具" {
		return true
	}
	if match := carbonReferenceCommand.FindStringSubmatch(text); match != nil {
		text = strings.TrimSpace(match[1])
	}
	for _, locator := range []string{text, strings.TrimRight(text, ".。!?！？")} {
		if match := carbonMarkdownReference.FindStringSubmatch(locator); match != nil {
			locator = match[1]
		}
		if strings.HasPrefix(locator, "<") && strings.HasSuffix(locator, ">") {
			locator = locator[1 : len(locator)-1]
		}
		if _, known := allowed[locator]; known {
			allowed[locator] = false
			return true
		}
	}
	return false
}

func carbonClaudeURL(img map[string]any) string {
	s, _ := img["source"].(map[string]any)
	switch s["type"] {
	case "url":
		u, _ := s["url"].(string)
		return u
	case "base64":
		m, _ := s["media_type"].(string)
		d, _ := s["data"].(string)
		if m != "" && d != "" {
			return "data:" + m + ";base64," + d
		}
	}
	return ""
}

var carbonRefusal = regexp.MustCompile(`^too many images: maximum allowed for model gemini-4-carbon is 10, got ([0-9]{1,7})$`)

func copilotChatRefusal(model string, status int, raw []byte) error {
	if model == carbonImageModel && status == http.StatusBadRequest {
		type refusal struct{ Code, Message string }
		var body struct {
			refusal
			Error *refusal
		}
		if json.Unmarshal(raw, &body) == nil {
			r := body.refusal
			if body.Error != nil {
				r = *body.Error
			}
			if r.Code == "invalid_request_body" {
				if match := carbonRefusal.FindStringSubmatch(r.Message); len(match) == 2 {
					n, _ := strconv.Atoi(match[1])
					if n > carbonImageLimit {
						return carbonImageError(n)
					}
				}
			}
		}
	}
	return copilotUpstreamError(status, raw)
}
