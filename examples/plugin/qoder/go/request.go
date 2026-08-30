package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
)

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func promptFromChat(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > maxExecutorBodyBytes {
		return "", newPluginCallError("invalid_request", "Qoder Chat request is empty or exceeds the bounded body limit", http.StatusBadRequest, false)
	}
	var request chatRequest
	if errDecode := json.Unmarshal(raw, &request); errDecode != nil || len(request.Messages) == 0 {
		return "", newPluginCallError("invalid_request", "Qoder executor requires a Chat Completions messages array", http.StatusBadRequest, false)
	}
	var parts []string
	for _, message := range request.Messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		text := messageText(message.Content)
		if text == "" {
			continue
		}
		parts = append(parts, strings.ToUpper(role[:1])+role[1:]+": "+text)
	}
	prompt := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if prompt == "" {
		return "", newPluginCallError("invalid_request", "Qoder executor found no text prompt", http.StatusBadRequest, false)
	}
	return prompt, nil
}

func messageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case "text", "input_text", "output_text", "":
			if value := strings.TrimSpace(block.Text); value != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.Join(parts, "\n")
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
