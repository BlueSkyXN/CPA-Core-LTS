package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexapptools"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexmetadata"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const codexDesktopUserAgentMarker = "codex desktop"

type desktopToolOverlayResult struct {
	body          []byte
	injectedCount int
	skipReason    string
}

func (m *Manager) applyCodexDesktopToolOverlay(provider string, toFormat sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, requestedModel string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || !cfg.Codex.DesktopToolOverlay.Enabled {
		return req, opts
	}

	result := buildCodexDesktopToolOverlay(
		provider,
		toFormat,
		req.Model,
		requestedModel,
		opts.SourceFormat,
		opts.Headers,
		req.Payload,
		cfg.Codex.DesktopToolOverlay.Tools,
	)
	log.WithFields(log.Fields{
		"bundle":         codexapptools.BundleVersion,
		"skip_reason":    result.skipReason,
		"injected_count": result.injectedCount,
	}).Debug("codex desktop tool overlay")
	if result.injectedCount == 0 {
		return req, opts
	}
	req.Payload = bytes.Clone(result.body)
	opts.OriginalRequest = bytes.Clone(result.body)
	return req, opts
}

func buildCodexDesktopToolOverlay(provider string, toFormat sdktranslator.Format, selectedModel, requestedModel string, sourceFormat sdktranslator.Format, headers http.Header, body []byte, configuredTools []string) desktopToolOverlayResult {
	result := desktopToolOverlayResult{body: body, skipReason: "not_applied"}
	if sourceFormat != sdktranslator.FormatOpenAIResponse {
		result.skipReason = "unsupported_source"
		return result
	}
	if !desktopToolOverlayTargetSupported(provider, toFormat) {
		result.skipReason = "unsupported_target"
		return result
	}
	if !strings.Contains(strings.ToLower(headers.Get("User-Agent")), codexDesktopUserAgentMarker) {
		result.skipReason = "desktop_user_agent_missing"
		return result
	}
	if strings.TrimSpace(selectedModel) == "" {
		result.skipReason = "selected_model_missing"
		return result
	}
	if containsFold(selectedModel, "gpt") || containsFold(requestedModel, "gpt") {
		result.skipReason = "gpt_model"
		return result
	}
	rootTurn, err := codexmetadata.IsUnambiguousRootUserTurn(body)
	if err != nil {
		result.skipReason = "root_metadata_invalid"
		return result
	}
	if !rootTurn {
		result.skipReason = "not_root_user_turn"
		return result
	}

	definitions, err := codexapptools.Select(configuredTools)
	if err != nil || len(definitions) == 0 {
		result.skipReason = "invalid_config"
		return result
	}
	root, ok := decodeJSONObject(body)
	if !ok {
		result.skipReason = "malformed_body"
		return result
	}
	if hasForbiddenDesktopOverlayTool(root) {
		result.skipReason = "forbidden_tool_surface"
		return result
	}

	collections, ok := desktopOverlayToolCollections(root)
	if !ok {
		result.skipReason = "invalid_tool_surface"
		return result
	}
	namespace, children, existingNames, found, valid := findCodexAppNamespaces(collections)
	if !valid {
		result.skipReason = "invalid_codex_app_namespace"
		return result
	}
	if !found {
		result.skipReason = "codex_app_namespace_missing"
		return result
	}

	for _, definition := range definitions {
		if _, exists := existingNames[definition.Name]; exists {
			continue
		}
		children = append(children, map[string]any{
			"type":        "function",
			"name":        definition.Name,
			"description": definition.Description,
			"parameters":  json.RawMessage(bytes.Clone(definition.Parameters)),
			"strict":      false,
		})
		existingNames[definition.Name] = struct{}{}
		result.injectedCount++
	}
	if result.injectedCount == 0 {
		result.skipReason = "no_new_tools"
		return result
	}
	namespace["tools"] = children
	updated, err := json.Marshal(root)
	if err != nil {
		result.injectedCount = 0
		result.skipReason = "marshal_failed"
		return result
	}
	result.body = updated
	result.skipReason = "applied"
	return result
}

func desktopToolOverlayTargetSupported(provider string, toFormat sdktranslator.Format) bool {
	if toFormat == sdktranslator.FormatOpenAIResponse || toFormat == sdktranslator.FormatOpenAI {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(provider), "xai") && toFormat == sdktranslator.FormatCodex
}

func containsFold(value, needle string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(needle))
}

func decodeJSONObject(body []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return root, true
}

func hasForbiddenDesktopOverlayTool(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		toolType, _ := typed["type"].(string)
		name, _ := typed["name"].(string)
		if toolType == "tool_search" || (toolType == "custom" && name == "exec") {
			return true
		}
		for _, child := range typed {
			if hasForbiddenDesktopOverlayTool(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasForbiddenDesktopOverlayTool(child) {
				return true
			}
		}
	}
	return false
}

func desktopOverlayToolCollections(root map[string]any) ([][]any, bool) {
	collections := make([][]any, 0, 2)
	if rawTools, exists := root["tools"]; exists {
		tools, ok := rawTools.([]any)
		if !ok {
			return nil, false
		}
		collections = append(collections, tools)
	}
	if rawInput, exists := root["input"]; exists {
		input, ok := rawInput.([]any)
		if !ok {
			return nil, false
		}
		for _, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if !ok || item["type"] != "additional_tools" {
				continue
			}
			rawTools, exists := item["tools"]
			if !exists {
				return nil, false
			}
			tools, ok := rawTools.([]any)
			if !ok {
				return nil, false
			}
			collections = append(collections, tools)
		}
	}
	return collections, true
}

func findCodexAppNamespaces(collections [][]any) (map[string]any, []any, map[string]struct{}, bool, bool) {
	var canonical map[string]any
	var canonicalChildren []any
	existingNames := make(map[string]struct{})
	found := false
	for _, tools := range collections {
		for _, rawTool := range tools {
			tool, ok := rawTool.(map[string]any)
			if !ok || tool["type"] != "namespace" || tool["name"] != "codex_app" {
				continue
			}
			found = true
			children, ok := tool["tools"].([]any)
			if !ok {
				return nil, nil, nil, true, false
			}
			if canonical == nil {
				canonical = tool
				canonicalChildren = children
			}
			for _, rawChild := range children {
				child, ok := rawChild.(map[string]any)
				if !ok {
					continue
				}
				if name, ok := child["name"].(string); ok && name != "" {
					existingNames[name] = struct{}{}
				}
			}
		}
	}
	return canonical, canonicalChildren, existingNames, found, true
}
