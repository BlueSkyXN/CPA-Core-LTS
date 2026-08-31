package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
}

type qoderTurnInput struct {
	Prompt       string              `json:"prompt"`
	SystemPrompt string              `json:"system_prompt,omitempty"`
	Content      []qoderContentBlock `json:"content"`
}

type qoderContentBlock struct {
	Type   string            `json:"type"`
	Text   string            `json:"text,omitempty"`
	Source *qoderImageSource `json:"source,omitempty"`
}

type qoderImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type openAIContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL json.RawMessage `json:"image_url,omitempty"`
	Source   json.RawMessage `json:"source,omitempty"`
}

func inputFromChat(raw []byte) (qoderTurnInput, error) {
	if len(raw) == 0 || len(raw) > maxExecutorBodyBytes {
		return qoderTurnInput{}, newPluginCallError("invalid_request", "Qoder Chat request is empty or exceeds the bounded body limit", http.StatusBadRequest, false)
	}
	var request chatRequest
	if errDecode := json.Unmarshal(raw, &request); errDecode != nil || len(request.Messages) == 0 {
		return qoderTurnInput{}, newPluginCallError("invalid_request", "Qoder executor requires a Chat Completions messages array", http.StatusBadRequest, false)
	}

	var systemParts []string
	var content []qoderContentBlock
	var promptParts []string
	for _, message := range request.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "" {
			role = "user"
		}
		blocks, errBlocks := qoderBlocksFromChatContent(message.Content)
		if errBlocks != nil {
			return qoderTurnInput{}, errBlocks
		}
		if toolCalls := compactJSONObjectOrArray(message.ToolCalls); toolCalls != "" {
			blocks = append(blocks, qoderContentBlock{Type: "text", Text: "Tool calls: " + toolCalls})
		}

		if role == "system" || role == "developer" {
			for _, block := range blocks {
				if block.Type != "text" {
					return qoderTurnInput{}, newPluginCallError("invalid_request", "Qoder system and developer messages support text content only", http.StatusBadRequest, false)
				}
				if text := strings.TrimSpace(block.Text); text != "" {
					systemParts = append(systemParts, text)
				}
			}
			continue
		}

		label := qoderRoleLabel(role, message)
		messageText := textFromQoderBlocks(blocks)
		if messageText != "" {
			promptParts = append(promptParts, label+": "+messageText)
		} else if hasQoderImage(blocks) {
			promptParts = append(promptParts, label+": [image input]")
		}
		if len(blocks) == 0 {
			continue
		}
		content = append(content, qoderContentBlock{Type: "text", Text: label + ":"})
		content = append(content, blocks...)
	}

	prompt := strings.TrimSpace(strings.Join(promptParts, "\n\n"))
	if len(content) == 0 || prompt == "" {
		return qoderTurnInput{}, newPluginCallError("invalid_request", "Qoder executor found no supported user, assistant, or tool content", http.StatusBadRequest, false)
	}
	return qoderTurnInput{
		Prompt:       prompt,
		SystemPrompt: strings.TrimSpace(strings.Join(systemParts, "\n\n")),
		Content:      content,
	}, nil
}

func qoderBlocksFromChatContent(raw json.RawMessage) ([]qoderContentBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text = strings.TrimSpace(text); text != "" {
			return []qoderContentBlock{{Type: "text", Text: text}}, nil
		}
		return nil, nil
	}
	var blocks []openAIContentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, newPluginCallError("invalid_request", "Qoder message content must be text or a supported content-block array", http.StatusBadRequest, false)
	}
	out := make([]qoderContentBlock, 0, len(blocks))
	for _, block := range blocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "", "text", "input_text", "output_text":
			if value := strings.TrimSpace(block.Text); value != "" {
				out = append(out, qoderContentBlock{Type: "text", Text: value})
			}
		case "image_url", "input_image", "image":
			imageSource, errImage := qoderImageFromOpenAIBlock(block)
			if errImage != nil {
				return nil, errImage
			}
			out = append(out, qoderContentBlock{Type: "image", Source: imageSource})
		default:
			return nil, newPluginCallError("invalid_request", "Qoder message contains an unsupported content block", http.StatusBadRequest, false)
		}
	}
	return out, nil
}

func qoderImageFromOpenAIBlock(block openAIContentBlock) (*qoderImageSource, error) {
	imageURL := ""
	if len(block.ImageURL) > 0 {
		if json.Unmarshal(block.ImageURL, &imageURL) != nil {
			var value struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(block.ImageURL, &value) == nil {
				imageURL = value.URL
			}
		}
	}
	if imageURL == "" && len(block.Source) > 0 {
		var source qoderImageSource
		if json.Unmarshal(block.Source, &source) == nil {
			switch source.Type {
			case "base64":
				return validateBase64ImageSource(source.MediaType, source.Data)
			case "url":
				return validateRemoteImageSource(source.URL)
			}
		}
	}
	imageURL = strings.TrimSpace(imageURL)
	if strings.HasPrefix(strings.ToLower(imageURL), "data:") {
		return qoderImageFromDataURL(imageURL)
	}
	return validateRemoteImageSource(imageURL)
}

func qoderImageFromDataURL(value string) (*qoderImageSource, error) {
	header, data, ok := strings.Cut(value, ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return nil, newPluginCallError("invalid_request", "Qoder image data URL must use base64 encoding", http.StatusBadRequest, false)
	}
	mediaType := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"))
	return validateBase64ImageSource(mediaType, data)
}

func validateBase64ImageSource(mediaType, data string) (*qoderImageSource, error) {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, newPluginCallError("invalid_request", "Qoder image media type is unsupported", http.StatusBadRequest, false)
	}
	data = strings.TrimSpace(data)
	if data == "" {
		return nil, newPluginCallError("invalid_request", "Qoder image data is empty", http.StatusBadRequest, false)
	}
	if _, errDecode := base64.StdEncoding.DecodeString(data); errDecode != nil {
		return nil, newPluginCallError("invalid_request", "Qoder image data is not valid base64", http.StatusBadRequest, false)
	}
	return &qoderImageSource{Type: "base64", MediaType: mediaType, Data: data}, nil
}

func validateRemoteImageSource(value string) (*qoderImageSource, error) {
	value = strings.TrimSpace(value)
	parsed, errParse := url.Parse(value)
	if errParse != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return nil, newPluginCallError("invalid_request", "Qoder image URL must be an absolute HTTP or HTTPS URL without credentials", http.StatusBadRequest, false)
	}
	return &qoderImageSource{Type: "url", URL: value}, nil
}

func qoderRoleLabel(role string, message chatMessage) string {
	switch role {
	case "assistant":
		return "Assistant"
	case "tool":
		identity := strings.TrimSpace(message.Name)
		if identity == "" {
			identity = strings.TrimSpace(message.ToolCallID)
		}
		if identity != "" {
			return "Tool " + identity
		}
		return "Tool"
	case "user":
		return "User"
	default:
		return strings.ToUpper(role[:1]) + role[1:]
	}
}

func textFromQoderBlocks(blocks []qoderContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func hasQoderImage(blocks []qoderContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "image" {
			return true
		}
	}
	return false
}

func compactJSONObjectOrArray(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	switch value.(type) {
	case []any, map[string]any:
	default:
		return ""
	}
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return ""
	}
	return string(encoded)
}

func validateCanonicalModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return newPluginCallError("unsupported_model", "canonical Qoder model ID is required", http.StatusBadRequest, false)
	}
	normalized := normalizeModel(model)
	for _, canonical := range canonicalQoderModelIDs {
		if model == canonical {
			return nil
		}
		if normalized == normalizeModel(canonical) {
			return newPluginCallError("unsupported_model", "Qoder model aliases are not accepted; use the canonical case-sensitive model ID", http.StatusBadRequest, false)
		}
	}
	for _, displayName := range canonicalQoderModelDisplayNames {
		if normalized == normalizeModel(displayName) {
			return newPluginCallError("unsupported_model", "Qoder display names are not executable model IDs; use the exact ID returned by /v1/models", http.StatusBadRequest, false)
		}
	}
	// Live typed model discovery may introduce new canonical IDs. Preserve them
	// exactly instead of normalizing or guessing an alias.
	return nil
}

func normalizeModel(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}
